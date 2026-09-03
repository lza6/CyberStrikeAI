package skillpackage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNestedSkillManifestInPackage 验证 K0c 递归后嵌套包语义：
// 嵌套 skill（skills/pentesterflow/deserialize/SKILL.md）的 manifest name
// 只需匹配 SKILL.md 所在目录名（"deserialize"），而非完整相对路径
// "pentesterflow/deserialize"。顶层包行为不变。
func TestNestedSkillManifestInPackage(t *testing.T) {
	nested := &SkillManifest{Name: "deserialize", Description: "嵌套 skill"}

	// 嵌套路径：name 匹配末段目录名即可
	if err := ValidateAgentSkillManifestInPackage(nested, "pentesterflow/deserialize"); err != nil {
		t.Fatalf("嵌套包（name=deserialize, dir=pentesterflow/deserialize）应通过校验，got %v", err)
	}
	if err := ValidateAgentSkillManifestInPackage(nested, `pentesterflow\deserialize`); err != nil {
		t.Fatalf("嵌套包（Windows 分隔符）应通过校验，got %v", err)
	}

	// 末段不匹配仍要拒绝
	if err := ValidateAgentSkillManifestInPackage(nested, "pentesterflow/other"); err == nil {
		t.Fatal("嵌套包末段目录名与 manifest name 不一致应被拒绝")
	}

	// 顶层行为不变：name == 目录名通过，不一致拒绝
	if err := ValidateAgentSkillManifestInPackage(nested, "deserialize"); err != nil {
		t.Fatalf("顶层包 name=deserialize, dir=deserialize 应通过，got %v", err)
	}
	if err := ValidateAgentSkillManifestInPackage(nested, "top"); err == nil {
		t.Fatal("顶层包 name 与目录名不一致应被拒绝")
	}

	// 空 packageDirName：跳过目录名校验（保持原行为）
	if err := ValidateAgentSkillManifestInPackage(nested, "  "); err != nil {
		t.Fatalf("空 packageDirName 应跳过目录名校验，got %v", err)
	}
}

// TestParseSkillMD_ListStyleAllowedTools 验证 allowed-tools 为 YAML 列表形式的
// SKILL.md（pentesterflow/* 存量 11 个包的实际写法）能被解析，不因类型不匹配
// 报错导致 skill 被静默丢弃。官方规范为字符串形式，两种写法均需容忍。
func TestParseSkillMD_ListStyleAllowedTools(t *testing.T) {
	raw := []byte("---\nname: deserialize\ndescription: d\nallowed-tools:\n  - http\n  - shell\n  - file_write\n---\n# D\nbody\n")
	m, body, err := ParseSkillMD(raw)
	if err != nil {
		t.Fatalf("列表形式 allowed-tools 应能解析，got %v", err)
	}
	if m.Name != "deserialize" {
		t.Fatalf("name 不符: %q", m.Name)
	}
	if m.AllowedTools != "http, shell, file_write" {
		t.Fatalf("allowed-tools 应归一化为逗号串，got %q", m.AllowedTools)
	}
	if !strings.HasPrefix(body, "# D") {
		t.Fatalf("body 不符: %q", body)
	}

	// 字符串形式行为不变
	rawStr := []byte("---\nname: a\ndescription: d\nallowed-tools: exec, grep\n---\nb\n")
	m2, _, err := ParseSkillMD(rawStr)
	if err != nil {
		t.Fatalf("字符串形式 allowed-tools 应能解析，got %v", err)
	}
	if m2.AllowedTools != "exec, grep" {
		t.Fatalf("字符串形式 allowed-tools 不符: %q", m2.AllowedTools)
	}

	// 真正损坏的 YAML 仍应报错
	bad := []byte("---\nname: a\n  bad: : : :\n---\nb\n")
	if _, _, err := ParseSkillMD(bad); err == nil {
		t.Fatal("损坏 YAML 应报错")
	}
}

// TestListSkillSummariesNestedVisible 验证 admin API 列表对嵌套包可见（P0 场景回归）：
// ListSkillDirNames 返回 3（含 2 个嵌套），ListSkillSummaries 不得静默丢弃嵌套 skill。
func TestListSkillSummariesNestedVisible(t *testing.T) {
	root := t.TempDir()
	writeSkill := func(relDir, name, desc string) {
		t.Helper()
		dir := filepath.Join(root, filepath.FromSlash(relDir))
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		content := "---\nname: " + name + "\ndescription: " + desc + "\n---\n\n# " + name + "\n"
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	writeSkill("top", "top", "顶层")
	writeSkill("pentesterflow/deserialize", "deserialize", "嵌套1")
	writeSkill("pentesterflow/reverse", "reverse", "嵌套2")

	names, err := ListSkillDirNames(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 3 {
		t.Fatalf("ListSkillDirNames 期望 3，实际 %d: %v", len(names), names)
	}

	summaries, err := ListSkillSummaries(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != len(names) {
		t.Fatalf("ListSkillSummaries(%d) 不得静默丢 ListSkillDirNames(%d) 的项：admin API 可见性回归", len(summaries), len(names))
	}
	got := map[string]bool{}
	for _, s := range summaries {
		got[s.DirName] = true
		if s.Name != filepath.Base(s.DirName) {
			t.Fatalf("summary name %q 应匹配末段目录名 %q", s.Name, s.DirName)
		}
	}
	for _, want := range []string{"top", "pentesterflow/deserialize", "pentesterflow/reverse"} {
		if !got[want] {
			t.Fatalf("嵌套/顶层 skill %q 不在 ListSkillSummaries 结果中，got %v", want, got)
		}
	}
	if !got["pentesterflow/deserialize"] || !strings.Contains("pentesterflow/deserialize", "/") {
		t.Fatal("嵌套 skill 未被 ListSkillSummaries 返回")
	}
}
