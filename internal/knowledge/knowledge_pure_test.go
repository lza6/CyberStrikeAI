package knowledge

import (
	"testing"

	"github.com/cloudwego/eino/schema"
)

// ---- chunk_eino.go: tokenizerLenFunc ----

func TestTokenizerLenFunc_Fallback(t *testing.T) {
	// Empty model -> fallback rune/4.
	f := tokenizerLenFunc("")
	if f("") != 0 {
		t.Errorf("empty string should return 0")
	}
	// "hello" (5 runes) -> (5+3)/4 = 2.
	if got := f("hello"); got != 2 {
		t.Errorf("tokenizerLenFunc(\"hello\") = %d, want 2", got)
	}
	// "你好" (2 runes) -> (2+3)/4 = 1.
	if got := f("你好"); got != 1 {
		t.Errorf("tokenizerLenFunc(\"你好\") = %d, want 1", got)
	}
}

func TestTokenizerLenFunc_KnownModel(t *testing.T) {
	f := tokenizerLenFunc("text-embedding-ada-002")
	// tiktoken cache is available offline; a plain ASCII word has a small
	// deterministic token count.
	n := f("hello world")
	if n < 1 {
		t.Errorf("expected >0 tokens for known model, got %d", n)
	}
	// Unknown model must not panic and should fall back to rune/4.
	uf := tokenizerLenFunc("no-such-model-xyz")
	if uf("hello") != 2 {
		t.Errorf("unknown model fallback = %d, want 2", uf("hello"))
	}
}

func TestTokenizerLenFunc_WhitespaceModel(t *testing.T) {
	// Whitespace-only model name treated as empty -> fallback.
	f := tokenizerLenFunc("   ")
	if f("") != 0 {
		t.Errorf("whitespace model should fallback")
	}
}

// ---- chunk_eino.go: newKnowledgeSplitter ----

func TestNewKnowledgeSplitter_InvalidSize(t *testing.T) {
	if _, err := newKnowledgeSplitter(0, 8, ""); err == nil {
		t.Fatalf("chunk size 0 should error")
	}
	if _, err := newKnowledgeSplitter(-1, 8, ""); err == nil {
		t.Fatalf("negative chunk size should error")
	}
}

func TestNewKnowledgeSplitter_NegativeOverlap(t *testing.T) {
	sp, err := newKnowledgeSplitter(64, -5, "")
	if err != nil {
		t.Fatalf("negative overlap should be clamped, got err %v", err)
	}
	if sp == nil {
		t.Fatalf("splitter nil")
	}
}

func TestNewKnowledgeSplitter_Transform(t *testing.T) {
	sp, err := newKnowledgeSplitter(32, 4, "text-embedding-3-small")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"short", "hello world"},
		{"long", "## 子标题\n\n这是一段用于测试分块是否按分隔符边界正确切开的长文本。\n\n## 另一个标题\n\n更多内容。这里包含很多字符，用来验证分块逻辑在长度超过 chunk_size 时的行为。\n\n### 三级标题\n\n补充说明。"},
	}
	for _, tc := range cases {
		docs, err := sp.Transform(t.Context(), []*schema.Document{{Content: tc.in}})
		if err != nil {
			t.Errorf("%s: transform err %v", tc.name, err)
			continue
		}
		// Panics/errors are the main concern; just assert non-nil output.
		if docs == nil {
			t.Errorf("%s: nil docs output", tc.name)
		}
	}
}

// ---- chunk_eino.go: newMarkdownHeaderSplitter ----

func TestNewMarkdownHeaderSplitter(t *testing.T) {
	sp, err := newMarkdownHeaderSplitter(t.Context())
	if err != nil {
		t.Fatalf("newMarkdownHeaderSplitter: %v", err)
	}
	if sp == nil {
		t.Fatalf("splitter nil")
	}
	docs, err := sp.Transform(t.Context(), []*schema.Document{
		{Content: "# 一\n内容A\n## 二\n内容B\n### 三\n内容C"},
	})
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	// At least one chunk. Header splitter should produce multiple sections.
	if len(docs) < 1 {
		t.Errorf("expected >=1 chunk, got %d", len(docs))
	}
}

// ---- eino_meta.go helpers ----

func TestMetaLookupString(t *testing.T) {
	if got := MetaLookupString(nil, "k"); got != "" {
		t.Errorf("nil map = %q", got)
	}
	if got := MetaLookupString(map[string]any{}, "k"); got != "" {
		t.Errorf("missing key = %q", got)
	}
	if got := MetaLookupString(map[string]any{"k": nil}, "k"); got != "" {
		t.Errorf("nil value = %q", got)
	}
	if got := MetaLookupString(map[string]any{"k": "v"}, "k"); got != "v" {
		t.Errorf("string value = %q", got)
	}
	if got := MetaLookupString(map[string]any{"k": 42}, "k"); got != "42" {
		t.Errorf("int value = %q", got)
	}
	if got := MetaLookupString(map[string]any{"k": 3.14}, "k"); got != "3.14" {
		t.Errorf("float value = %q", got)
	}
}

func TestMetaStringOK(t *testing.T) {
	if s, ok := MetaStringOK(nil, "k"); ok || s != "" {
		t.Errorf("nil: s=%q ok=%v", s, ok)
	}
	if s, ok := MetaStringOK(map[string]any{"k": "  "}, "k"); ok || s != "" {
		t.Errorf("whitespace: s=%q ok=%v", s, ok)
	}
	if s, ok := MetaStringOK(map[string]any{"k": " v "}, "k"); !ok || s != "v" {
		t.Errorf("value: s=%q ok=%v", s, ok)
	}
}

func TestRequireMetaString(t *testing.T) {
	if _, err := RequireMetaString(nil, "k"); err == nil {
		t.Errorf("nil map should error")
	}
	if _, err := RequireMetaString(map[string]any{}, "k"); err == nil {
		t.Errorf("missing key should error")
	}
	if _, err := RequireMetaString(map[string]any{"k": ""}, "k"); err == nil {
		t.Errorf("empty value should error")
	}
	if s, err := RequireMetaString(map[string]any{"k": "ok"}, "k"); err != nil || s != "ok" {
		t.Errorf("valid: s=%q err=%v", s, err)
	}
}

func TestRequireMetaInt(t *testing.T) {
	if _, err := RequireMetaInt(nil, "k"); err == nil {
		t.Errorf("nil map should error")
	}
	if _, err := RequireMetaInt(map[string]any{}, "k"); err == nil {
		t.Errorf("missing key should error")
	}
	valid := map[string]any{
		"i":   int(3),
		"i32": int32(3),
		"i64": int64(3),
		"f64": float64(3),
	}
	for k, want := range map[string]int{"i": 3, "i32": 3, "i64": 3, "f64": 3} {
		v, err := RequireMetaInt(valid, k)
		if err != nil || v != want {
			t.Errorf("RequireMetaInt(%s) = %d, %v; want %d", k, v, err, want)
		}
	}
	if _, err := RequireMetaInt(map[string]any{"k": "x"}, "k"); err == nil {
		t.Errorf("unsupported type should error")
	}
}

func TestDSLNumeric(t *testing.T) {
	if v, ok := DSLNumeric(float64(1.5)); !ok || v != 1.5 {
		t.Errorf("float64: %v %v", v, ok)
	}
	if v, ok := DSLNumeric(float32(1.5)); !ok || v != 1.5 {
		t.Errorf("float32: %v %v", v, ok)
	}
	if v, ok := DSLNumeric(int(3)); !ok || v != 3 {
		t.Errorf("int: %v %v", v, ok)
	}
	if v, ok := DSLNumeric(int64(3)); !ok || v != 3 {
		t.Errorf("int64: %v %v", v, ok)
	}
	if v, ok := DSLNumeric(uint64(3)); !ok || v != 3 {
		t.Errorf("uint64: %v %v", v, ok)
	}
	if _, ok := DSLNumeric("x"); ok {
		t.Errorf("string should not be numeric")
	}
}

func TestMetaFloat64OK(t *testing.T) {
	if v, ok := MetaFloat64OK(nil, "k"); ok || v != 0 {
		t.Errorf("nil: %v %v", v, ok)
	}
	if v, ok := MetaFloat64OK(map[string]any{"k": "x"}, "k"); ok {
		t.Errorf("string should not be numeric: %v", v)
	}
	if v, ok := MetaFloat64OK(map[string]any{"k": 0.9}, "k"); !ok || v != 0.9 {
		t.Errorf("float: %v %v", v, ok)
	}
}

// ---- retriever.go: cosineSimilarity ----

func TestCosineSimilarity(t *testing.T) {
	// identical
	if got := cosineSimilarity([]float32{1, 0, 0}, []float32{1, 0, 0}); got != 1.0 {
		t.Errorf("identical = %v, want 1", got)
	}
	// orthogonal
	if got := cosineSimilarity([]float32{1, 0}, []float32{0, 1}); got != 0 {
		t.Errorf("orthogonal = %v, want 0", got)
	}
	// zero vector
	if got := cosineSimilarity([]float32{0, 0}, []float32{0, 0}); got != 0 {
		t.Errorf("zero = %v, want 0", got)
	}
	// length mismatch
	if got := cosineSimilarity([]float32{1, 2}, []float32{1}); got != 0 {
		t.Errorf("mismatch = %v, want 0", got)
	}
	// partial (cross product known): a=1,0; b=1,0 -> 1
	if got := cosineSimilarity([]float32{1, 0}, []float32{1, 0}); got != 1 {
		t.Errorf("parallel = %v, want 1", got)
	}
}

// ---- tool.go pure helpers ----

func TestContains(t *testing.T) {
	if !contains([]string{"a", "b"}, "a") {
		t.Errorf("contains a")
	}
	if contains([]string{"a"}, "b") {
		t.Errorf("should not contain b")
	}
	if contains(nil, "x") {
		t.Errorf("nil slice")
	}
}

func TestGetRetrievalMetadata(t *testing.T) {
	q, rt := GetRetrievalMetadata(map[string]any{"query": "q", "risk_type": "XSS"})
	if q != "q" || rt != "XSS" {
		t.Errorf("got %q %q", q, rt)
	}
	q, rt = GetRetrievalMetadata(map[string]any{})
	if q != "" || rt != "" {
		t.Errorf("empty got %q %q", q, rt)
	}
	q, rt = GetRetrievalMetadata(map[string]any{"query": 123})
	if q != "" {
		t.Errorf("non-string query = %q", q)
	}
}

func TestFormatRetrievalResults(t *testing.T) {
	if s := FormatRetrievalResults(nil); s != "未找到相关结果" {
		t.Errorf("empty = %q", s)
	}
	results := []*RetrievalResult{
		{Item: &KnowledgeItem{ID: "i1", Category: "XSS", Title: "t1"}, Similarity: 0.9},
		{Item: &KnowledgeItem{ID: "i2", Category: "SQLi", Title: "t2"}, Similarity: 0.8},
	}
	s := FormatRetrievalResults(results)
	if s == "" || s == "未找到相关结果" {
		t.Errorf("unexpected result %q", s)
	}
}

// ---- types.go: formatTime + MarshalJSON ----

func TestFormatTime(t *testing.T) {
	if got := formatTime(zeroTime()); got != "" {
		t.Errorf("zero = %q, want empty", got)
	}
}

func TestKnowledgeItemMarshalJSON(t *testing.T) {
	item := &KnowledgeItem{ID: "i1", Category: "cat", Title: "t", CreatedAt: fixedTime(), UpdatedAt: fixedTime()}
	b, err := item.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	if s == "" {
		t.Fatalf("empty json")
	}
}

func TestKnowledgeItemSummaryMarshalJSON(t *testing.T) {
	item := &KnowledgeItemSummary{ID: "i1", Category: "cat", Title: "t", CreatedAt: fixedTime(), UpdatedAt: fixedTime()}
	b, err := item.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(b) == 0 {
		t.Fatalf("empty json")
	}
}

func TestRetrievalLogMarshalJSON(t *testing.T) {
	log := &RetrievalLog{ID: "l1", Query: "q", CreatedAt: fixedTime()}
	b, err := log.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(b) == 0 {
		t.Fatalf("empty json")
	}
}

// ---- retriever.go: RetrievalConfigFromYAML ----

func TestRetrievalConfigFromYAML(t *testing.T) {
	rc := RetrievalConfigFromYAML(yamlRetrievalConfig())
	if rc.TopK != 3 || rc.SimilarityThreshold != 0.5 || rc.SubIndexFilter != "x" {
		t.Errorf("mapping wrong: %+v", rc)
	}
}
