package ctxindex

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// Tokenize splits text into lowercase term tokens. It approximates SQLite's
// unicode61 tokenizer: alphanumeric runs are extracted, everything else is a
// separator, and case is folded. Digit-only and single-char tokens are kept
// (FTS5 keeps them too); empty input yields an empty slice.
//
// This is a pure function with no DB dependency, so it can be unit-tested in
// any environment. It is not a stemmer — porter stemming is layered separately
// when a caller opts in.
func Tokenize(text string) []string {
	if text == "" {
		return nil
	}
	var b strings.Builder
	tokens := make([]string, 0, 16)
	flush := func() {
		if b.Len() > 0 {
			tokens = append(tokens, b.String())
			b.Reset()
		}
	}
	for _, r := range text {
		switch {
		case r < 0x80 && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(unicode.ToLower(r))
		case r >= 0x80 && !unicode.IsSpace(r) && !unicode.IsPunct(r):
			// CJK and other non-ASCII letters are kept as-is (lowered for Latin range only).
			b.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return tokens
}

// TermFrequency returns how many times each query term appears in docTokens.
// It is the building block for BM25 term-at-a-time scoring.
func TermFrequency(docTokens, queryTerms []string) map[string]int {
	if len(queryTerms) == 0 {
		return nil
	}
	counts := make(map[string]int, len(queryTerms))
	qset := make(map[string]struct{}, len(queryTerms))
	for _, t := range queryTerms {
		qset[t] = struct{}{}
	}
	for _, t := range docTokens {
		if _, want := qset[t]; want {
			counts[t]++
		}
	}
	return counts
}

// BM25Scores ranks documents against a query using Okapi BM25.
//
// Parameters mirror the canonical form: k1 controls term-frequency saturation
// (typical 1.2–2.0), b controls field-length normalisation (typical 0.75). A
// weight of 5.0 on title and 1.0 on content reproduces context-mode's
// `bm25(chunks, 5.0, 1.0)` (store.ts:585) when titles and content are indexed
// as separate fields. Here we emulate multi-field scoring by computing BM25
// independently on title and content corpora and adding titleWeight*titleScore
// + contentWeight*contentScore.
//
// The function is pure: callers pass tokenised docs; no DB is touched.
func BM25Scores(docs []Document, query string, opts BM25Options) []Scored {
	if len(docs) == 0 || strings.TrimSpace(query) == "" {
		return nil
	}
	if opts.K1 <= 0 {
		opts.K1 = 1.2
	}
	if opts.B < 0 || opts.B > 1 {
		opts.B = 0.75
	}
	if opts.TitleWeight <= 0 {
		opts.TitleWeight = 5.0
	}
	if opts.ContentWeight <= 0 {
		opts.ContentWeight = 1.0
	}

	qTerms := Tokenize(query)
	if len(qTerms) == 0 {
		return nil
	}

	// Pre-tokenise once.
	titleTokens := make([][]string, len(docs))
	contentTokens := make([][]string, len(docs))
	titleLens := make([]float64, len(docs))
	contentLens := make([]float64, len(docs))
	var titleSum, contentSum float64
	for i, d := range docs {
		tt := Tokenize(d.Title)
		ct := Tokenize(d.Content)
		titleTokens[i] = tt
		contentTokens[i] = ct
		titleLens[i] = float64(len(tt))
		contentLens[i] = float64(len(ct))
		titleSum += titleLens[i]
		contentSum += contentLens[i]
	}
	n := float64(len(docs))
	avgTitleLen := titleSum / n
	avgContentLen := contentSum / n
	if avgTitleLen == 0 {
		avgTitleLen = 1
	}
	if avgContentLen == 0 {
		avgContentLen = 1
	}

	// Document frequency per term across both fields combined (a term missing
	// from the whole corpus contributes 0 to every doc's score, which is fine).
	df := make(map[string]int, len(qTerms))
	for _, qt := range qTerms {
		if _, seen := df[qt]; seen {
			continue
		}
		for i := range docs {
			if containsToken(titleTokens[i], qt) || containsToken(contentTokens[i], qt) {
				df[qt]++
			}
		}
	}

	out := make([]Scored, 0, len(docs))
	for i := range docs {
		var score float64
		tfTitle := TermFrequency(titleTokens[i], qTerms)
		tfContent := TermFrequency(contentTokens[i], qTerms)
		for _, qt := range qTerms {
			idf := idf(n, float64(df[qt]))
			score += opts.TitleWeight * bm25Term(idf, float64(tfTitle[qt]), opts.K1, opts.B, titleLens[i], avgTitleLen)
			score += opts.ContentWeight * bm25Term(idf, float64(tfContent[qt]), opts.K1, opts.B, contentLens[i], avgContentLen)
		}
		if score > 0 {
			out = append(out, Scored{Doc: docs[i], Score: score})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		// Stable tiebreak: shorter title first (denser signal), then ID.
		if len(out[i].Doc.Title) != len(out[j].Doc.Title) {
			return len(out[i].Doc.Title) < len(out[j].Doc.Title)
		}
		return out[i].Doc.ID < out[j].Doc.ID
	})
	return out
}

// BM25Options tunes BM25 scoring. Zero values fall back to Okapi defaults.
type BM25Options struct {
	K1            float64
	B             float64
	TitleWeight   float64
	ContentWeight float64
}

// idf is the Okapi BM25 IDF with the +1 smoothing used by most FTS5 ports:
// idf = ln(1 + (N - df + 0.5) / (df + 0.5)).
func idf(n, df float64) float64 {
	if n <= 0 {
		return 0
	}
	if df <= 0 {
		return 0
	}
	num := n - df + 0.5
	den := df + 0.5
	if den <= 0 {
		return 0
	}
	v := 1 + num/den
	if v <= 0 {
		return 0
	}
	// ln on a guarded positive argument.
	x := v
	// tiny inline ln to avoid importing math for one call? keep math import path stable:
	return ln(x)
}

// bm25Term computes the per-term contribution:
// tf*(k1+1) / (tf + k1*(1 - b + b*dl/avgdl))
func bm25Term(idf, tf, k1, b, dl, avgdl float64) float64 {
	if idf <= 0 || tf <= 0 {
		return 0
	}
	if avgdl <= 0 {
		avgdl = 1
	}
	denomNorm := 1 - b + b*(dl/avgdl)
	denom := tf + k1*denomNorm
	if denom <= 0 {
		return 0
	}
	return idf * (tf * (k1 + 1)) / denom
}

func containsToken(tokens []string, want string) bool {
	for _, t := range tokens {
		if t == want {
			return true
		}
	}
	return false
}

// ln is a thin wrapper over math.Log. The indirection keeps BM25 callers free
// of a direct math import while guaranteeing non-negative output for the rare
// guarded-argument case that slipped past idf's clamps.
func ln(x float64) float64 {
	if x <= 0 {
		return 0
	}
	return math.Log(x)
}
