package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberstrike-ai/internal/config"

	"github.com/gin-gonic/gin"
)

// --- 系统提示词处理器测试 ---

// stubAgentConfigAccessor 线程安全的配置访问器测试桩。
type stubAgentConfigAccessor struct {
	mu   sync.Mutex
	path string
}

func (s *stubAgentConfigAccessor) GetAgentSystemPromptPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path
}

func (s *stubAgentConfigAccessor) SetAgentSystemPromptPath(p string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.path = p
}

func writeTestPromptFile(t *testing.T, dir, filename, name, description, content string) {
	t.Helper()
	body := "name: " + name + "\ndescription: " + description + "\ncontent: |\n"
	for _, line := range strings.Split(content, "\n") {
		body += "  " + line + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(body), 0644); err != nil {
		t.Fatalf("写入测试提示词文件失败: %v", err)
	}
}

func setupPromptsTest(t *testing.T) (*gin.Engine, *SystemPromptsHandler, *stubAgentConfigAccessor, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()

	acc := &stubAgentConfigAccessor{}
	h := NewSystemPromptsHandler(dir)
	h.SetConfig(acc)

	router := gin.New()
	router.GET("/api/system-prompts", h.ListSystemPrompts)
	router.GET("/api/system-prompts/current", h.CurrentSystemPrompt)
	router.GET("/api/system-prompts/:filename", h.GetSystemPrompt)
	router.POST("/api/system-prompts", h.CreateSystemPrompt)
	router.PUT("/api/system-prompts/:filename", h.UpdateSystemPrompt)
	router.DELETE("/api/system-prompts/:filename", h.DeleteSystemPrompt)
	router.POST("/api/system-prompts/:filename/activate", h.ActivateSystemPrompt)
	return router, h, acc, dir
}

func TestSystemPrompts_List_IncludesBuiltin(t *testing.T) {
	router, _, _, dir := setupPromptsTest(t)
	writeTestPromptFile(t, dir, "pentest.yaml", "默认渗透助手", "内置单代理提示的增强版", "你是渗透测试助手。")
	writeTestPromptFile(t, dir, "redteam.yaml", "红队专家", "红队视角", "你是红队专家。")

	req := httptest.NewRequest(http.MethodGet, "/api/system-prompts", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Prompts []promptEntry `json:"prompts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(resp.Prompts) != 3 {
		t.Fatalf("期望 3 条（2 文件 + 内置），实际 %d: %s", len(resp.Prompts), w.Body.String())
	}
	// 第一条为内置兜底
	if resp.Prompts[0].Filename != "__builtin__" || !resp.Prompts[0].IsBuiltin || !resp.Prompts[0].IsActive {
		t.Fatalf("内置条目错误: %+v", resp.Prompts[0])
	}
	if resp.Prompts[1].Name != "默认渗透助手" || resp.Prompts[1].IsActive {
		t.Fatalf("文件条目错误: %+v", resp.Prompts[1])
	}
}

func TestSystemPrompts_CreateAndActivate(t *testing.T) {
	router, _, acc, dir := setupPromptsTest(t)

	// 合法创建（.yaml 后缀）
	body2 := `{"filename":"custom.yaml","name":"自定义提示2","description":"测试2","content":"内容2"}`
	req2 := httptest.NewRequest(http.MethodPost, "/api/system-prompts", bytes.NewBufferString(body2))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("合法创建失败 %d: %s", w2.Code, w2.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "custom.yaml")); err != nil {
		t.Fatalf("文件未落盘: %v", err)
	}

	// 重复创建 → 409
	reqDup := httptest.NewRequest(http.MethodPost, "/api/system-prompts", bytes.NewBufferString(body2))
	reqDup.Header.Set("Content-Type", "application/json")
	wDup := httptest.NewRecorder()
	router.ServeHTTP(wDup, reqDup)
	if wDup.Code != http.StatusConflict {
		t.Fatalf("重复创建期望 409，实际 %d: %s", wDup.Code, wDup.Body.String())
	}

	// 激活
	req3 := httptest.NewRequest(http.MethodPost, "/api/system-prompts/custom.yaml/activate", nil)
	w3 := httptest.NewRecorder()
	router.ServeHTTP(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("激活失败 %d: %s", w3.Code, w3.Body.String())
	}
	// 激活写入的是相对 config.yaml 目录的完整路径（prompts/custom.yaml），
	// 与 agent.promptBaseDir（= configDir）拼路径约定一致，否则 agent 读不到文件。
	if got := acc.GetAgentSystemPromptPath(); got != "prompts/custom.yaml" {
		t.Fatalf("激活后路径期望 prompts/custom.yaml，实际 %q", got)
	}

	// current
	req4 := httptest.NewRequest(http.MethodGet, "/api/system-prompts/current", nil)
	w4 := httptest.NewRecorder()
	router.ServeHTTP(w4, req4)
	if w4.Code != http.StatusOK {
		t.Fatalf("current 失败 %d", w4.Code)
	}
	if !strings.Contains(w4.Body.String(), `"active_filename":"custom.yaml"`) {
		t.Fatalf("current 响应缺少激活文件名: %s", w4.Body.String())
	}

	// 列表中 custom.yaml 标记为激活
	req5 := httptest.NewRequest(http.MethodGet, "/api/system-prompts", nil)
	w5 := httptest.NewRecorder()
	router.ServeHTTP(w5, req5)
	var listResp struct {
		Prompts []promptEntry `json:"prompts"`
	}
	_ = json.Unmarshal(w5.Body.Bytes(), &listResp)
	found := false
	for _, p := range listResp.Prompts {
		if p.Filename == "custom.yaml" {
			found = true
			if !p.IsActive {
				t.Fatalf("custom.yaml 应标记激活: %+v", p)
			}
		}
		if p.Filename == "__builtin__" && p.IsActive {
			t.Fatalf("激活 custom.yaml 后内置不应为激活态")
		}
	}
	if !found {
		t.Fatalf("列表中未找到 custom.yaml")
	}
}

func TestSystemPrompts_CreateRejectsPathTraversalAndInvalidName(t *testing.T) {
	router, _, _, dir := setupPromptsTest(t)
	cases := []struct {
		body    string
		wantDir string // 创建失败时不应有任何文件落盘
	}{
		{`{"filename":"../evil.yaml","name":"x","content":"c"}`, ""},
		{`{"filename":"sub/dir.yaml","name":"x","content":"c"}`, ""},
		{`{"filename":"bad name.yaml","name":"x","content":"c"}`, ""},
		{`{"filename":"noext.yaml","name":"","content":"c"}`, ""},
		{`{"filename":"ok.yaml","name":"","content":"c"}`, ""},
		{`{"filename":"ok.yaml","name":"x","content":"  "}`, ""},
	}
	for i, tc := range cases {
		req := httptest.NewRequest(http.MethodPost, "/api/system-prompts", bytes.NewBufferString(tc.body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code == http.StatusOK {
			t.Fatalf("用例 %d 应被拒绝，实际 200: %s", i, tc.body)
		}
	}
	// ../evil.yaml 不应写到 dir 之外
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil.yaml")); err == nil {
		t.Fatalf("路径穿越未被阻止：evil.yaml 落到了上级目录")
	}
}

func TestSystemPrompts_UpdateDelete(t *testing.T) {
	router, _, acc, dir := setupPromptsTest(t)
	writeTestPromptFile(t, dir, "edit.yaml", "旧名", "旧描述", "旧内容")

	// 更新
	body := `{"name":"新名","description":"新描述","content":"新内容"}`
	req := httptest.NewRequest(http.MethodPut, "/api/system-prompts/edit.yaml", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("更新失败 %d: %s", w.Code, w.Body.String())
	}

	// 激活后删除 → 回退内置
	req2 := httptest.NewRequest(http.MethodPost, "/api/system-prompts/edit.yaml/activate", nil)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("激活失败 %d", w2.Code)
	}
	req3 := httptest.NewRequest(http.MethodDelete, "/api/system-prompts/edit.yaml", nil)
	w3 := httptest.NewRecorder()
	router.ServeHTTP(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("删除失败 %d: %s", w3.Code, w3.Body.String())
	}
	if got := acc.GetAgentSystemPromptPath(); got != "" {
		t.Fatalf("删除激活文件后应回退内置（路径空），实际 %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "edit.yaml")); !os.IsNotExist(err) {
		t.Fatalf("文件应已删除")
	}

	// 删除不存在 → 404
	req4 := httptest.NewRequest(http.MethodDelete, "/api/system-prompts/missing.yaml", nil)
	w4 := httptest.NewRecorder()
	router.ServeHTTP(w4, req4)
	if w4.Code != http.StatusNotFound {
		t.Fatalf("删除不存在期望 404，实际 %d", w4.Code)
	}
}

func TestSystemPrompts_ActivateBuiltinClearsPath(t *testing.T) {
	router, _, acc, dir := setupPromptsTest(t)
	acc.SetAgentSystemPromptPath("some.yaml")
	writeTestPromptFile(t, dir, "some.yaml", "x", "", "c")

	req := httptest.NewRequest(http.MethodPost, "/api/system-prompts/__builtin__/activate", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("激活内置失败 %d: %s", w.Code, w.Body.String())
	}
	if got := acc.GetAgentSystemPromptPath(); got != "" {
		t.Fatalf("激活内置后路径应为空，实际 %q", got)
	}
}

func TestSystemPrompts_GetPromptForEdit(t *testing.T) {
	router, _, _, dir := setupPromptsTest(t)
	writeTestPromptFile(t, dir, "read.yaml", "读取测试", "描述", "第一行\n第二行")

	req := httptest.NewRequest(http.MethodGet, "/api/system-prompts/read.yaml", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("读取失败 %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Filename    string `json:"filename"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Content     string `json:"content"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if resp.Name != "读取测试" || strings.TrimSpace(resp.Content) != "第一行\n第二行" {
		t.Fatalf("读取内容错误: %+v", resp)
	}
}

// --- 更新检测处理器测试 ---

func newTestUpdateHandler(t *testing.T, currentVersion string) (*UpdateHandler, *httptest.Server) {
	t.Helper()
	cfg := &config.Config{Version: currentVersion}
	h := NewUpdateHandler(cfg, nil)
	// 测试环境可能配置系统代理（HTTP_PROXY）；直连本地 mock，避免代理干扰。
	h.httpClient = &http.Client{Timeout: 10 * time.Second}

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			w.WriteHeader(http.StatusNotAcceptable)
			return
		}
		// 请求路径 = 注入的子路径前缀 + "/repos/lza6/CyberStrikeAI/releases/latest"
		switch {
		case strings.Contains(r.URL.Path, "/newer/"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tag_name":"v1.8.0","html_url":"https://github.com/lza6/CyberStrikeAI/releases/tag/v1.8.0","body":"- 新功能A\n- 修复B"}`))
		case strings.Contains(r.URL.Path, "/same/"):
			_, _ = w.Write([]byte(`{"tag_name":"` + currentVersion + `","html_url":"http://x","body":""}`))
		case strings.Contains(r.URL.Path, "/older/"):
			_, _ = w.Write([]byte(`{"tag_name":"v1.7.16","html_url":"http://x","body":""}`))
		case strings.Contains(r.URL.Path, "/badjson/"):
			_, _ = w.Write([]byte(`not-json`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(mock.Close)
	h.SetAPIBase(mock.URL)
	return h, mock
}

func callCheckUpdate(h *UpdateHandler) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/update/check", nil)
	h.CheckUpdate(c)
	return w
}

func TestUpdateCheck_HasUpdate(t *testing.T) {
	h, mock := newTestUpdateHandler(t, "v1.7.17")
	h.SetAPIBase(mock.URL + "/newer")
	w := callCheckUpdate(h)

	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var resp updateCheckResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !resp.HasUpdate {
		t.Fatalf("1.7.17 < 1.8.0 应有更新: %+v", resp)
	}
	if resp.LatestVersion != "1.8.0" || resp.CurrentVersion != "1.7.17" {
		t.Fatalf("版本字段错误: %+v", resp)
	}
	if resp.ReleaseURL == "" || !strings.Contains(resp.ReleaseNotes, "新功能A") {
		t.Fatalf("release 字段错误: %+v", resp)
	}
	if resp.CheckedAt == "" {
		t.Fatalf("checked_at 不应为空")
	}
}

func TestUpdateCheck_NoUpdateWhenSameOrOlder(t *testing.T) {
	h, mock := newTestUpdateHandler(t, "v1.8.0")
	h.SetAPIBase(mock.URL + "/same")
	w := callCheckUpdate(h)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", w.Code)
	}
	var resp updateCheckResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.HasUpdate {
		t.Fatalf("同版本不应有更新: %+v", resp)
	}

	h2, mock2 := newTestUpdateHandler(t, "v1.7.18")
	h2.SetAPIBase(mock2.URL + "/older")
	w2 := callCheckUpdate(h2)
	var resp2 updateCheckResponse
	_ = json.Unmarshal(w2.Body.Bytes(), &resp2)
	if resp2.HasUpdate {
		t.Fatalf("1.7.18 > 1.7.16 不应有更新: %+v", resp2)
	}
}

func TestUpdateCheck_BadJSONReturns502(t *testing.T) {
	h, mock := newTestUpdateHandler(t, "v1.7.17")
	h.SetAPIBase(mock.URL + "/badjson")
	w := callCheckUpdate(h)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("坏 JSON 期望 502，实际 %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "无法连接更新服务器") {
		t.Fatalf("错误信息不符: %s", w.Body.String())
	}
}

func TestUpdateCheck_UnreachableUpstreamReturns502(t *testing.T) {
	h, _ := newTestUpdateHandler(t, "v1.7.17")
	h.SetAPIBase("http://127.0.0.1:1") // 不可达端口
	w := callCheckUpdate(h)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("不可达上游期望 502，实际 %d: %s", w.Code, w.Body.String())
	}
}

func TestCompareVersionStrings(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.8.0", "1.7.17", 1},
		{"1.7.17", "1.7.17", 0},
		{"1.7.16", "1.7.17", -1},
		{"v1.8", "v1.7.99", 1},
		{"2.0", "1.10.0", 1},
		{"1.10", "1.9.9", 1},
		{"1.0.0", "", 1},
		{"", "1.0.0", -1},
		{"1.7.17", "v1.7.17", 0},
		// 预发布标记：rc/beta 版本小于同号正式版（S4 回归）
		{"1.8.0-rc.1", "1.8.0", -1},
		{"1.8.0-beta", "1.8.0", -1},
		{"1.8.0", "1.8.0-rc.1", 1},
		{"1.8.1", "1.8.0-rc.1", 1},
	}
	for _, tc := range cases {
		got := compareVersionStrings(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("compare(%q,%q) = %d，期望 %d", tc.a, tc.b, got, tc.want)
		}
	}
}
