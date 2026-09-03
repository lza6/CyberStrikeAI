package ctxindex

import (
	"reflect"
	"testing"
)

func TestTokenize_BasicASCII(t *testing.T) {
	got := Tokenize("Hello, World! 42")
	want := []string{"hello", "world", "42"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Tokenize = %v, want %v", got, want)
	}
}

func TestTokenize_EmptyAndPunct(t *testing.T) {
	if got := Tokenize(""); len(got) != 0 {
		t.Fatalf("Tokenize(\"\") = %v, want empty", got)
	}
	if got := Tokenize(",,,!!!   "); len(got) != 0 {
		t.Fatalf("Tokenize punct-only = %v, want empty", got)
	}
}

func TestTokenize_CJKPreserved(t *testing.T) {
	got := Tokenize("扫描 192.168.1.0/24 端口 80")
	// CJK runs are kept as single tokens; digits split on dot.
	want := []string{"扫描", "192", "168", "1", "0", "24", "端口", "80"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Tokenize CJK = %v, want %v", got, want)
	}
}

func TestBM25_RanksRelevantFirst(t *testing.T) {
	docs := []Document{
		{ID: "a", Title: "nmap scan", Content: "open ports 22 80 443"},
		{ID: "b", Title: "webshell upload", Content: "POST /upload.php php payload"},
		{ID: "c", Title: "sqlmap run", Content: "injection found: admin' --"},
	}
	got := BM25Scores(docs, "nmap scan ports", BM25Options{})
	if len(got) == 0 {
		t.Fatal("expected at least one scored doc")
	}
	if got[0].Doc.ID != "a" {
		t.Fatalf("top hit = %s, want a (nmap doc)", got[0].Doc.ID)
	}
	// Irrelevant doc b should not outrank a.
	for _, s := range got {
		if s.Doc.ID == "b" && s.Score > got[0].Score {
			t.Fatalf("doc b outranked a: b=%.4f a=%.4f", s.Score, got[0].Score)
		}
	}
}

func TestBM25_EmptyQueryReturnsNil(t *testing.T) {
	docs := []Document{{ID: "x", Title: "t", Content: "c"}}
	if got := BM25Scores(docs, "   ", BM25Options{}); got != nil {
		t.Fatalf("empty query should return nil, got %v", got)
	}
}

func TestBM25_TitleWeightBoostsTitleMatch(t *testing.T) {
	// Doc A has the term only in title; doc B only in content. With title
	// weight 5x content weight, A must rank above B for the same query term.
	docs := []Document{
		{ID: "title-only", Title: "recon", Content: "nothing relevant here"},
		{ID: "content-only", Title: "misc", Content: "recon results found"},
	}
	got := BM25Scores(docs, "recon", BM25Options{})
	if len(got) < 2 {
		t.Fatalf("expected 2 scored docs, got %d", len(got))
	}
	if got[0].Doc.ID != "title-only" {
		t.Fatalf("title-weighted match should win: got %s first", got[0].Doc.ID)
	}
}

func TestBM25_OptionsDefaultsApplied(t *testing.T) {
	// Zero-value opts must still score without panic; default k1=1.2, b=0.75.
	docs := []Document{{ID: "d", Title: "x", Content: "important term here"}}
	got := BM25Scores(docs, "important", BM25Options{})
	if len(got) != 1 || got[0].Doc.ID != "d" {
		t.Fatalf("defaults: got %v", got)
	}
	if got[0].Score <= 0 {
		t.Fatalf("score should be positive, got %.4f", got[0].Score)
	}
}

func TestRRF_MergesRankings(t *testing.T) {
	a := []Scored{
		{Doc: Document{ID: "1", Title: "alpha"}, Score: 10},
		{Doc: Document{ID: "2", Title: "beta"}, Score: 5},
	}
	b := []Scored{
		{Doc: Document{ID: "2", Title: "beta"}, Score: 8},
		{Doc: Document{ID: "3", Title: "gamma"}, Score: 4},
	}
	merged := ReciprocalRankFusion([][]Scored{a, b}, 60)
	if len(merged) != 3 {
		t.Fatalf("expected 3 unique docs, got %d", len(merged))
	}
	// Doc 2 appears rank-2 in both lists → strongest RRF signal.
	if merged[0].Doc.ID != "2" {
		t.Fatalf("RRF top should be doc 2 (present in both rankings), got %s", merged[0].Doc.ID)
	}
}

func TestRRF_EmptyInput(t *testing.T) {
	if got := ReciprocalRankFusion(nil, 60); got != nil {
		t.Fatalf("nil input should return nil, got %v", got)
	}
	if got := ReciprocalRankFusion([][]Scored{{}}, 60); got != nil {
		t.Fatalf("empty rankings should return nil, got %v", got)
	}
}

func TestRRF_KDefaultsTo60(t *testing.T) {
	a := []Scored{{Doc: Document{ID: "1"}, Score: 1}}
	// k=0 should fall back to 60 and still produce a score.
	merged := ReciprocalRankFusion([][]Scored{a}, 0)
	if len(merged) != 1 || merged[0].Score <= 0 {
		t.Fatalf("k=0 default: got %v", merged)
	}
}

func TestBuildVerdict_NoHits(t *testing.T) {
	if got := BuildVerdict(nil, "q", 5); got != "" {
		t.Fatalf("nil hits should return empty, got %q", got)
	}
	if got := BuildVerdict([]Scored{}, "q", 5); got != "" {
		t.Fatalf("empty hits should return empty, got %q", got)
	}
}

func TestBuildVerdict_BoundedPreview(t *testing.T) {
	hits := []Scored{
		{Doc: Document{ID: "h1", Title: "port 22", Content: "SSH-2.0-OpenSSH_8.9p1 Ubuntu-3ubuntu0.10\nBanner line two\nBanner line three"}},
	}
	out := BuildVerdict(hits, "ssh", 5)
	if out == "" {
		t.Fatal("expected non-empty verdict")
	}
	// Verdict must NOT contain content beyond first line.
	if !contains(out, "SSH-2.0-OpenSSH_8.9p1 Ubuntu-3ubuntu0.10") {
		t.Fatalf("verdict missing first-line preview: %q", out)
	}
	if contains(out, "Banner line two") {
		t.Fatalf("verdict leaked second line into context: %q", out)
	}
	if !contains(out, "ctx_search") {
		t.Fatalf("verdict should point to ctx_search: %q", out)
	}
}

func TestBuildVerdict_MaxResults(t *testing.T) {
	hits := []Scored{
		{Doc: Document{ID: "1", Title: "t1", Content: "line1"}},
		{Doc: Document{ID: "2", Title: "t2", Content: "line2"}},
		{Doc: Document{ID: "3", Title: "t3", Content: "line3"}},
	}
	out := BuildVerdict(hits, "q", 2)
	// 2 hit lines + header + footer pointer; doc 3 must not appear.
	if contains(out, "line3") {
		t.Fatalf("maxResults=2 should omit 3rd hit: %q", out)
	}
	if !contains(out, "line1") || !contains(out, "line2") {
		t.Fatalf("maxResults=2 should include first 2 hits: %q", out)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
