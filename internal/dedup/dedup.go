// Package dedup 检测重复漏洞提交。
//
// 通过对比新漏洞的 title（可选 target）与已有提交语料，判断是否为重复报告。
//
// 算法刻意保持简单：token-set Jaccard 相似度 + 停用词过滤。足以识别
// "SQL Injection in /search" 与 "Sql injection on the search endpoint" 之类
// 的同一漏洞不同表述。更复杂的编辑距离类指标会引入外部依赖，而此阶段
// 边际收益有限。
//
// 移植自 Pentest-Swarm-AI 的 internal/agent/report/dedup 包，语义保持一致。
package dedup

import (
	"sort"
	"strings"
)

// Prior 是一条已有提交的最小记录——仅包含基于标题匹配所需字段。
// 调用方将平台特定的结构映射到此处。
type Prior struct {
	ID     string
	Title  string
	Target string // 可选；若设置且匹配，则提升相似度
	State  string // 可选状态，例如 "duplicate"、"resolved"
}

// Match 是一条排序后的命中结果：（已有提交，相似度 0..1）。
type Match struct {
	Prior      Prior
	Similarity float64
}

// FindDuplicates 返回新漏洞标题在阈值之上、Top-K 的匹配结果。
// 结果为空表示无可信重复。
//
// 默认阈值 0.6 是合理默认值——标题有 60%+ 词重叠通常即为同一漏洞。
// 若研究员反馈漏报，可下调；若反馈误报，可上调。
//
// target 匹配时相似度提升：sim = 1.0 - (1.0-sim)*0.5，与参考项目一致。
func FindDuplicates(title, target string, priors []Prior, threshold float64, k int) []Match {
	if threshold <= 0 {
		threshold = 0.6
	}
	if k <= 0 {
		k = 3
	}
	tokens := tokenise(title)
	hits := make([]Match, 0, len(priors))
	for _, p := range priors {
		sim := jaccard(tokens, tokenise(p.Title))
		if target != "" && p.Target != "" && strings.EqualFold(target, p.Target) {
			// target 匹配是强信号——提升相似度。
			sim = 1.0 - (1.0-sim)*0.5
		}
		if sim >= threshold {
			hits = append(hits, Match{Prior: p, Similarity: sim})
		}
	}
	// 按相似度降序排序——此处语料规模极小（几十条而非百万级）。
	sort.Slice(hits, func(i, j int) bool {
		return hits[i].Similarity > hits[j].Similarity
	})
	if len(hits) > k {
		hits = hits[:k]
	}
	return hits
}

// --- 内部实现 ---

var stopwords = map[string]struct{}{}

func init() {
	for _, w := range []string{
		"a", "an", "and", "at", "by", "for", "in", "is", "it", "of",
		"on", "or", "the", "to", "via", "with", "through",
	} {
		stopwords[w] = struct{}{}
	}
}

// tokenise 将字符串小写化、按非字母数字字符切分，并剔除停用词。
func tokenise(s string) map[string]struct{} {
	out := map[string]struct{}{}
	cur := strings.Builder{}
	flush := func() {
		w := cur.String()
		cur.Reset()
		if len(w) < 2 {
			return
		}
		if _, stop := stopwords[w]; stop {
			return
		}
		out[w] = struct{}{}
	}
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

// jaccard = |A ∩ B| / |A ∪ B|。1.0 表示集合相同，0.0 表示完全不相交。
func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	inter := 0
	for k := range a {
		if _, ok := b[k]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}
