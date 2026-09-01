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

使用 ` + "`exec`" + ` 工具执行，再用 ` + "`grep`" + ` 过滤。
也调 ` + "`nmap-ghost`" + ` 这个不存在的工具。
`)
	// beta 只引用真实工具
	writeSkillContent(t, dir, "beta", `---
name: beta
tools:
  - exec
  - list_files
---
# Beta
用 ` + "`exec`" + ` 跑命令。
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
		{"a", false},              // 太短
		{"the", false},            // 停用词
		{"with", false},           // 停用词
		{"path/to/x", false},      // 含斜杠
		{"foo.bar", false},        // 含点
		{"foo bar", false},        // 含空格
		{"abcdefghijklmnopqrstuvwxyz1234567890abcdef", false}, // 太长
	}
	for _, tc := range cases {
		if got := isLikelyToolName(tc.name); got != tc.want {
			t.Errorf("isLikelyToolName(%q)=%v want %v", tc.name, got, tc.want)
		}
	}
}
