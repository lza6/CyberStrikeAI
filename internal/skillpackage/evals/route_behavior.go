package evals

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ============================================================================
// Tier 3 行为评测（离线确定性路由模拟）
//
// 设计说明（反伪实现）：
//   - 真实 LLM 在环评测需付费 API 调用（项目付费红线），不做伪造。
//   - 本文件实现 Tier 3 的确定性子集：给定用户查询文本，按 skill description
//     的词面重合度模拟"最可能被路由到的 skill"，验证：
//       1) 每条查询在其目标 skill 的 description 中有词面证据（可路由性）；
//       2) 同一查询命中的多个 skill 中目标 skill 排名第一（路由正确性）；
//       3) 无查询命中超过 N 个 skill（路由发散度上限）。
//   - 这无法替代真实 LLM 路由评测（语义理解），但能在零成本下捕获
//     "description 措辞与预期触发场景完全脱节"的回归——Tier 2 只查碰撞，
//     查不出"与别的 skill 不像但也没写对触发词"的情况。
//   - 需要真实 LLM 的语义评测仍标 TODO（evals_tier3_llm.go 预留），不伪造。
// ============================================================================

// RouteCase 一条 Tier 3 路由用例。
type RouteCase struct {
	// Query 模拟用户查询（中文或英文短语）。
	Query string
	// ExpectedSkill 期望被路由到的 skill 目录名。
	ExpectedSkill string
}

// RouteMiss 一条路由偏差。
type RouteMiss struct {
	Query       string  `json:"query"`
	Expected    string  `json:"expected_skill"`
	Routed      string  `json:"routed_skill,omitempty"`
	TopScore    float64 `json:"top_score,omitempty"`
	TargetScore float64 `json:"target_score,omitempty"`
	Reason      string  `json:"reason"`
}

// RouteBehaviorResult Tier 3 离线路由评测结果。
type RouteBehaviorResult struct {
	TotalCases int         `json:"total_cases"`
	Passed     int         `json:"passed"`
	Misses     []RouteMiss `json:"misses"`
}

// skillDoc 内部：skill 名 + 拼接的 description 文本。
type skillDoc struct {
	name string
	desc string
}

// loadSkillDocs 读取 skillsDir 下全部 SKILL.md 的 name + description。
func loadSkillDocs(skillsDir string) ([]skillDoc, error) {
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return nil, err
	}
	var docs []skillDoc
	for _, e := range entries {
		if !e.IsDir() || isSkillContainer(skillsDir, e.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(skillsDir, e.Name(), "SKILL.md"))
		if err != nil {
			continue // 结构问题归 Tier 1 管
		}
		desc := extractDescription(string(data))
		if desc == "" {
			continue
		}
		docs = append(docs, skillDoc{name: e.Name(), desc: desc})
	}
	return docs, nil
}

// extractDescription 从 SKILL.md frontmatter 提取 description 值（支持跨行折叠）。
func extractDescription(content string) string {
	if !strings.HasPrefix(content, "---") {
		return ""
	}
	lines := strings.Split(content, "\n")
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return ""
	}
	var vals []string
	inDesc := false
	for _, line := range lines[1:end] {
		if strings.HasPrefix(line, "description:") {
			inDesc = true
			first := strings.TrimSpace(strings.TrimPrefix(line, "description:"))
			// 折叠标量（>- / |-）
			if first == ">-" || first == ">" || first == "|-" || first == "|" {
				continue
			}
			if first != "" {
				vals = append(vals, first)
			}
			continue
		}
		if inDesc {
			// 折叠块内的续行（缩进开头）；遇到下一个顶级键停止
			if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
				vals = append(vals, strings.TrimSpace(line))
			} else if strings.TrimSpace(line) != "" {
				inDesc = false
			}
		}
	}
	return strings.Join(vals, " ")
}

// routeScore 计算 query 对 skill description 的词面路由得分（0-1）。
// 机制：query 分词在 desc 中的命中率（子串匹配，兼容中文单字/英文词干）。
func routeScore(query, desc string) float64 {
	qTokens := tokenize(query)
	if len(qTokens) == 0 {
		return 0
	}
	dLower := strings.ToLower(desc)
	hit := 0
	for _, t := range qTokens {
		if strings.Contains(dLower, t) {
			hit++
		}
	}
	return float64(hit) / float64(len(qTokens))
}

// EvaluateRouteBehavior Tier 3 离线路由评测。
// 对每条用例：算全部 skill 的 routeScore，目标 skill 需满足：
//   1) 目标得分 ≥ 0.34（约 1/3 词面命中，description 有可路由证据）；
//   2) 目标排名并列第一（或差距 < 0.1 视为并列）；
//   3) 无超过 maxDivergence 个 skill 得分 ≥ 0.5（路由发散度）。
func EvaluateRouteBehavior(skillsDir string, cases []RouteCase) (*RouteBehaviorResult, error) {
	docs, err := loadSkillDocs(skillsDir)
	if err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		return nil, fmt.Errorf("no skills loaded from %s", skillsDir)
	}

	result := &RouteBehaviorResult{TotalCases: len(cases)}
	for _, tc := range cases {
		type scored struct {
			name  string
			score float64
		}
		scores := make([]scored, 0, len(docs))
		var target float64
		for _, d := range docs {
			s := routeScore(tc.Query, d.desc)
			scores = append(scores, scored{d.name, s})
			if d.name == tc.ExpectedSkill {
				target = s
			}
		}
		sort.Slice(scores, func(i, j int) bool {
			if scores[i].score != scores[j].score {
				return scores[i].score > scores[j].score
			}
			return scores[i].name < scores[j].name
		})

		// 检查 1：目标 description 有词面证据
		if target < 0.34 {
			result.Misses = append(result.Misses, RouteMiss{
				Query: tc.Query, Expected: tc.ExpectedSkill,
				TargetScore: target,
				Reason:      fmt.Sprintf("目标 skill description 词面证据不足（%.2f < 0.34），真实 LLM 路由大概率 miss", target),
			})
			continue
		}

		// 检查 2：目标排名（并列第一或差距 < 0.1）
		top := scores[0]
		if top.name != tc.ExpectedSkill && top.score-target >= 0.1 {
			result.Misses = append(result.Misses, RouteMiss{
				Query: tc.Query, Expected: tc.ExpectedSkill,
				Routed: top.name, TopScore: top.score, TargetScore: target,
				Reason: fmt.Sprintf("词面路由第一名是 %s（%.2f），目标仅 %.2f", top.name, top.score, target),
			})
			continue
		}

		// 检查 3：路由发散度（≥0.5 的 skill 数）
		const maxDivergence = 6
		diverge := 0
		for _, s := range scores {
			if s.score >= 0.5 {
				diverge++
			}
		}
		if diverge > maxDivergence {
			result.Misses = append(result.Misses, RouteMiss{
				Query: tc.Query, Expected: tc.ExpectedSkill,
				Reason: fmt.Sprintf("%d 个 skill 得分 ≥0.5（上限 %d），description 措辞区分度不足", diverge, maxDivergence),
			})
			continue
		}

		result.Passed++
	}
	return result, nil
}

// FormatRouteReport 把 Tier 3 结果格式化为报告段。
func FormatRouteReport(r *RouteBehaviorResult) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Tier 3 离线路由评测: %d/%d 通过\n", r.Passed, r.TotalCases))
	for _, m := range r.Misses {
		b.WriteString(fmt.Sprintf("  - [%s] %s\n    %s\n", m.Expected, m.Query, m.Reason))
	}
	return b.String()
}
