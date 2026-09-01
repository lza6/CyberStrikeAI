// Package skillpackage 提供工具引用漂移门：扫描 skills/*/SKILL.md 正文里引用的工具名，
// 与"真实工具全集"比对，报告引用了不存在的工具（幽灵工具）。设计移植自 caveman
// verbs-gate.mjs（从源派生真实 tool surface，扫描 skill 引用，fail-closed）。
//
// 启发式提取规则：优先抓反引号代码片段（`tool_name`）与 SKILL.md frontmatter
// 的 tools 字段。宁可少报不要漏报——false positive 可由人工复核排除。
package skillpackage

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// VerbsGateViolation 单条违规：skill 引用了不存在的工具。
type VerbsGateViolation struct {
	Skill      string // skill 包名
	File       string // SKILL.md 相对路径
	Line       int    // 行号（1-based）
	Referenced string // 被引用的幽灵工具名
	Hint       string // 提示
}

// ScanToolReferences 扫描 skillsDir 下每个 SKILL.md，提取引用的工具名，
// 与 realTools 比对，返回引用了不存在工具的违规清单。
//
// 提取规则（保守，覆盖主要写法）：
//  1. frontmatter 的 tools: [a, b, c] 或 tools:\n - a\n - b
//  2. 正文中反引号包裹的工具名 `tool_name`（snake_case / kebab-case / 含连字符）
//  3. "使用 exec 工具" / "调用 nmap 工具" 这类中文行内引用只作为补充启发，不强制
//
// realTools 为空时直接返回空（无基线无法判定）。
func ScanToolReferences(skillsDir string, realTools map[string]bool) ([]VerbsGateViolation, error) {
	if len(realTools) == 0 {
		return nil, nil
	}
	if skillsDir == "" {
		return nil, nil
	}
	info, err := os.Stat(skillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取 skills 目录失败: %w", err)
	}
	if !info.IsDir() {
		return nil, nil
	}
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return nil, fmt.Errorf("读取 skills 目录失败: %w", err)
	}

	// 工具名候选正则：反引号里的 snake_case/kebab-case/含连字符的标识符（3+ 字符）
	backtickRe := regexp.MustCompile("`([a-z][a-z0-9_-]{2,})`")
	// frontmatter tools: 行两种形态
	toolsInlineRe := regexp.MustCompile(`(?m)^\s*tools:\s*\[([^\]]*)\]`)
	toolsListRe := regexp.MustCompile(`(?m)^\s*-\s+([a-z][a-z0-9_-]{2,})`)

	var violations []VerbsGateViolation
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		skillFile := filepath.Join(skillsDir, e.Name(), "SKILL.md")
		data, err := os.ReadFile(skillFile)
		if err != nil {
			continue
		}
		lines := strings.Split(string(data), "\n")

		// 分离 frontmatter 与正文
		fmEnd := -1
		if strings.HasPrefix(string(data), "---") {
			for i := 1; i < len(lines); i++ {
				if strings.TrimSpace(lines[i]) == "---" {
					fmEnd = i
					break
				}
			}
		}

		// 1. frontmatter tools 字段
		if fmEnd > 0 {
			inTools := false
			for i := 0; i < fmEnd; i++ {
				line := lines[i]
				if toolsInlineRe.MatchString(line) {
					// inline 形态 tools: [a, b, c]
					m := toolsInlineRe.FindStringSubmatch(line)
					if len(m) > 1 {
						for _, name := range strings.Split(m[1], ",") {
							name = strings.TrimSpace(strings.Trim(name, `"' `))
							if name != "" && isLikelyToolName(name) && !realTools[name] {
								violations = append(violations, VerbsGateViolation{
									Skill: e.Name(), File: filepath.ToSlash(filepath.Join(e.Name(), "SKILL.md")),
									Line: i + 1, Referenced: name, Hint: "frontmatter tools 字段",
								})
							}
						}
					}
					inTools = false
					continue
				}
				if strings.HasPrefix(strings.TrimSpace(line), "tools:") {
					inTools = true
					continue
				}
				if inTools {
					m := toolsListRe.FindStringSubmatch(line)
					if len(m) > 1 {
						name := m[1]
						if isLikelyToolName(name) && !realTools[name] {
							violations = append(violations, VerbsGateViolation{
								Skill: e.Name(), File: filepath.ToSlash(filepath.Join(e.Name(), "SKILL.md")),
								Line: i + 1, Referenced: name, Hint: "frontmatter tools 列表项",
							})
						}
						continue
					}
					// 列表项结束（非 - 开头且非空）
					if strings.TrimSpace(line) != "" && !strings.HasPrefix(strings.TrimSpace(line), "-") {
						inTools = false
					}
				}
			}
		}

		// 2. 正文中反引号工具名
		for i := fmEnd + 1; i < len(lines); i++ {
			for _, m := range backtickRe.FindAllStringSubmatch(lines[i], -1) {
				name := m[1]
				if isLikelyToolName(name) && !realTools[name] {
					violations = append(violations, VerbsGateViolation{
						Skill: e.Name(), File: filepath.ToSlash(filepath.Join(e.Name(), "SKILL.md")),
						Line: i + 1, Referenced: name, Hint: "正文反引号引用",
					})
				}
			}
		}
	}

	// 去重（同一 skill 同一工具多次出现只报一次）
	seen := map[string]bool{}
	var dedup []VerbsGateViolation
	for _, v := range violations {
		key := v.Skill + "|" + v.Referenced
		if !seen[key] {
			seen[key] = true
			dedup = append(dedup, v)
		}
	}
	sort.Slice(dedup, func(i, j int) bool {
		if dedup[i].Skill != dedup[j].Skill {
			return dedup[i].Skill < dedup[j].Skill
		}
		return dedup[i].Referenced < dedup[j].Referenced
	})
	return dedup, nil
}

// isLikelyToolName 过滤明显非工具名的反引号内容（句子/代码块/路径）。
// 工具名约定：snake_case 或 kebab-case，不含空格/点/斜杠，长度 3-40。
func isLikelyToolName(s string) bool {
	if len(s) < 3 || len(s) > 40 {
		return false
	}
	if strings.ContainsAny(s, " ./\\='\":") {
		return false
	}
	// 排除常见非工具词（英文句子片段）
	stop := map[string]bool{
		"the": true, "and": true, "for": true, "with": true, "this": true, "that": true,
		"use": true, "tool": true, "tools": true, "skill": true, "skills": true,
	}
	if stop[strings.ToLower(s)] {
		return false
	}
	return true
}
