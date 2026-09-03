// Package evals 提供 skill 触发路由质量的三层评测（agent-skills 思想移植）：
// Tier 1 结构校验（frontmatter/命名/必需段）+ Tier 2 触发路由碰撞检测（TF-IDF 相似度）。
// Tier 3 行为评测：离线确定性子集见 route_behavior.go（词面路由模拟，零成本回归锚）；
// 真实 LLM 在环评测需付费 API（付费红线），不做伪造。
package evals

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// StructureViolation Tier 1 结构违规。
type StructureViolation struct {
	Skill string `json:"skill"`
	Issue string `json:"issue"`
	Line  int    `json:"line"`
}

// CollisionViolation Tier 2 触发描述碰撞。
type CollisionViolation struct {
	SkillA     string  `json:"skill_a"`
	SkillB     string  `json:"skill_b"`
	Similarity float64 `json:"similarity"`
}

// isSkillContainer 目录是否为非 skill 容器（跳过用）：
// . 开头内部目录，或含子目录 SKILL.md 的聚合父目录（pentesterflow/vulnclaw-specialized）。
func isSkillContainer(dir, name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	entries, err := os.ReadDir(filepath.Join(dir, name))
	if err != nil {
		return false
	}
	selfHasSkill := false
	childHasSkill := false
	for _, e := range entries {
		if e.IsDir() {
			if _, err := os.Stat(filepath.Join(dir, name, e.Name(), "SKILL.md")); err == nil {
				childHasSkill = true
			}
		} else if e.Name() == "SKILL.md" {
			selfHasSkill = true
		}
	}
	return !selfHasSkill && childHasSkill
}

// ValidateStructure Tier 1：扫描每个 SKILL.md 的结构完整性。
// 检查：frontmatter 存在、name/description 非空、正文非空。
func ValidateStructure(skillsDir string) ([]StructureViolation, error) {
	var violations []StructureViolation
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() || isSkillContainer(skillsDir, e.Name()) {
			continue
		}
		skillFile := filepath.Join(skillsDir, e.Name(), "SKILL.md")
		data, err := os.ReadFile(skillFile)
		if err != nil {
			violations = append(violations, StructureViolation{Skill: e.Name(), Issue: "SKILL.md 不存在"})
			continue
		}
		content := string(data)
		lines := strings.Split(content, "\n")

		// frontmatter 检查
		if !strings.HasPrefix(content, "---") {
			violations = append(violations, StructureViolation{Skill: e.Name(), Issue: "缺 YAML frontmatter（须以 --- 开头）", Line: 1})
			continue
		}
		fmEnd := -1
		for i := 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "---" {
				fmEnd = i
				break
			}
		}
		if fmEnd < 0 {
			violations = append(violations, StructureViolation{Skill: e.Name(), Issue: "frontmatter 未闭合", Line: 1})
			continue
		}
		fm := strings.Join(lines[1:fmEnd], "\n")
		if !strings.Contains(fm, "name:") {
			violations = append(violations, StructureViolation{Skill: e.Name(), Issue: "frontmatter 缺 name 字段", Line: 2})
		}
		if !strings.Contains(fm, "description:") {
			violations = append(violations, StructureViolation{Skill: e.Name(), Issue: "frontmatter 缺 description 字段", Line: 2})
		}
		// 正文非空
		body := strings.TrimSpace(strings.Join(lines[fmEnd+1:], "\n"))
		if body == "" {
			violations = append(violations, StructureViolation{Skill: e.Name(), Issue: "正文为空", Line: fmEnd + 1})
		}
	}
	sort.Slice(violations, func(i, j int) bool { return violations[i].Skill < violations[j].Skill })
	return violations, nil
}

// DetectTriggerCollisions Tier 2：description 相似度碰撞检测。
// 简化 TF-IDF：分词后余弦相似度 > threshold 报碰撞。
func DetectTriggerCollisions(skillsDir string, threshold float64) ([]CollisionViolation, error) {
	type skillDesc struct{ name, desc string }
	var descs []skillDesc
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() || isSkillContainer(skillsDir, e.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(skillsDir, e.Name(), "SKILL.md"))
		if err != nil {
			continue
		}
		content := string(data)
		if !strings.HasPrefix(content, "---") {
			continue
		}
		lines := strings.Split(content, "\n")
		fmEnd := -1
		for i := 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "---" {
				fmEnd = i
				break
			}
		}
		if fmEnd < 0 {
			continue
		}
		desc := ""
		for _, line := range lines[1:fmEnd] {
			if strings.HasPrefix(strings.TrimSpace(line), "description:") {
				desc = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "description:"))
				break
			}
		}
		descs = append(descs, skillDesc{name: e.Name(), desc: desc})
	}

	var violations []CollisionViolation
	for i := 0; i < len(descs); i++ {
		for j := i + 1; j < len(descs); j++ {
			sim := cosineSimilarity(descs[i].desc, descs[j].desc)
			if sim > threshold {
				violations = append(violations, CollisionViolation{
					SkillA: descs[i].name, SkillB: descs[j].name, Similarity: math.Round(sim*100) / 100,
				})
			}
		}
	}
	sort.Slice(violations, func(i, j int) bool { return violations[i].Similarity > violations[j].Similarity })
	return violations, nil
}

// cosineSimilarity 分词后余弦相似度。
func cosineSimilarity(a, b string) float64 {
	ta := tokenize(a)
	tb := tokenize(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	fa := termFreq(ta)
	fb := termFreq(tb)
	var dot, na, nb float64
	for k, v := range fa {
		na += v * v
		if w, ok := fb[k]; ok {
			dot += v * w
		}
	}
	for _, v := range fb {
		nb += v * v
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// tokenize 简易分词：小写化 + 按非字母数字切分（中文字符按单字）。
func tokenize(s string) []string {
	s = strings.ToLower(s)
	var out []string
	var cur strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			cur.WriteRune(r)
		} else if r >= 0x4e00 && r <= 0x9fff {
			// 中文：前一个词 flush，单字成词
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
			out = append(out, string(r))
		} else {
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func termFreq(tokens []string) map[string]float64 {
	m := make(map[string]float64)
	for _, t := range tokens {
		m[t]++
	}
	return m
}

// FormatReport 把评测结果格式化为可读报告。
func FormatReport(violations []StructureViolation, collisions []CollisionViolation) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("=== Skill Evals 报告 ===\n"))
	b.WriteString(fmt.Sprintf("Tier 1 结构违规: %d\n", len(violations)))
	for _, v := range violations {
		b.WriteString(fmt.Sprintf("  - [%s] %s (line %d)\n", v.Skill, v.Issue, v.Line))
	}
	b.WriteString(fmt.Sprintf("Tier 2 触发碰撞: %d\n", len(collisions)))
	for _, c := range collisions {
		b.WriteString(fmt.Sprintf("  - %s ↔ %s 相似度 %.2f\n", c.SkillA, c.SkillB, c.Similarity))
	}
	return b.String()
}
