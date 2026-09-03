package promptassembly

import (
	"strings"
	"testing"
)

// TestManager_RenderFull 三个 struct 全填时渲染所有块。
func TestManager_RenderFull(t *testing.T) {
	m := NewManager()
	ctx := Context{
		RepositoryInfo: RepositoryInfo{
			RepoName:      "OpenWolf",
			RepoDirectory: "/work/OpenWolf",
			BranchName:    "main",
		},
		RuntimeInfo: RuntimeInfo{
			Date:                        "2026-09-02T12:00:00Z",
			WorkingDir:                  "/work/OpenWolf",
			AdditionalAgentInstructions: "禁止提交密钥",
			AvailableHosts:              map[string]int{"localhost": 8080},
			CustomSecretsDescriptions:   map[string]string{"API_KEY": "外部 API 密钥"},
		},
		ConversationInstructions: ConversationInstructions{Content: "本轮目标：完成 J4/J5 验收"},
		RepoInstructions:         "本项目用 gofmt，禁止 tab。",
	}
	out := m.Render(ctx)
	checks := []string{
		"<REPOSITORY_INFO>",
		"OpenWolf",
		"/work/OpenWolf",
		"Branch: main",
		"<RUNTIME_INFORMATION>",
		"2026-09-02T12:00:00Z",
		"禁止提交密钥",
		"localhost:8080",
		"<CUSTOM_SECRETS>",
		"API_KEY: 外部 API 密钥",
		"<CONVERSATION_INSTRUCTIONS>",
		"本轮目标：完成 J4/J5 验收",
		"<REPOSITORY_INSTRUCTIONS>",
		"本项目用 gofmt",
	}
	for _, c := range checks {
		if !strings.Contains(out, c) {
			t.Errorf("渲染应包含 %q\n完整输出:\n%s", c, out)
		}
	}
}

// TestManager_RenderEmpty 所有字段空时不输出任何块（不污染 prompt）。
func TestManager_RenderEmpty(t *testing.T) {
	m := NewManager()
	out := m.Render(NewContext())
	if strings.TrimSpace(out) != "" {
		t.Fatalf("空 Context 应渲染为空串，got:\n%s", out)
	}
}

// TestManager_RenderIdempotent 同一 Context 多次渲染结果一致。
func TestManager_RenderIdempotent(t *testing.T) {
	m := NewManager()
	ctx := Context{
		RuntimeInfo:              RuntimeInfo{Date: "2026-09-02", WorkingDir: "/w"},
		ConversationInstructions: ConversationInstructions{Content: "固定指令"},
	}
	out1 := m.Render(ctx)
	out2 := m.Render(ctx)
	if out1 != out2 {
		t.Fatalf("幂等性失败：两次渲染不一致\n1:\n%s\n2:\n%s", out1, out2)
	}
}

// TestManager_RenderRuntimeOnly 仅 RuntimeInfo 填充时只渲染 RUNTIME 块。
func TestManager_RenderRuntimeOnly(t *testing.T) {
	m := NewManager()
	ctx := Context{
		RuntimeInfo: RuntimeInfo{Date: "2026-09-02", WorkingDir: "/w"},
	}
	out := m.Render(ctx)
	if !strings.Contains(out, "<RUNTIME_INFORMATION>") {
		t.Errorf("应渲染 RUNTIME 块，got:\n%s", out)
	}
	if strings.Contains(out, "<REPOSITORY_INFO>") {
		t.Errorf("RepositoryInfo 空时不应渲染 REPOSITORY 块，got:\n%s", out)
	}
	if strings.Contains(out, "<CONVERSATION_INSTRUCTIONS>") {
		t.Errorf("ConversationInstructions 空时不应渲染该块，got:\n%s", out)
	}
}

// TestRenderMicroagentInfo microagent 触发块。
func TestRenderMicroagentInfo(t *testing.T) {
	m := NewManager()
	out := m.RenderMicroagentInfo([]MicroagentKnowledge{
		{Name: "sqli", Trigger: "sqli", Content: "SQLi 指引"},
		{Name: "xss", Trigger: "xss", Content: "XSS 指引"},
	})
	if !strings.Contains(out, "<EXTRA_INFO>") {
		t.Fatalf("应包含 EXTRA_INFO 块，got:\n%s", out)
	}
	if !strings.Contains(out, "sqli") || !strings.Contains(out, "xss") {
		t.Fatalf("应包含两个 microagent，got:\n%s", out)
	}
	// 块数量应为 2
	if strings.Count(out, "<EXTRA_INFO>") != 2 {
		t.Fatalf("应渲染 2 个 EXTRA_INFO 块，got %d", strings.Count(out, "<EXTRA_INFO>"))
	}
	if m.RenderMicroagentInfo(nil) != "" {
		t.Fatal("空 microagent 应返回空串")
	}
}

// TestRuntimeInfo_IsEmpty IsEmpty 判断。
func TestRuntimeInfo_IsEmpty(t *testing.T) {
	var r RuntimeInfo
	if !r.IsEmpty() {
		t.Fatal("零值应 IsEmpty=true")
	}
	r.Date = "x"
	if r.IsEmpty() {
		t.Fatal("有字段时应 IsEmpty=false")
	}
}

// TestDefaultDate 默认日期串格式正确。
func TestDefaultDate(t *testing.T) {
	d := DefaultDate()
	if d == "" {
		t.Fatal("DefaultDate 不应为空")
	}
	// 应是 RFC3339 格式（含 T 和 Z）
	if !strings.Contains(d, "T") {
		t.Fatalf("DefaultDate 应为 RFC3339，got %q", d)
	}
}
