package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadMarkdownAgentsDir_SkipsUnderscoreFiles 验证 `_` 前缀 .md（共享片段）不作为 agent 加载：
// 不出现在 SubAgents、FileEntries，也不作为 orchestrator。
func TestLoadMarkdownAgentsDir_SkipsUnderscoreFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("_shared.md", "---\nid: _shared\nname: Operator Charter 共享段\ndescription: 共享契约\n---\n\n# Charter\n授权立场……\n")
	write("worker.md", "---\nid: worker\nname: Worker\ndescription: W\n---\n\nDo work\n")

	load, err := LoadMarkdownAgentsDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range load.SubAgents {
		if strings.HasPrefix(s.ID, "_") {
			t.Fatalf("SubAgents 不应含 `_` 前缀幽灵 agent，got %+v", s)
		}
	}
	if len(load.SubAgents) != 1 || load.SubAgents[0].ID != "worker" {
		t.Fatalf("subs 应只剩 worker，got %+v", load.SubAgents)
	}
	for _, fe := range load.FileEntries {
		if strings.HasPrefix(fe.Filename, "_") {
			t.Fatalf("FileEntries 不应含共享片段 %q", fe.Filename)
		}
	}
	if load.Orchestrator != nil {
		t.Fatalf("_shared.md 不应被解析为主代理: %+v", load.Orchestrator)
	}
}

// TestExpandSharedIncludes 验证 {{include:_shared}} 占位符被 _shared.md 正文替换。
func TestExpandSharedIncludes(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("_shared.md", "---\nid: _shared\nname: shared\ndescription: d\n---\n\nCHARTER-CONTENT-42\n")
	write("agent1.md", "---\nid: agent1\nname: A1\ndescription: d\n---\n\nbefore\n\n{{include:_shared}}\n\nafter\n")
	write("agent2.md", "---\nid: agent2\nname: A2\ndescription: d\n---\n\nno include here\n")
	// 未知片段保持原样
	write("agent3.md", "---\nid: agent3\nname: A3\ndescription: d\n---\n\n{{include:unknown}}\n")

	load, err := LoadMarkdownAgentsDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, s := range load.SubAgents {
		got[s.ID] = s.Instruction
	}
	a1, ok := got["agent1"]
	if !ok {
		t.Fatalf("agent1 未加载: %+v", got)
	}
	if !strings.Contains(a1, "CHARTER-CONTENT-42") {
		t.Fatalf("{{include:_shared}} 未替换为 _shared 正文，instruction: %q", a1)
	}
	if strings.Contains(a1, "{{include:") {
		t.Fatalf("替换后仍残留占位符: %q", a1)
	}
	if !strings.Contains(a1, "before") || !strings.Contains(a1, "after") {
		t.Fatalf("include 前后正文丢失: %q", a1)
	}
	if a2 := got["agent2"]; strings.Contains(a2, "CHARTER") {
		t.Fatalf("无占位符的 agent 不应被注入共享段: %q", a2)
	}
	if a3 := got["agent3"]; !strings.Contains(a3, "{{include:unknown}}") {
		t.Fatalf("未知片段占位符应保持原样: %q", a3)
	}
}

// TestParseMarkdownSubAgent_KeepsPlaceholder 单文件解析路径（admin Get API 用）
// 不做 include 展开——占位符原样保留，展开只发生在目录加载链。
func TestParseMarkdownSubAgent_KeepsPlaceholder(t *testing.T) {
	raw := "---\nid: w\nname: W\ndescription: d\n---\n\n{{include:_shared}}\n"
	sub, err := ParseMarkdownSubAgent("w.md", raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sub.Instruction, "{{include:_shared}}") {
		t.Fatalf("单文件解析不应展开 include，got %q", sub.Instruction)
	}
}
