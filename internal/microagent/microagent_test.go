package microagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 写入临时 microagent 文件的 helper。
func writeMA(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

// TestParseMicroagent_KnowledgeWithTriggers 关键词触发型：triggers 非空 → TypeKnowledge。
func TestParseMicroagent_KnowledgeWithTriggers(t *testing.T) {
	dir := t.TempDir()
	body := "---\nname: sqli-hints\ntriggers:\n  - sql injection\n  - sqli\n---\n遇到 SQL 注入时优先用 union 语法。\n"
	writeMA(t, dir, "sqli-hints.md", body)
	repo, knowledge, err := LoadFromDir(dir)
	if err != nil {
		t.Fatalf("LoadFromDir err: %v", err)
	}
	if len(repo) != 0 {
		t.Fatalf("无 repo microagent，got %d", len(repo))
	}
	if len(knowledge) != 1 {
		t.Fatalf("应加载 1 个 knowledge microagent，got %d", len(knowledge))
	}
	ma := knowledge["sqli-hints"]
	if ma == nil {
		t.Fatal("sqli-hints 未加载")
	}
	if ma.Type != TypeKnowledge {
		t.Fatalf("type 应为 knowledge，got %v", ma.Type)
	}
	if ma.Content != "遇到 SQL 注入时优先用 union 语法。" {
		t.Fatalf("Content 不匹配，got %q", ma.Content)
	}
	if !ma.IsKnowledge() {
		t.Fatal("IsKnowledge 应为 true")
	}
	if ma.IsRepo() {
		t.Fatal("IsRepo 应为 false")
	}
}

// TestParseMicroagent_RepoAlwaysOn 无 triggers → TypeRepo（always-on）。
func TestParseMicroagent_RepoAlwaysOn(t *testing.T) {
	dir := t.TempDir()
	body := "---\nname: repo-conventions\n---\n本项目用 gofmt，禁止 tab。\n"
	writeMA(t, dir, "repo-conventions.md", body)
	repo, knowledge, err := LoadFromDir(dir)
	if err != nil {
		t.Fatalf("LoadFromDir err: %v", err)
	}
	if len(knowledge) != 0 {
		t.Fatalf("无 knowledge，got %d", len(knowledge))
	}
	if len(repo) != 1 {
		t.Fatalf("应加载 1 个 repo，got %d", len(repo))
	}
	ma := repo["repo-conventions"]
	if ma.Type != TypeRepo {
		t.Fatalf("type 应为 repo，got %v", ma.Type)
	}
	if !ma.IsRepo() {
		t.Fatal("IsRepo 应为 true")
	}
}

// TestMatchTrigger_Substring 小写 substring 匹配命中第一个 trigger。
func TestMatchTrigger_Substring(t *testing.T) {
	ma := &Microagent{
		Name:     "x",
		Type:     TypeKnowledge,
		Metadata: Metadata{Triggers: []string{"SQLi", "union select"}},
	}
	if got := ma.MatchTrigger("发现一个 sqli 漏洞"); got != "SQLi" {
		t.Fatalf("应命中 SQLi，got %q", got)
	}
	if got := ma.MatchTrigger("用了 UNION SELECT 绕过"); got != "union select" {
		t.Fatalf("应命中 union select（大小写无关），got %q", got)
	}
	if got := ma.MatchTrigger("无关消息不命中"); got != "" {
		t.Fatalf("无命中应返回空串，got %q", got)
	}
}

// TestMatchTrigger_RepoNoTrigger repo 类型不靠触发，MatchTrigger 恒空。
func TestMatchTrigger_RepoNoTrigger(t *testing.T) {
	ma := &Microagent{Name: "r", Type: TypeRepo, Metadata: Metadata{Triggers: []string{"x"}}}
	if got := ma.MatchTrigger("x"); got != "" {
		t.Fatalf("repo 类型 MatchTrigger 应恒空，got %q", got)
	}
}

// TestRegistry_LayerOverride 三层覆盖：后加载者同名覆盖先加载。
func TestRegistry_LayerOverride(t *testing.T) {
	d1 := t.TempDir()
	writeMA(t, d1, "shared.md", "---\nname: shared\n---\n全局版本\n")
	d2 := t.TempDir()
	writeMA(t, d2, "shared.md", "---\nname: shared\n---\n项目版本\n")

	r := NewRegistry()
	if err := r.LoadLayer(d1); err != nil {
		t.Fatal(err)
	}
	if err := r.LoadLayer(d2); err != nil {
		t.Fatal(err)
	}
	if !r.Has("shared") {
		t.Fatal("shared 应已加载")
	}
	// 验证内容为后加载者
	got := r.RepoContent()
	if !contains(got, "项目版本") || contains(got, "全局版本") {
		t.Fatalf("后加载者应覆盖，got %q", got)
	}
}

// TestRegistry_RetrieveKeywordAndDedup 按关键词检索命中，并按会话去重（已注入过的不重复注入）。
func TestRegistry_RetrieveKeywordAndDedup(t *testing.T) {
	dir := t.TempDir()
	writeMA(t, dir, "sqli.md", "---\nname: sqli\ntriggers: [sqli]\n---\nSQLi 指引\n")
	writeMA(t, dir, "xss.md", "---\nname: xss\ntriggers: [xss]\n---\nXSS 指引\n")
	r := NewRegistry()
	if err := r.LoadLayer(dir); err != nil {
		t.Fatal(err)
	}

	// 首次检索 sqli 命中
	hits := r.Retrieve("conv-1", "检测 sqli 注入点")
	if len(hits) != 1 || hits[0].Name != "sqli" {
		t.Fatalf("应命中 sqli，got %+v", hits)
	}
	// 第二次检索同会话同 sqli：应被去重
	hits2 := r.Retrieve("conv-1", "继续 sqli 利用")
	if len(hits2) != 0 {
		t.Fatalf("同会话同 microagent 应去重，got %+v", hits2)
	}
	// 不同会话不受去重影响
	hits3 := r.Retrieve("conv-2", "另一个 sqli 场景")
	if len(hits3) != 1 || hits3[0].Name != "sqli" {
		t.Fatalf("不同会话应独立命中，got %+v", hits3)
	}
}

// TestRegistry_DisabledFilter 禁用名单过滤。
func TestRegistry_DisabledFilter(t *testing.T) {
	dir := t.TempDir()
	writeMA(t, dir, "sqli.md", "---\nname: sqli\ntriggers: [sqli]\n---\nSQLi\n")
	r := NewRegistry()
	if err := r.LoadLayer(dir); err != nil {
		t.Fatal(err)
	}
	r.SetDisabled([]string{"sqli"})
	hits := r.Retrieve("c", "sqli 检测")
	if len(hits) != 0 {
		t.Fatalf("禁用后不应命中，got %+v", hits)
	}
	if got := r.RepoContent(); got != "" {
		// sqli 是 knowledge 不是 repo，RepoContent 本应空
	}
}

// TestRegistry_ResetSeen 重置会话后可重新注入。
func TestRegistry_ResetSeen(t *testing.T) {
	dir := t.TempDir()
	writeMA(t, dir, "sqli.md", "---\nname: sqli\ntriggers: [sqli]\n---\nSQLi\n")
	r := NewRegistry()
	if err := r.LoadLayer(dir); err != nil {
		t.Fatal(err)
	}
	_ = r.Retrieve("c1", "sqli")
	if len(r.Retrieve("c1", "sqli again")) != 0 {
		t.Fatal("同会话重复应去重")
	}
	r.ResetSeen("c1")
	if len(r.Retrieve("c1", "sqli again")) != 1 {
		t.Fatal("重置后应重新命中")
	}
}

// TestRenderExtraInfo 渲染块结构。
func TestRenderExtraInfo(t *testing.T) {
	out := RenderExtraInfo([]Knowledge{
		{Name: "sqli", Trigger: "sqli", Content: "SQLi 指引"},
	})
	if !contains(out, "<EXTRA_INFO>") || !contains(out, "sqli") || !contains(out, "SQLi 指引") {
		t.Fatalf("渲染应包含块结构与内容，got %q", out)
	}
	if RenderExtraInfo(nil) != "" {
		t.Fatal("空命中应返回空串")
	}
}

// TestRenderRepoInstructions repo 指令块。
func TestRenderRepoInstructions(t *testing.T) {
	out := RenderRepoInstructions("go fmt 约定")
	if !contains(out, "<REPOSITORY_INSTRUCTIONS>") || !contains(out, "go fmt 约定") {
		t.Fatalf("渲染应包含块，got %q", out)
	}
	if RenderRepoInstructions("  ") != "" {
		t.Fatal("空内容应返回空串")
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
