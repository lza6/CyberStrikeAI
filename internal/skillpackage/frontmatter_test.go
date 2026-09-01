package skillpackage

import (
	"strings"
	"testing"
)

// TestExtractSkillMDFrontMatterYAML_HappyPath 验证标准 front matter + body 正确切分。
func TestExtractSkillMDFrontMatterYAML_HappyPath(t *testing.T) {
	raw := []byte("---\nname: alpha\ndescription: 测试\n---\n# Alpha\n正文内容\n")
	fm, body, err := ExtractSkillMDFrontMatterYAML(raw)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if !strings.Contains(fm, "name: alpha") {
		t.Fatalf("front matter 缺 name: %q", fm)
	}
	if !strings.HasPrefix(body, "# Alpha") {
		t.Fatalf("body 应以 # Alpha 开头: %q", body)
	}
}

// TestExtractSkillMDFrontMatterYAML_NoLeadingFence 验证缺少首行 --- 返回错误。
func TestExtractSkillMDFrontMatterYAML_NoLeadingFence(t *testing.T) {
	raw := []byte("name: alpha\ndescription: 测试\n")
	_, _, err := ExtractSkillMDFrontMatterYAML(raw)
	if err == nil {
		t.Fatal("缺首行 --- 应报错")
	}
	if !strings.Contains(err.Error(), "must start with YAML front matter") {
		t.Fatalf("错误信息不符: %v", err)
	}
}

// TestExtractSkillMDFrontMatterYAML_MissingClosingFence 验证缺少闭合 --- 返回错误。
func TestExtractSkillMDFrontMatterYAML_MissingClosingFence(t *testing.T) {
	raw := []byte("---\nname: alpha\n描述\nbody never ends")
	_, _, err := ExtractSkillMDFrontMatterYAML(raw)
	if err == nil {
		t.Fatal("缺闭合 --- 应报错")
	}
	if !strings.Contains(err.Error(), "must end with a line containing only ---") {
		t.Fatalf("错误信息不符: %v", err)
	}
}

// TestExtractSkillMDFrontMatterYAML_EmptyContent 验证空内容返回错误。
func TestExtractSkillMDFrontMatterYAML_EmptyContent(t *testing.T) {
	if _, _, err := ExtractSkillMDFrontMatterYAML([]byte("")); err == nil {
		t.Fatal("空内容应报错")
	}
}

// TestParseSkillMD_PopulatesManifest 验证 ParseSkillMD 正确解析 YAML 字段到 SkillManifest。
func TestParseSkillMD_PopulatesManifest(t *testing.T) {
	raw := []byte("---\nname: alpha\ndescription: 测试技能\nlicense: MIT\nallowed-tools: exec, grep\n---\n# Alpha\n正文\n")
	m, body, err := ParseSkillMD(raw)
	if err != nil {
		t.Fatalf("ParseSkillMD: %v", err)
	}
	if m.Name != "alpha" {
		t.Fatalf("name 不符: %q", m.Name)
	}
	if m.Description != "测试技能" {
		t.Fatalf("description 不符: %q", m.Description)
	}
	if m.License != "MIT" {
		t.Fatalf("license 不符: %q", m.License)
	}
	if m.AllowedTools != "exec, grep" {
		t.Fatalf("allowed-tools 不符: %q", m.AllowedTools)
	}
	if !strings.HasPrefix(body, "# Alpha") {
		t.Fatalf("body 不符: %q", body)
	}
}

// TestParseSkillMD_BadYAML 验证 front matter 为非 YAML 报错。
func TestParseSkillMD_BadYAML(t *testing.T) {
	// front matter 内含非法 YAML 缩进应触发 unmarshal 错误
	raw := []byte("---\nname: alpha\n  bad: : : :\n---\nbody\n")
	if _, _, err := ParseSkillMD(raw); err == nil {
		t.Fatal("非法 YAML 应报错")
	}
}

// TestBuildSkillMD_RoundTrip 验证 BuildSkillMD 输出可被 ParseSkillMD 解析回等价 manifest。
func TestBuildSkillMD_RoundTrip(t *testing.T) {
	src := &SkillManifest{
		Name:         "alpha",
		Description:  "测试技能",
		License:      "MIT",
		AllowedTools: "exec, grep",
	}
	body := "# Alpha\n\n使用 exec 工具。\n"
	b, err := BuildSkillMD(src, body)
	if err != nil {
		t.Fatalf("BuildSkillMD: %v", err)
	}
	out := string(b)
	if !strings.HasPrefix(out, "---\n") {
		t.Fatalf("应以 --- 开头: %q", out)
	}
	// front matter 必须含 name
	if !strings.Contains(out, "name: alpha") {
		t.Fatalf("输出缺 name: %q", out)
	}
	// body 必须保留
	if !strings.Contains(out, "使用 exec 工具") {
		t.Fatalf("body 未保留: %q", out)
	}
	// 反解一次
	m, _, err := ParseSkillMD(b)
	if err != nil {
		t.Fatalf("ParseSkillMD(BuildSkillMD(...)): %v", err)
	}
	if m.Name != src.Name || m.License != src.License || m.AllowedTools != src.AllowedTools {
		t.Fatalf("往返不一致: %+v", m)
	}
}

// TestBuildSkillMD_NilManifest 验证 nil manifest 返回错误。
func TestBuildSkillMD_NilManifest(t *testing.T) {
	if _, err := BuildSkillMD(nil, "body"); err == nil {
		t.Fatal("nil manifest 应报错")
	}
}

// TestBuildSkillMD_TrimsWhitespaceInFields 验证字段首尾空白被 TrimSpace。
func TestBuildSkillMD_TrimsWhitespaceInFields(t *testing.T) {
	m := &SkillManifest{Name: "  alpha  ", Description: "  desc  "}
	b, err := BuildSkillMD(m, "  body  ")
	if err != nil {
		t.Fatalf("BuildSkillMD: %v", err)
	}
	out := string(b)
	if !strings.Contains(out, "name: alpha") {
		t.Fatalf("name 未被 TrimSpace: %q", out)
	}
	if !strings.Contains(out, "description: desc") {
		t.Fatalf("description 未被 TrimSpace: %q", out)
	}
}

// TestManifestTags 验证 manifestTags 从 metadata.tags 提取标签列表。
func TestManifestTags(t *testing.T) {
	cases := []struct {
		name string
		m    *SkillManifest
		want []string
	}{
		{"nil metadata", &SkillManifest{}, nil},
		{"no tags", &SkillManifest{Metadata: map[string]any{"version": "1"}}, nil},
		{"any slice", &SkillManifest{Metadata: map[string]any{"tags": []any{"xss", "sqli"}}}, []string{"xss", "sqli"}},
		{"string slice", &SkillManifest{Metadata: map[string]any{"tags": []string{"a", "b"}}}, []string{"a", "b"}},
		{"filters empty", &SkillManifest{Metadata: map[string]any{"tags": []any{"", "x"}}}, []string{"x"}},
	}
	for _, tc := range cases {
		got := manifestTags(tc.m)
		if len(got) != len(tc.want) {
			t.Fatalf("%s: got %v want %v", tc.name, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("%s[%d]: got %q want %q", tc.name, i, got[i], tc.want[i])
			}
		}
	}
}

// TestVersionFromMetadata 验证 versionFromMetadata 提取 version 字符串。
func TestVersionFromMetadata(t *testing.T) {
	if v := versionFromMetadata(nil); v != "" {
		t.Fatalf("nil 应返回空, got %q", v)
	}
	if v := versionFromMetadata(&SkillManifest{Metadata: map[string]any{}}); v != "" {
		t.Fatalf("空 metadata 应返回空, got %q", v)
	}
	if v := versionFromMetadata(&SkillManifest{Metadata: map[string]any{"version": "1.2.3"}}); v != "1.2.3" {
		t.Fatalf("version 不符: %q", v)
	}
	if v := versionFromMetadata(&SkillManifest{Metadata: map[string]any{"version": "  1.0  "}}); v != "1.0" {
		t.Fatalf("version 未 TrimSpace: %q", v)
	}
	// 非 string 类型应返回空
	if v := versionFromMetadata(&SkillManifest{Metadata: map[string]any{"version": 123}}); v != "" {
		t.Fatalf("非 string version 应返回空, got %q", v)
	}
}
