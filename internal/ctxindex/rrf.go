package ctxindex

import (
	"sort"
	"strings"
)

// ReciprocalRankFusion merges multiple ranked result lists into a single
// ranking using RRF with the standard K=60 smoothing constant. Each input
// list must already be sorted best-first; RRF is rank-based so absolute
// scores are irrelevant. This mirrors context-mode's store.ts:1244-1284.
//
// The function is pure and CGO-free.
func ReciprocalRankFusion(rankings [][]Scored, k int) []Scored {
	if len(rankings) == 0 {
		return nil
	}
	if k <= 0 {
		k = 60
	}
	type acc struct {
		doc   Document
		score float64
		best  float64 // best individual score across rankings (tiebreak)
	}
	merged := make(map[string]*acc, 64)
	anyHits := false
	for _, ranking := range rankings {
		for i, s := range ranking {
			anyHits = true
			key := s.Doc.ID
			if key == "" {
				key = s.Doc.Title + "|" + s.Doc.Content
			}
			a, ok := merged[key]
			if !ok {
				a = &acc{doc: s.Doc}
				merged[key] = a
			}
			a.score += 1.0 / float64(k+i+1)
			if s.Score > a.best {
				a.best = s.Score
			}
		}
	}
	if !anyHits {
		return nil
	}
	out := make([]Scored, 0, len(merged))
	for _, a := range merged {
		out = append(out, Scored{Doc: a.doc, Score: a.score})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].Doc.ID != out[j].Doc.ID {
			return out[i].Doc.ID < out[j].Doc.ID
		}
		return false
	})
	return out
}

// BuildVerdict renders ranked hits into the compact "title + first-line
// preview" format the model sees when intent-search deduplicates a large
// output. It never returns the raw content of a hit — only a 120-rune
// preview of its first line — so the context budget stays bounded. This is
// the Go port of context-mode's server.ts:2015-2025 `intentSearch`.
//
// The function is pure and CGO-free.
func BuildVerdict(hits []Scored, query string, maxResults int) string {
	q := strings.TrimSpace(query)
	var b strings.Builder
	if len(hits) == 0 || q == "" {
		return ""
	}
	if maxResults <= 0 || maxResults > len(hits) {
		maxResults = len(hits)
	}
	b.WriteString("Indexed sections matching \"")
	b.WriteString(q)
	b.WriteString("\":\n\n")
	for i := 0; i < maxResults; i++ {
		preview := firstLinePreview(hits[i].Doc.Content, 120)
		b.WriteString("  - ")
		if h := strings.TrimSpace(hits[i].Doc.Title); h != "" {
			b.WriteString(h)
		} else {
			b.WriteString(hits[i].Doc.ID)
		}
		b.WriteString(": ")
		b.WriteString(preview)
		b.WriteString("\n")
	}
	b.WriteString("\nUse ctx_search(queries: [...]) to retrieve full content of any section.")
	return b.String()
}

// firstLinePreview returns the first line of content trimmed to maxRunes
// runes, without cutting multi-byte glyphs mid-character.
func firstLinePreview(content string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	// Take up to the first newline.
	if nl := strings.IndexByte(content, '\n'); nl >= 0 {
		content = content[:nl]
	}
	content = strings.TrimSpace(content)
	if maxRunes >= len([]rune(content)) {
		return content
	}
	runes := []rune(content)
	return string(runes[:maxRunes])
}
