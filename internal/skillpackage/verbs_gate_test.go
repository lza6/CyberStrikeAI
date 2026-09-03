package skillpackage

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSkillContent(t *testing.T, dir, name, content string) {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestScanToolReferencesFindsGhosts(t *testing.T) {
	dir := t.TempDir()
	// alpha 引用真实工具 exec/grep + 幽灵 nmap-ghost
	writeSkillContent(t, dir, "alpha", `---
name: alpha
description: 测试
tools: [exec, grep, nmap-ghost]
---
# Alpha

使用 `+"`exec`"+` 工具执行，再用 `+"`grep`"+` 过滤。
也调 `+"`nmap-ghost`"+` 这个不存在的工具。
`)
	// beta 只引用真实工具
	writeSkillContent(t, dir, "beta", `---
name: beta
tools:
  - exec
  - list_files
---
# Beta
用 `+"`exec`"+` 跑命令。
`)
	realTools := map[string]bool{"exec": true, "grep": true, "list_files": true}
	violations, err := ScanToolReferences(dir, realTools)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 {
		t.Fatalf("期望 1 个幽灵违规（nmap-ghost），got %d: %+v", len(violations), violations)
	}
	if violations[0].Referenced != "nmap-ghost" || violations[0].Skill != "alpha" {
		t.Fatalf("违规内容不符: %+v", violations[0])
	}
}

func TestScanToolReferencesEmpty(t *testing.T) {
	dir := t.TempDir()
	// 无 realTools 基线
	v, err := ScanToolReferences(dir, nil)
	if err != nil || len(v) != 0 {
		t.Fatalf("无基线应返回空，got %v %v", v, err)
	}
	// 空目录
	v, err = ScanToolReferences(dir, map[string]bool{"exec": true})
	if err != nil || len(v) != 0 {
		t.Fatalf("空目录应返回空")
	}
}

func TestIsLikelyToolName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"exec", true},
		{"nmap-sV", true},
		{"sqlmap", true},
		{"a", false},         // 太短
		{"the", false},       // 停用词
		{"with", false},      // 停用词
		{"path/to/x", false}, // 含斜杠
		{"foo.bar", false},   // 含点
		{"foo bar", false},   // 含空格
		{"abcdefghijklmnopqrstuvwxyz1234567890abcdef", false}, // 太长
		// P1-6 停词表扩充：高频技术字段名不再报幽灵
		{"name", false},
		{"status", false},
		{"body", false},
		{"confidence", false},
		{"confirmed", false},
		{"base64_encode", false},
		{"sha256_hash", false},
		{"onerror", false},
		{"true", false},
		{"null", false},
	}
	for _, tc := range cases {
		if got := isLikelyToolName(tc.name); got != tc.want {
			t.Errorf("isLikelyToolName(%q)=%v want %v", tc.name, got, tc.want)
		}
	}
}

// TestRealToolsHint P1-6：真实工具前缀/子串匹配——真实工具的缩写引用不算幽灵。
func TestRealToolsHint(t *testing.T) {
	h := newRealToolsHint(map[string]bool{"nmap-sub": true, "web-search": true})
	if !h.isRealToolRef("nmap") {
		t.Error("nmap 是 nmap-sub 的前缀，应判定为真实工具引用")
	}
	if !h.isRealToolRef("nmap-sub") {
		t.Error("精确匹配应命中")
	}
	if !h.isRealToolRef("search") {
		t.Error("search 是 web-search 的子串，应判定为真实工具引用")
	}
	if h.isRealToolRef("ghost-tool") {
		t.Error("ghost-tool 与真实工具无前缀/子串关系，不应命中")
	}
	if h.isRealToolRef("") {
		t.Error("空串不应命中")
	}
}

// TestMarkdownAgentNames P1-6：agents 目录的 Markdown 子代理名白名单。
func TestMarkdownAgentNames(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"vulnerability-triage.md", "_shared.md", "README.md", ".hidden.md", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	names := markdownAgentNames(dir)
	if !names["vulnerability-triage"] {
		t.Errorf("vulnerability-triage.md 应被收录，got %v", names)
	}
	for _, banned := range []string{"_shared", "README", "hidden", "notes"} {
		if names[banned] {
			t.Errorf("%s 不应被收录（共享片段/README/非 md），got %v", banned, names)
		}
	}
	// 目录不存在返回空 map（不 panic）
	if got := markdownAgentNames(filepath.Join(dir, "not-exist")); len(got) != 0 {
		t.Errorf("目录不存在应返回空，got %v", got)
	}
}

// TestScanToolReferencesBodyHintFiltering P1-6：正文反引号的真实工具缩写引用与
// skill 互引/子代理引用不报幽灵；frontmatter 真幽灵仍严格检出。
func TestScanToolReferencesBodyHintFiltering(t *testing.T) {
	dir := t.TempDir()
	// sibling agents 目录（与 skillsDir 同级，模拟 <config>/agents）
	if err := os.MkdirAll(filepath.Join(dir, "agents"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agents", "recon-agent.md"), []byte("id: recon-agent"), 0644); err != nil {
		t.Fatal(err)
	}
	// 嵌套 skill 目录（skill 互引白名单来源）
	skillsDir := filepath.Join(dir, "skills")
	if err := os.MkdirAll(filepath.Join(skillsDir, "ctf-web"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "ctf-web", "SKILL.md"), []byte("---\nname: ctf-web\ndescription: x\n---\n# ctf-web\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// alpha 正文引用：真实工具缩写（nmap）、skill 互引（ctf-web）、子代理名（recon-agent）、
	// 真幽灵 ghost-tool；frontmatter 引用真幽灵 fm-ghost（真契约，hint 不豁免）
	writeSkillContent(t, skillsDir, "alpha", `---
name: alpha
description: 测试
tools: [exec, fm-ghost]
---
# Alpha

用 `+"`nmap`"+` 扫描（nmap-sub 的缩写），交给 `+"`ctf-web`"+` 与 `+"`recon-agent`"+` 处理，
最后调 `+"`ghost-tool`"+`。
`)
	realTools := map[string]bool{"exec": true, "nmap-sub": true}
	violations, err := ScanToolReferences(skillsDir, realTools)
	if err != nil {
		t.Fatal(err)
	}
	// ctf-web 是 skill 目录（skills/ctf-web/）→ 白名单；nmap 前缀命中 nmap-sub → hint；
	// recon-agent 是 agents 目录子代理 → 白名单。剩 ghost-tool（正文）+ fm-ghost（frontmatter）。
	got := map[string]string{}
	for _, v := range violations {
		got[v.Referenced] = v.Hint
	}
	if len(violations) != 2 {
		t.Fatalf("期望 2 个违规（ghost-tool 正文 + fm-ghost frontmatter），got %d: %+v", len(violations), violations)
	}
	if got["ghost-tool"] == "" || got["fm-ghost"] == "" {
		t.Fatalf("违规内容不符: %+v", violations)
	}
	if got["fm-ghost"] != "frontmatter tools 字段" {
		t.Fatalf("fm-ghost 应来自 frontmatter（真契约严格校验），hint=%q", got["fm-ghost"])
	}
	// 白名单词不得出现
	for _, banned := range []string{"nmap", "ctf-web", "recon-agent"} {
		if _, ok := got[banned]; ok {
			t.Errorf("%s 应被过滤（hint/目录/子代理白名单），但被报为幽灵", banned)
		}
	}
}

// TestScanToolReferencesRecursiveSubdir K0a：验证递归扫描子目录。
// skills/security/alpha/SKILL.md 与顶层 skills/gamma/SKILL.md 都应被扫到。
func TestScanToolReferencesRecursiveSubdir(t *testing.T) {
	dir := t.TempDir()
	// 嵌套子目录下的 skill
	nestedDir := filepath.Join(dir, "security", "alpha")
	if err := os.MkdirAll(nestedDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nestedDir, "SKILL.md"), []byte(`---
name: alpha
tools: [exec, ghost-recursive]
---
# Alpha nested

用 `+"`exec`"+` 与 `+"`ghost-recursive`"+`。
`), 0644); err != nil {
		t.Fatal(err)
	}
	// 顶层 skill（向后兼容）
	writeSkillContent(t, dir, "gamma", `---
name: gamma
tools: [exec]
---
# Gamma top

用 `+"`exec`"+`。
`)
	realTools := map[string]bool{"exec": true}
	violations, err := ScanToolReferences(dir, realTools)
	if err != nil {
		t.Fatalf("递归扫描失败: %v", err)
	}
	// 应检出 ghost-recursive（在 security/alpha 下）
	if len(violations) != 1 {
		t.Fatalf("期望 1 个幽灵违规（ghost-recursive），got %d: %+v", len(violations), violations)
	}
	if violations[0].Referenced != "ghost-recursive" {
		t.Fatalf("违规工具名不符: %+v", violations[0])
	}
	// 包名应为 "security/alpha"（相对路径，与 lock.go 对齐）
	if violations[0].Skill != "security/alpha" {
		t.Fatalf("嵌套 skill 包名应为 security/alpha，got %q", violations[0].Skill)
	}
	// File 字段应为相对路径 security/alpha/SKILL.md
	if violations[0].File != "security/alpha/SKILL.md" {
		t.Fatalf("File 字段应为 security/alpha/SKILL.md，got %q", violations[0].File)
	}
}

// TestScanToolReferencesSkipsHiddenDir K0a：验证跳过隐藏目录（.eino 等）。
func TestScanToolReferencesSkipsHiddenDir(t *testing.T) {
	dir := t.TempDir()
	// .eino/plantask 下的 SKILL.md 不应被扫描
	hiddenDir := filepath.Join(dir, ".eino", "plantask", "some-skill")
	if err := os.MkdirAll(hiddenDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hiddenDir, "SKILL.md"), []byte(`---
name: hidden
tools: [ghost-in-hidden]
---
# Hidden

用 `+"`ghost-in-hidden`"+`。
`), 0644); err != nil {
		t.Fatal(err)
	}
	// 正常 skill
	writeSkillContent(t, dir, "visible", `---
name: visible
tools: [exec]
---
# Visible
`)
	realTools := map[string]bool{"exec": true}
	violations, err := ScanToolReferences(dir, realTools)
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	// 不应检出 ghost-in-hidden（在 .eino 隐藏目录下被跳过）
	for _, v := range violations {
		if v.Referenced == "ghost-in-hidden" {
			t.Fatalf("隐藏目录 .eino 下的 SKILL.md 不应被扫描，但检出: %+v", v)
		}
	}
}
