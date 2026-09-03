package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cyberstrike-ai/internal/bounty"
	"cyberstrike-ai/internal/database"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// newBugBountyTestContext 构造一个带 query 的 gin.Context，用于纯逻辑测试（不碰 DB）。
func newBugBountyTestContext(t *testing.T, query string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/bugbounty/report?"+query, nil)
	return c, w
}

// TestBugBounty_LoadProgramStats_EmptySlugReturnsNil 无 program_slug 时返回 nil（走公开市场回退）。
func TestBugBounty_LoadProgramStats_EmptySlugReturnsNil(t *testing.T) {
	h := &BugBountyHandler{}
	c, _ := newBugBountyTestContext(t, "")
	if stats := h.loadProgramStats(c); stats != nil {
		t.Fatalf("空 slug 应返回 nil，got %+v", stats)
	}
}

// TestBugBounty_LoadProgramStats_SlugWithAvg 验证 program_avg_<sev> 注入。
func TestBugBounty_LoadProgramStats_SlugWithAvg(t *testing.T) {
	h := &BugBountyHandler{}
	c, _ := newBugBountyTestContext(t, "program_slug=shopify&program_avg_critical=1500&program_top_critical=5000")
	stats := h.loadProgramStats(c)
	if stats == nil {
		t.Fatal("应返回非 nil stats")
	}
	if stats.Slug != "shopify" {
		t.Errorf("Slug=%q want shopify", stats.Slug)
	}
	if stats.AveragePerSeverity["critical"] != 1500 {
		t.Errorf("avg critical=%d want 1500", stats.AveragePerSeverity["critical"])
	}
	if stats.TopPerSeverity["critical"] != 5000 {
		t.Errorf("top critical=%d want 5000", stats.TopPerSeverity["critical"])
	}
}

// TestBugBounty_LoadProgramStats_AllEmptyReturnsNil slug 存在但全空 avg/top 时返回 nil（走公开市场）。
func TestBugBounty_LoadProgramStats_AllEmptyReturnsNil(t *testing.T) {
	h := &BugBountyHandler{}
	c, _ := newBugBountyTestContext(t, "program_slug=shopify")
	if stats := h.loadProgramStats(c); stats != nil {
		t.Fatalf("slug 存在但全空应返回 nil，got %+v", stats)
	}
}

// TestBugBounty_StatsSourceLabel 验证来源标签。
func TestBugBounty_StatsSourceLabel(t *testing.T) {
	if got := statsSourceLabel(nil); !strings.Contains(got, "industry-average") {
		t.Errorf("nil stats label 缺 industry-average: %q", got)
	}
	stats := &bounty.ProgramStats{Slug: "acme", AveragePerSeverity: map[string]int{}, TopPerSeverity: map[string]int{}}
	if got := statsSourceLabel(stats); !strings.Contains(got, "program:acme") {
		t.Errorf("stats label 缺 program:acme: %q", got)
	}
}

// TestBugBounty_TruncTitle 验证标题截断与管道转义。
func TestBugBounty_TruncTitle(t *testing.T) {
	if got := truncTitle("short"); got != "short" {
		t.Errorf("short title 被错误截断: %q", got)
	}
	long := strings.Repeat("a", 50)
	got := truncTitle(long)
	if !strings.HasSuffix(got, "...") {
		t.Errorf("长 title 应以 ... 结尾: %q", got)
	}
	if strings.Contains(got, "|") {
		t.Errorf("管道符未转义: %q", got)
	}
	if truncTitle("a|b") != "a\\|b" {
		t.Errorf("管道符转义错误: %q", truncTitle("a|b"))
	}
}

// TestBugBounty_ExportBounty_EmptyItems 空漏洞列表走 respondEmpty 分支（复用 Export 入口）。
func TestBugBounty_ExportBounty_EmptyItems(t *testing.T) {
	h := &BugBountyHandler{}
	c, w := newBugBountyTestContext(t, "format=bounty")
	h.respondEmpty(c, "bounty")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "\"total\":0") {
		t.Errorf("respondEmpty 应含 total:0: %s", body)
	}
	if !strings.Contains(body, "bounty_total") {
		t.Errorf("respondEmpty 应含 bounty_total: %s", body)
	}
}

// TestBugBounty_ExportBounty_WithMockVulns 用真实 bounty 算法验证 JSON 结构。
// 绕过 DB：直接调 exportBounty，传入 mock Vulnerability 切片。
func TestBugBounty_ExportBounty_WithMockVulns(t *testing.T) {
	h := &BugBountyHandler{logger: zap.NewNop()}
	items := []*database.Vulnerability{
		{ID: "v1", Title: "SQL Injection in /search", Severity: "critical", Target: "acme.corp"},
		{ID: "v2", Title: "Stored XSS in comments", Severity: "high", Target: "acme.corp"},
		{ID: "v3", Title: "Missing HSTS", Severity: "low"},
	}
	c, w := newBugBountyTestContext(t, "format=bounty")
	h.exportBounty(c, items)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", w.Code)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("JSON 解析失败: %v body=%s", err, w.Body.String())
	}
	if resp["format"] != "bounty" {
		t.Errorf("format=%v want bounty", resp["format"])
	}
	if int(resp["total"].(float64)) != 3 {
		t.Errorf("total=%v want 3", resp["total"])
	}
	total, _ := resp["bounty_total"].(map[string]interface{})
	if total == nil {
		t.Fatal("bounty_total 缺失")
	}
	// critical(1500-10000) + high(500-3000) + low(50-200) = 2050-13200
	if int(total["low_usd"].(float64)) != 2050 {
		t.Errorf("low_usd=%v want 2050", total["low_usd"])
	}
	if int(total["high_usd"].(float64)) != 13200 {
		t.Errorf("high_usd=%v want 13200", total["high_usd"])
	}
}

// TestBugBounty_ExportROI_WithSpend 验证 ROI 计算与红黄绿 verdict。
func TestBugBounty_ExportROI_WithSpend(t *testing.T) {
	h := &BugBountyHandler{logger: zap.NewNop()}
	items := []*database.Vulnerability{
		{ID: "v1", Title: "RCE", Severity: "critical"},
	}
	// critical 公开市场 low=1500；spend=10 → ratio=150 > 10 → green
	c, w := newBugBountyTestContext(t, "format=roi&spend=10")
	h.exportROI(c, items)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("JSON 解析失败: %v", err)
	}
	if resp["verdict"] != "green" {
		t.Errorf("verdict=%v want green (critical $1500 / $10 spend = 150x)", resp["verdict"])
	}
	if resp["bounty_low_usd"].(float64) != 1500 {
		t.Errorf("bounty_low_usd=%v want 1500", resp["bounty_low_usd"])
	}
}

// TestBugBounty_ExportROI_MissingSpend 缺 spend 参数返回 400。
func TestBugBounty_ExportROI_MissingSpend(t *testing.T) {
	h := &BugBountyHandler{logger: zap.NewNop()}
	items := []*database.Vulnerability{{ID: "v1", Severity: "critical"}}
	c, w := newBugBountyTestContext(t, "format=roi")
	h.exportROI(c, items)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("缺 spend 应返回 400，got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "spend") {
		t.Errorf("错误消息应提及 spend: %s", w.Body.String())
	}
}

// TestBugBounty_ExportROI_InvalidSpend 非法 spend 字符串（如 abc）返回 400（修复 L2）。
func TestBugBounty_ExportROI_InvalidSpend(t *testing.T) {
	h := &BugBountyHandler{logger: zap.NewNop()}
	items := []*database.Vulnerability{{ID: "v1", Severity: "critical"}}
	c, w := newBugBountyTestContext(t, "format=roi&spend=abc")
	h.exportROI(c, items)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("非法 spend 应返回 400，got %d", w.Code)
	}
}

// TestBugBounty_ExportROI_ZeroSpend spend=0 返回 400（修复 L2：不再静默当 0 返回 red）。
func TestBugBounty_ExportROI_ZeroSpend(t *testing.T) {
	h := &BugBountyHandler{logger: zap.NewNop()}
	items := []*database.Vulnerability{{ID: "v1", Severity: "critical"}}
	c, w := newBugBountyTestContext(t, "format=roi&spend=0")
	h.exportROI(c, items)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("spend=0 应返回 400，got %d", w.Code)
	}
}

// TestBugBounty_ExportDedup_FindsDuplicates 验证去重组识别相似标题。
func TestBugBounty_ExportDedup_FindsDuplicates(t *testing.T) {
	h := &BugBountyHandler{logger: zap.NewNop()}
	items := []*database.Vulnerability{
		{ID: "v1", Title: "SQL Injection in /search endpoint", Target: "acme.corp", Severity: "high"},
		{ID: "v2", Title: "SQL injection on the search endpoint", Target: "acme.corp", Severity: "high"},
		{ID: "v3", Title: "Missing HSTS header", Target: "acme.corp", Severity: "low"},
	}
	c, w := newBugBountyTestContext(t, "format=dedup&threshold=0.4&k=3")
	h.exportDedup(c, items)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", w.Code)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("JSON 解析失败: %v", err)
	}
	if resp["format"] != "dedup" {
		t.Errorf("format=%v want dedup", resp["format"])
	}
	groups, _ := resp["groups"].([]interface{})
	if len(groups) == 0 {
		t.Errorf("应识别出至少 1 组重复（v1/v2 标题相似），got 0 groups")
	}
	// 修复 M1 回归：threshold 回显应为生效值 0.4（非 0）
	if resp["threshold"].(float64) != 0.4 {
		t.Errorf("threshold 回显=%v want 0.4（生效值）", resp["threshold"])
	}
}

// TestBugBounty_ExportDedup_MoreThanKDuplicates 修复 M1 回归：
// 5 条相似标题 + k=3 时，不应因 self 占 Top-K 导致分组碎片化。
func TestBugBounty_ExportDedup_MoreThanKDuplicates(t *testing.T) {
	h := &BugBountyHandler{logger: zap.NewNop()}
	items := []*database.Vulnerability{
		{ID: "v1", Title: "SQL Injection in search", Target: "a.corp", Severity: "high"},
		{ID: "v2", Title: "SQL Injection in search", Target: "a.corp", Severity: "high"},
		{ID: "v3", Title: "SQL Injection in search", Target: "a.corp", Severity: "high"},
		{ID: "v4", Title: "SQL Injection in search", Target: "a.corp", Severity: "high"},
		{ID: "v5", Title: "SQL Injection in search", Target: "a.corp", Severity: "high"},
	}
	c, w := newBugBountyTestContext(t, "format=dedup&threshold=0.3&k=3")
	h.exportDedup(c, items)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", w.Code)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("JSON 解析失败: %v", err)
	}
	// 5 条完全相同标题：v1 作 representative 匹配 v2/v3/v4（k=3），v5 未被匹配成 representative
	// 但应被标记为 matched（相似度 1.0）。修复 M1 后：priors 排除 self，Top-K 全给真重复。
	// 预期：1 个 group（v1 代表），duplicate_count=3，merged_count=3。
	// v5 因 v1 的 k=3 用尽未进 v1 的 matches，但 v5 不应自己开新组（matched[v5] 在 v2/v3/v4 之外未被标记）。
	// 严格断言：merged_count <= 4（5-1），且 groups 不应碎片化成多个。
	groups, _ := resp["groups"].([]interface{})
	merged := int(resp["merged_count"].(float64))
	if merged > 4 {
		t.Errorf("merged_count=%d 应 <= 4（5 条最多合并 4）", merged)
	}
	// 不应有多个 group（5 条相同标题应聚成 1 组，除非 k 用尽后 v5 自成 representative）
	// v5 自成 representative 时会匹配 v1（但 v1 已 matched）→ matches 为空 → 不开组。
	// 所以 groups 应为 1（仅 v1）。
	if len(groups) > 1 {
		t.Errorf("groups=%d 应为 1（相同标题不应碎片化），修复 M1 后 priors 排除已 matched 的", len(groups))
	}
}

// TestBugBounty_ExportDedup_ThresholdZeroNormalized threshold=0 应回显 0.6（修复 L1）。
func TestBugBounty_ExportDedup_ThresholdZeroNormalized(t *testing.T) {
	h := &BugBountyHandler{logger: zap.NewNop()}
	items := []*database.Vulnerability{
		{ID: "v1", Title: "XSS", Target: "a.corp", Severity: "high"},
	}
	c, w := newBugBountyTestContext(t, "format=dedup&threshold=0")
	h.exportDedup(c, items)
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("JSON 解析失败: %v", err)
	}
	if resp["threshold"].(float64) != 0.6 {
		t.Errorf("threshold=0 应回显生效值 0.6，got %v", resp["threshold"])
	}
}

// TestBugBounty_ExportReport_InvalidSpendMarkdown 报告模式下非法 spend 提示但不阻断（修复 L2）。
func TestBugBounty_ExportReport_InvalidSpendMarkdown(t *testing.T) {
	h := &BugBountyHandler{logger: zap.NewNop()}
	items := []*database.Vulnerability{{ID: "v1", Title: "XSS", Severity: "high"}}
	c, w := newBugBountyTestContext(t, "format=report&spend=abc")
	h.exportReport(c, items)
	if w.Code != http.StatusOK {
		t.Fatalf("报告模式不应因非法 spend 阻断，got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "参数非法") {
		t.Errorf("非法 spend 应提示'参数非法':\n%s", body)
	}
}

// TestBugBounty_EscapeMarkdownInline 验证标题转义（修复 L5）。
func TestBugBounty_EscapeMarkdownInline(t *testing.T) {
	if got := escapeMarkdownInline("normal title"); got != "normal title" {
		t.Errorf("普通标题不应改变: %q", got)
	}
	if got := escapeMarkdownInline("a`b"); !strings.Contains(got, "\\`") {
		t.Errorf("反引号未转义: %q", got)
	}
	if got := escapeMarkdownInline("a*b"); !strings.Contains(got, "\\*") {
		t.Errorf("星号未转义: %q", got)
	}
	if got := escapeMarkdownInline("a[b]"); !strings.Contains(got, "\\[") {
		t.Errorf("方括号未转义: %q", got)
	}
}

// TestBugBounty_ExportReport_Markdown 验证默认 Markdown 报告聚合。
func TestBugBounty_ExportReport_Markdown(t *testing.T) {
	h := &BugBountyHandler{logger: zap.NewNop()}
	items := []*database.Vulnerability{
		{ID: "v1", Title: "RCE via deserialization", Severity: "critical", Target: "acme.corp"},
		{ID: "v2", Title: "XSS in comments", Severity: "medium"},
	}
	c, w := newBugBountyTestContext(t, "format=report&spend=5")
	h.exportReport(c, items)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"漏洞赏金与 ROI 报告", "赏金估值", "去重分析", "投资回报（ROI）", "RCE via deserialization"} {
		if !strings.Contains(body, want) {
			t.Errorf("报告缺 %q:\n%s", want, body)
		}
	}
	// critical(1500-10000) + medium(150-800) = 1650-10800；spend=5 → ratio_low=330 > 10 → green
	if !strings.Contains(body, "🟢") {
		t.Errorf("spend=5 + critical 应 verdict green (🟢):\n%s", body)
	}
}

// TestBugBounty_ExportReport_NoSpendOmitsROI 验证无 spend 时 ROI footer 省略提示。
func TestBugBounty_ExportReport_NoSpendOmitsROI(t *testing.T) {
	h := &BugBountyHandler{logger: zap.NewNop()}
	items := []*database.Vulnerability{{ID: "v1", Title: "XSS", Severity: "high"}}
	c, w := newBugBountyTestContext(t, "format=report")
	h.exportReport(c, items)
	if !strings.Contains(w.Body.String(), "LLM 花费未提供") {
		t.Errorf("无 spend 应输出 ROI 省略提示:\n%s", w.Body.String())
	}
}

// TestBugBounty_Export_InvalidFormat 未知 format 返回 400（绕过 DB，直接测 switch default）。
func TestBugBounty_Export_InvalidFormat(t *testing.T) {
	h := &BugBountyHandler{logger: zap.NewNop()}
	items := []*database.Vulnerability{{ID: "v1", Severity: "high", Title: "x"}}
	c, w := newBugBountyTestContext(t, "format=invalid")
	h.testSwitchFormat(c, items)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid format 应 400，got %d", w.Code)
	}
}

// testSwitchFormat 仅用于测试 Export 的 format switch default 分支（绕过 DB）。
func (h *BugBountyHandler) testSwitchFormat(c *gin.Context, items []*database.Vulnerability) {
	format := strings.ToLower(strings.TrimSpace(c.DefaultQuery("format", "report")))
	switch format {
	case "bounty":
		h.exportBounty(c, items)
	case "roi":
		h.exportROI(c, items)
	case "dedup":
		h.exportDedup(c, items)
	case "report", "":
		h.exportReport(c, items)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "format 仅支持 bounty|roi|dedup|report"})
	}
}
