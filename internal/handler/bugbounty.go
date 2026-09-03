// Package handler - bugbounty 提供漏洞赏金估值、重复检测、ROI 投资回报的聚合导出。
//
// 设计来源：移植自 Pentest-Swarm-AI 的 bounty/dedup/roi 三包，适配 CyberStrikeAI 的
// database.Vulnerability 数据结构。复用 VulnerabilityHandler 的筛选与 RBAC access 语义，
// 保证导出范围与 /api/vulnerabilities/export 一致，避免出现"赏金报告能看到漏洞列表
// 看不到"的越权不一致。
//
// 三个导出格式：
//   - ?format=bounty  ：per-finding 赏金区间 + 总区间 JSON
//   - ?format=roi     ：ROI 计算（需 spend 参数）+ 红/黄/绿 verdict
//   - ?format=dedup   ：去重分析（找出疑似重复提交）
//   - 默认（无 format）：聚合报告（bounty + dedup + 可选 roi footer）的 Markdown
package handler

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cyberstrike-ai/internal/bounty"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/dedup"
	"cyberstrike-ai/internal/roi"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// BugBountyHandler 漏洞赏金与 ROI 处理器。
// 复用 VulnerabilityHandler 的 db/logger/audit，保证筛选与权限语义一致。
type BugBountyHandler struct {
	vulnHandler *VulnerabilityHandler
	db          *database.DB
	logger      *zap.Logger
}

// NewBugBountyHandler 构造赏金处理器。传入已配置好的 VulnerabilityHandler 以复用其 db。
func NewBugBountyHandler(vh *VulnerabilityHandler, logger *zap.Logger) *BugBountyHandler {
	return &BugBountyHandler{
		vulnHandler: vh,
		db:          vh.db,
		logger:      logger,
	}
}

// Export GET /api/bugbounty/report
// 查询参数（筛选复用 vulnerabilities 列表）：project_id/conversation_id/severity/status/q 等。
// format=bounty|roi|dedup|report（默认 report=Markdown 聚合）。
// format=roi 需额外 spend=<float>（美元 LLM 花费）。
// format=dedup 额外 threshold=<0..1>（默认 0.6）、k=<int>（默认 3）。
// program_slug=<string> + program_avg_<sev>=<int> + program_top_<sev>=<int> 可选注入项目统计。
func (h *BugBountyHandler) Export(c *gin.Context) {
	// 修复 L3：先校验 format 合法性，避免空结果时 invalid format 误返回 200。
	format := strings.ToLower(strings.TrimSpace(c.DefaultQuery("format", "report")))
	switch format {
	case "bounty", "roi", "dedup", "report", "":
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "format 仅支持 bounty|roi|dedup|report"})
		return
	}

	filter := parseVulnerabilityListFilter(c)
	access := vulnerabilityAccessFromContext(c)

	total, err := h.db.CountVulnerabilitiesForAccess(filter, access)
	if err != nil {
		internalError(c, h.logger, "bugbounty.go Export Count", err)
		return
	}
	if total == 0 {
		// 空结果：返回合法的零值结构，避免前端 JSON 解析失败。
		h.respondEmpty(c, format)
		return
	}

	items, err := h.db.ListVulnerabilitiesForAccess(total, 0, filter, access)
	if err != nil {
		internalError(c, h.logger, "bugbounty.go Export List", err)
		return
	}

	switch format {
	case "bounty":
		h.exportBounty(c, items)
	case "roi":
		h.exportROI(c, items)
	case "dedup":
		h.exportDedup(c, items)
	case "report", "":
		h.exportReport(c, items)
	}
}

// respondEmpty 返回零值聚合结构。
func (h *BugBountyHandler) respondEmpty(c *gin.Context, format string) {
	c.JSON(http.StatusOK, gin.H{
		"format":       format,
		"total":        0,
		"bounty_total": gin.H{"low_usd": 0, "high_usd": 0},
		"dedup":        gin.H{"duplicate_groups": 0, "merged_count": 0},
		"roi":          nil,
		"message":      "当前筛选范围无漏洞",
	})
}

// loadProgramStats 从查询参数注入可选的项目级赏金统计。
// program_slug=shopify; program_avg_critical=1500; program_top_critical=5000。
// 任一 severity 的 avg/top 缺失时按 bounty.Estimate 的推导规则补全。
func (h *BugBountyHandler) loadProgramStats(c *gin.Context) *bounty.ProgramStats {
	slug := strings.TrimSpace(c.Query("program_slug"))
	if slug == "" {
		return nil
	}
	stats := &bounty.ProgramStats{
		Slug:               slug,
		AveragePerSeverity: map[string]int{},
		TopPerSeverity:     map[string]int{},
	}
	for _, sev := range []string{string(bounty.SeverityCritical), string(bounty.SeverityHigh),
		string(bounty.SeverityMedium), string(bounty.SeverityLow), string(bounty.SeverityInformational)} {
		if v := c.Query("program_avg_" + sev); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				stats.AveragePerSeverity[sev] = n
			}
		}
		if v := c.Query("program_top_" + sev); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				stats.TopPerSeverity[sev] = n
			}
		}
	}
	if len(stats.AveragePerSeverity) == 0 && len(stats.TopPerSeverity) == 0 {
		return nil // 全空则走公开市场回退
	}
	return stats
}

// exportBounty format=bounty：per-finding 赏金区间 + 总区间。
func (h *BugBountyHandler) exportBounty(c *gin.Context, items []*database.Vulnerability) {
	stats := h.loadProgramStats(c)
	type findingBounty struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		Severity string `json:"severity"`
		Target   string `json:"target"`
		LowUSD   int    `json:"low_usd"`
		HighUSD  int    `json:"high_usd"`
		Source   string `json:"source"`
	}
	out := make([]findingBounty, 0, len(items))
	var lowTotal, highTotal int
	for _, v := range items {
		if v == nil {
			continue
		}
		r := bounty.Estimate(bounty.Finding{Severity: strings.ToLower(strings.TrimSpace(v.Severity))}, stats)
		out = append(out, findingBounty{
			ID: v.ID, Title: v.Title, Severity: v.Severity, Target: v.Target,
			LowUSD: r.LowUSD, HighUSD: r.HighUSD, Source: r.Source,
		})
		lowTotal += r.LowUSD
		highTotal += r.HighUSD
	}
	c.JSON(http.StatusOK, gin.H{
		"format":   "bounty",
		"total":    len(out),
		"findings": out,
		"bounty_total": gin.H{
			"low_usd":  lowTotal,
			"high_usd": highTotal,
			"source":   statsSourceLabel(stats),
		},
	})
}

// exportROI format=roi：ROI 计算（需 spend 参数）+ 红/黄/绿 verdict。
func (h *BugBountyHandler) exportROI(c *gin.Context, items []*database.Vulnerability) {
	spendStr := strings.TrimSpace(c.Query("spend"))
	spend, err := strconv.ParseFloat(spendStr, 64)
	if err != nil || spend <= 0 {
		// 修复 L2：spend 非法字符串（如 "abc"）或 <=0 一律 400，避免静默当 0 返回 red verdict。
		c.JSON(http.StatusBadRequest, gin.H{"error": "format=roi 需 spend=<正美元> 参数（LLM 花费，需为有效正数）"})
		return
	}
	stats := h.loadProgramStats(c)
	findings := make([]bounty.Finding, 0, len(items))
	for _, v := range items {
		if v == nil {
			continue
		}
		findings = append(findings, bounty.Finding{Severity: strings.ToLower(strings.TrimSpace(v.Severity))})
	}
	r := roi.Calculate(spend, findings, stats)
	c.JSON(http.StatusOK, gin.H{
		"format":          "roi",
		"total":           len(findings),
		"spend_usd":       r.SpendUSD,
		"bounty_low_usd":  r.BountyLowUSD,
		"bounty_high_usd": r.BountyHighUSD,
		"ratio_low":       r.RatioLow,
		"ratio_high":      r.RatioHigh,
		"verdict":         string(r.Verdict),
		"footer":          r.Footer(),
	})
}

// exportDedup format=dedup：去重分析（找出疑似重复提交）。
func (h *BugBountyHandler) exportDedup(c *gin.Context, items []*database.Vulnerability) {
	threshold, _ := strconv.ParseFloat(c.DefaultQuery("threshold", "0.6"), 64)
	k, _ := strconv.Atoi(c.DefaultQuery("k", "3"))
	if k <= 0 {
		k = 3
	}

	type dupMatch struct {
		ID           string  `json:"current_id"`
		Title        string  `json:"current_title"`
		MatchedID    string  `json:"matched_id"`
		MatchedTitle string  `json:"matched_title"`
		Similarity   float64 `json:"similarity"`
		TargetMatch  bool    `json:"target_match"`
	}
	type group struct {
		ID             string     `json:"representative_id"`
		Title          string     `json:"representative_title"`
		Severity       string     `json:"severity"`
		Target         string     `json:"target"`
		DuplicateCount int        `json:"duplicate_count"`
		Matches        []dupMatch `json:"matches"`
	}

	// 以每条 finding 为"新提交"，其余为 priors，做 pairwise 去重。
	// 修复 M1：priors 必须排除"当前 v 自身"，否则 self 以 sim=1.0 占据 Top-K 名额，
	// 把真正的重复挤出结果，导致重复条目数 > K 时分组碎片化、merged_count 膨胀。
	allPriors := make([]dedup.Prior, 0, len(items))
	for _, v := range items {
		if v == nil {
			continue
		}
		allPriors = append(allPriors, dedup.Prior{ID: v.ID, Title: v.Title, Target: v.Target})
	}

	matched := map[string]bool{} // 已被归入某组的 finding ID
	groups := []group{}
	for _, v := range items {
		if v == nil || matched[v.ID] {
			continue
		}
		// 每轮迭代构造排除自身的 priors，确保 Top-K 名额全给真正的重复。
		priors := make([]dedup.Prior, 0, len(allPriors)-1)
		for _, p := range allPriors {
			if p.ID == v.ID {
				continue
			}
			if matched[p.ID] {
				continue // 已归组的不再当 priors（避免重复匹配）
			}
			priors = append(priors, p)
		}
		hits := dedup.FindDuplicates(v.Title, v.Target, priors, threshold, k)
		matches := []dupMatch{}
		for _, m := range hits {
			matches = append(matches, dupMatch{
				ID: v.ID, Title: v.Title,
				MatchedID: m.Prior.ID, MatchedTitle: m.Prior.Title,
				Similarity: m.Similarity, TargetMatch: strings.EqualFold(v.Target, m.Prior.Target) && v.Target != "",
			})
			matched[m.Prior.ID] = true
		}
		if len(matches) > 0 {
			groups = append(groups, group{
				ID: v.ID, Title: v.Title, Severity: v.Severity, Target: v.Target,
				DuplicateCount: len(matches), Matches: matches,
			})
			matched[v.ID] = true
		}
	}
	merged := 0
	for _, g := range groups {
		merged += g.DuplicateCount
	}
	c.JSON(http.StatusOK, gin.H{
		"format":           "dedup",
		"total":            len(allPriors),
		"threshold":        normalizeThresholdLabel(threshold),
		"k":                k,
		"duplicate_groups": len(groups),
		"merged_count":     merged,
		"groups":           groups,
	})
}

// exportReport 默认 format=report：聚合 Markdown 报告（bounty + dedup + 可选 roi footer）。
func (h *BugBountyHandler) exportReport(c *gin.Context, items []*database.Vulnerability) {
	stats := h.loadProgramStats(c)
	threshold, _ := strconv.ParseFloat(c.DefaultQuery("threshold", "0.6"), 64)
	k, _ := strconv.Atoi(c.DefaultQuery("k", "3"))
	if k <= 0 {
		k = 3
	}
	spendStr := strings.TrimSpace(c.Query("spend"))
	spend, spendErr := strconv.ParseFloat(spendStr, 64)
	// 修复 L2：非法 spend 字符串不静默当 0；只有"未提供"或"<=0"才省略 ROI footer。
	hasSpend := spendStr != "" && spendErr == nil && spend > 0

	var b strings.Builder
	b.WriteString("# 漏洞赏金与 ROI 报告\n\n")
	b.WriteString(fmt.Sprintf("- 生成时间: %s\n", nowLocal()))
	b.WriteString(fmt.Sprintf("- 漏洞总数: %d\n", len(items)))
	b.WriteString(fmt.Sprintf("- 赏金来源: %s\n", statsSourceLabel(stats)))
	if hasSpend {
		b.WriteString(fmt.Sprintf("- LLM 花费: $%.2f\n", spend))
	} else if spendStr != "" {
		// 非法 spend 提示（不阻断报告，但明确告知）
		b.WriteString(fmt.Sprintf("- LLM 花费: 参数非法（%q），ROI footer 省略\n", spendStr))
	}
	b.WriteString("\n## 一、赏金估值（per-finding）\n\n")
	findings := make([]bounty.Finding, 0, len(items))
	var lowTotal, highTotal int
	for _, v := range items {
		if v == nil {
			continue
		}
		sev := strings.ToLower(strings.TrimSpace(v.Severity))
		r := bounty.Estimate(bounty.Finding{Severity: sev}, stats)
		findings = append(findings, bounty.Finding{Severity: sev})
		lowTotal += r.LowUSD
		highTotal += r.HighUSD
		b.WriteString(fmt.Sprintf("- **%s** [%s] `%s` → $%d–$%d (%s)\n",
			escapeMarkdownInline(v.Title), v.Severity, v.ID, r.LowUSD, r.HighUSD, r.Source))
	}
	b.WriteString(fmt.Sprintf("\n**总赏金区间: $%d – $%d**\n\n", lowTotal, highTotal))

	// dedup 段（修复 M1：排除自身再入 Top-K，避免 >K 时分组碎片化）
	b.WriteString("## 二、去重分析\n\n")
	allPriors := make([]dedup.Prior, 0, len(items))
	for _, v := range items {
		if v == nil {
			continue
		}
		allPriors = append(allPriors, dedup.Prior{ID: v.ID, Title: v.Title, Target: v.Target})
	}
	dupCount := 0
	matched := map[string]bool{}
	for _, v := range items {
		if v == nil || matched[v.ID] {
			continue
		}
		priors := make([]dedup.Prior, 0, len(allPriors)-1)
		for _, p := range allPriors {
			if p.ID == v.ID || matched[p.ID] {
				continue
			}
			priors = append(priors, p)
		}
		hits := dedup.FindDuplicates(v.Title, v.Target, priors, threshold, k)
		for _, m := range hits {
			if dupCount == 0 {
				b.WriteString("| 当前 finding | 匹配 prior | 相似度 | 目标匹配 |\n|---|---|---|---|\n")
			}
			b.WriteString(fmt.Sprintf("| %s | %s | %.2f | %v |\n",
				truncTitle(v.Title), truncTitle(m.Prior.Title), m.Similarity,
				strings.EqualFold(v.Target, m.Prior.Target) && v.Target != ""))
			matched[m.Prior.ID] = true
			dupCount++
		}
	}
	if dupCount == 0 {
		b.WriteString("无疑似重复提交。\n")
	} else {
		b.WriteString(fmt.Sprintf("\n**疑似重复: %d 条**\n", dupCount))
	}

	// ROI footer
	if hasSpend {
		b.WriteString("\n## 三、投资回报（ROI）\n\n")
		r := roi.Calculate(spend, findings, stats)
		b.WriteString(r.Footer())
		b.WriteString("\n")
	} else if spendStr != "" {
		b.WriteString("\n## 三、投资回报（ROI）\n\nLLM 花费参数非法，ROI footer 省略。\n")
	} else {
		b.WriteString("\n## 三、投资回报（ROI）\n\nLLM 花费未提供（spend 参数），ROI footer 省略。\n")
	}

	c.Header("Content-Type", "text/markdown; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="bugbounty-report.md"`)
	_, _ = c.Writer.WriteString(b.String())
}

// statsSourceLabel 返回赏金来源标签。
func statsSourceLabel(stats *bounty.ProgramStats) string {
	if stats == nil {
		return "industry-average（HackerOne 公开市场中位数，向下取整）"
	}
	return "program:" + stats.Slug + "（注入项目统计，缺省 severity 回退 industry-average）"
}

// truncTitle 截断标题用于 Markdown 表格显示。
func truncTitle(s string) string {
	const max = 40
	s = strings.ReplaceAll(s, "|", "\\|")
	if len([]rune(s)) > max {
		return string([]rune(s)[:max]) + "..."
	}
	return s
}

// escapeMarkdownInline 转义内联 Markdown 标题中的危险字符（**、`、[ ]()），
// 避免标题含特殊字符时破坏 Markdown 结构或注入链接。修复 L5。
func escapeMarkdownInline(s string) string {
	// 转义反引号（避免标题里的反引号提前闭合代码段）
	s = strings.ReplaceAll(s, "`", "\\`")
	// 转义星号（避免 ** 触发加粗）
	s = strings.ReplaceAll(s, "*", "\\*")
	// 转义方括号（避免 [text](url) 注入链接）
	s = strings.ReplaceAll(s, "[", "\\[")
	return s
}

// nowLocal 返回本地时区的时间字符串（用于报告页脚显示，非 RFC3339 严格格式）。
func nowLocal() string {
	return time.Now().Format("2006-01-02 15:04:05")
}

// normalizeThresholdLabel 把用户传入的 threshold 规范化后回显：
// dedup 包内部把 <=0 的 threshold 当 0.6，这里对齐该语义，
// 避免 handler 回显 0 而内部按 0.6 走导致前端误解。同时把 >1 视为非法并回退默认 0.6。
func normalizeThresholdLabel(raw float64) float64 {
	if raw <= 0 || raw > 1 {
		return 0.6
	}
	return raw
}
