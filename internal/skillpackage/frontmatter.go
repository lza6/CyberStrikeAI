package skillpackage

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// ExtractSkillMDFrontMatterYAML returns the YAML source inside the first --- ... --- block and the markdown body.
func ExtractSkillMDFrontMatterYAML(raw []byte) (fmYAML string, body string, err error) {
	text := strings.TrimPrefix(string(raw), "\ufeff")
	if strings.TrimSpace(text) == "" {
		return "", "", fmt.Errorf("SKILL.md is empty")
	}
	lines := strings.Split(text, "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "---" {
		return "", "", fmt.Errorf("SKILL.md must start with YAML front matter (---) per Agent Skills standard")
	}
	var fmLines []string
	i := 1
	for i < len(lines) {
		if strings.TrimSpace(lines[i]) == "---" {
			break
		}
		fmLines = append(fmLines, lines[i])
		i++
	}
	if i >= len(lines) {
		return "", "", fmt.Errorf("SKILL.md: front matter must end with a line containing only ---")
	}
	body = strings.Join(lines[i+1:], "\n")
	body = strings.TrimSpace(body)
	fmYAML = strings.Join(fmLines, "\n")
	return fmYAML, body, nil
}

// ParseSkillMD parses SKILL.md YAML head + body.
//
// 兼容性说明：`allowed-tools` 官方规范为字符串形式（如 "Bash(belt *)"，
// https://agentskills.io/specification.md），但 pentesterflow/* 等 11 个存量
// skill 使用 YAML 列表形式（- http / - shell）。SkillManifest.AllowedTools
// 声明为 string，直接 Unmarshal 列表会报 "cannot unmarshal !!seq into string"
// 导致整个 skill 被静默丢弃。这里先容忍任意 front matter 再二次解析宽松
// manifest：列表形式的 allowed-tools 归一化为逗号串；未声明键（如 routing）
// 忽略。解析失败（真正损坏的 YAML）仍返回错误。
func ParseSkillMD(raw []byte) (*SkillManifest, string, error) {
	fmYAML, body, err := ExtractSkillMDFrontMatterYAML(raw)
	if err != nil {
		return nil, "", err
	}
	var m SkillManifest
	if err := yaml.Unmarshal([]byte(fmYAML), &m); err != nil {
		// 二次宽松解析：把 allowed-tools 列表归一化为逗号串后重试。
		var loose map[string]yaml.Node
		if lerr := yaml.Unmarshal([]byte(fmYAML), &loose); lerr == nil {
			if norm, ok := loose["allowed-tools"]; ok && norm.Kind == yaml.SequenceNode {
				var items []string
				for _, it := range norm.Content {
					var s string
					if err := it.Decode(&s); err == nil && strings.TrimSpace(s) != "" {
						items = append(items, strings.TrimSpace(s))
					}
				}
				loose["allowed-tools"] = yaml.Node{Kind: yaml.ScalarNode, Value: strings.Join(items, ", ")}
				if reb, merr := yaml.Marshal(loose); merr == nil {
					if err2 := yaml.Unmarshal(reb, &m); err2 == nil {
						return &m, body, nil
					}
				}
			}
		}
		return nil, "", fmt.Errorf("SKILL.md front matter: %w", err)
	}
	return &m, body, nil
}

type skillFrontMatterExport struct {
	Name          string         `yaml:"name"`
	Description   string         `yaml:"description"`
	License       string         `yaml:"license,omitempty"`
	Compatibility string         `yaml:"compatibility,omitempty"`
	Metadata      map[string]any `yaml:"metadata,omitempty"`
	AllowedTools  string         `yaml:"allowed-tools,omitempty"`
}

// BuildSkillMD serializes SKILL.md per agentskills.io.
func BuildSkillMD(m *SkillManifest, body string) ([]byte, error) {
	if m == nil {
		return nil, fmt.Errorf("nil manifest")
	}
	fm := skillFrontMatterExport{
		Name:          strings.TrimSpace(m.Name),
		Description:   strings.TrimSpace(m.Description),
		License:       strings.TrimSpace(m.License),
		Compatibility: strings.TrimSpace(m.Compatibility),
		AllowedTools:  strings.TrimSpace(m.AllowedTools),
	}
	if len(m.Metadata) > 0 {
		fm.Metadata = m.Metadata
	}
	head, err := yaml.Marshal(&fm)
	if err != nil {
		return nil, err
	}
	s := strings.TrimSpace(string(head))
	out := "---\n" + s + "\n---\n\n" + strings.TrimSpace(body) + "\n"
	return []byte(out), nil
}

func manifestTags(m *SkillManifest) []string {
	if m == nil || m.Metadata == nil {
		return nil
	}
	var out []string
	if raw, ok := m.Metadata["tags"]; ok {
		switch v := raw.(type) {
		case []any:
			for _, x := range v {
				if s, ok := x.(string); ok && s != "" {
					out = append(out, s)
				}
			}
		case []string:
			out = append(out, v...)
		}
	}
	return out
}

func versionFromMetadata(m *SkillManifest) string {
	if m == nil || m.Metadata == nil {
		return ""
	}
	if v, ok := m.Metadata["version"]; ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}
