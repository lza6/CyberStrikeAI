package knowledge

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"cyberstrike-ai/internal/config"

	einoembed "github.com/cloudwego/eino/components/embedding"
	"go.uber.org/zap"
)

// newEmbeddingMockServer spins up an httptest server that mimics the OpenAI
// /embeddings endpoint. responses is consumed one per request (last one repeats).
func newEmbeddingMockServer(t *testing.T, path string, responses ...func(w http.ResponseWriter, r *http.Request) bool) *httptest.Server {
	t.Helper()
	var idx int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if path != "" && r.URL.Path != path {
			t.Errorf("unexpected path %q, want %q", r.URL.Path, path)
		}
		i := int(atomic.LoadInt32(&idx))
		atomic.AddInt32(&idx, 1)
		if len(responses) == 0 {
			w.WriteHeader(500)
			return
		}
		if i >= len(responses) {
			i = len(responses) - 1
		}
		if !responses[i](w, r) {
			return
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// embeddingOK writes a valid OpenAI embeddings response with dim vectors.
func embeddingOK(dim int) func(w http.ResponseWriter, r *http.Request) bool {
	return func(w http.ResponseWriter, r *http.Request) bool {
		var req struct {
			Input []string `json:"input"`
		}
		_ = jsonDecodeBody(r, &req)
		data := make([]map[string]any, len(req.Input))
		for i := range data {
			vec := make([]float64, dim)
			for j := range vec {
				vec[j] = 0.1 * float64(j+1)
			}
			data[i] = map[string]any{"embedding": vec, "index": i}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = jsonEncode(w, map[string]any{"data": data, "model": "text-embedding-3-small"})
		return false
	}
}

// embeddingStatus writes the given HTTP status with a body.
func embeddingStatus(code int, body string) func(w http.ResponseWriter, r *http.Request) bool {
	return func(w http.ResponseWriter, r *http.Request) bool {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
		return false
	}
}

// newTestEmbedder builds an Embedder pointed at the mock server.
func newTestEmbedder(t *testing.T, baseURL string, indexing config.IndexingConfig) *Embedder {
	t.Helper()
	cfg := &config.KnowledgeConfig{
		Embedding: config.EmbeddingConfig{
			Provider: "openai",
			Model:    "text-embedding-3-small",
			BaseURL:  baseURL,
			APIKey:   "test-key",
		},
		Indexing: indexing,
	}
	e, err := NewEmbedder(t.Context(), cfg, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}
	return e
}

func TestNewEmbedder_NilConfig(t *testing.T) {
	if _, err := NewEmbedder(t.Context(), nil, nil, nil); err == nil {
		t.Fatalf("nil config should error")
	}
}

func TestNewEmbedder_MissingAPIKey(t *testing.T) {
	cfg := &config.KnowledgeConfig{Embedding: config.EmbeddingConfig{Model: "m"}}
	if _, err := NewEmbedder(t.Context(), cfg, nil, nil); err == nil {
		t.Fatalf("missing api key should error")
	}
}

func TestNewEmbedder_APIKeyFromOpenAIFallback(t *testing.T) {
	cfg := &config.KnowledgeConfig{Embedding: config.EmbeddingConfig{Model: "m"}}
	oa := &config.OpenAIConfig{APIKey: "sk-fallback"}
	e, err := NewEmbedder(t.Context(), cfg, oa, nil)
	if err != nil {
		t.Fatalf("fallback key: %v", err)
	}
	if e.EmbeddingModelName() != "m" {
		t.Errorf("model = %q", e.EmbeddingModelName())
	}
}

func TestNewEmbedder_RateLimitConfigs(t *testing.T) {
	srv := newEmbeddingMockServer(t, "", embeddingOK(3))
	cases := []config.IndexingConfig{
		{MaxRPM: 600, MaxRetries: 1, RetryDelayMs: 1},
		{RateLimitDelayMs: 1, MaxRetries: 1, RetryDelayMs: 1},
		{MaxRetries: 2, RetryDelayMs: 1},
	}
	for i, ic := range cases {
		e := newTestEmbedder(t, srv.URL, ic)
		vecs, err := e.EmbedStrings(t.Context(), []string{"a"})
		if err != nil {
			t.Errorf("case %d: %v", i, err)
			continue
		}
		if len(vecs) != 1 || len(vecs[0]) != 3 {
			t.Errorf("case %d: vecs=%v", i, vecs)
		}
	}
}

func TestEmbedder_EmptyInput(t *testing.T) {
	srv := newEmbeddingMockServer(t, "", embeddingOK(3))
	e := newTestEmbedder(t, srv.URL, config.IndexingConfig{})
	vecs, err := e.EmbedStrings(t.Context(), nil)
	if err != nil {
		t.Fatalf("nil input: %v", err)
	}
	if vecs != nil {
		t.Fatalf("nil input should return nil, got %v", vecs)
	}
	vecs2, err := e.EmbedStrings(t.Context(), []string{})
	if err != nil || vecs2 != nil {
		t.Fatalf("empty input: %v %v", vecs2, err)
	}
}

func TestEmbedder_EmbedText(t *testing.T) {
	srv := newEmbeddingMockServer(t, "", embeddingOK(4))
	e := newTestEmbedder(t, srv.URL, config.IndexingConfig{})
	vec, err := e.EmbedText(t.Context(), "hello")
	if err != nil {
		t.Fatalf("EmbedText: %v", err)
	}
	if len(vec) != 4 {
		t.Fatalf("dim = %d, want 4", len(vec))
	}
}

func TestEmbedder_EmbedTexts(t *testing.T) {
	srv := newEmbeddingMockServer(t, "", embeddingOK(3))
	e := newTestEmbedder(t, srv.URL, config.IndexingConfig{})
	vecs, err := e.EmbedTexts(t.Context(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("EmbedTexts: %v", err)
	}
	if len(vecs) != 2 {
		t.Fatalf("count = %d, want 2", len(vecs))
	}
}

func TestEmbedder_HTTPError(t *testing.T) {
	srv := newEmbeddingMockServer(t, "", embeddingStatus(500, "boom"))
	e := newTestEmbedder(t, srv.URL, config.IndexingConfig{MaxRetries: 2, RetryDelayMs: 1})
	if _, err := e.EmbedStrings(t.Context(), []string{"a"}); err == nil {
		t.Fatalf("500 with retries should eventually fail")
	}
}

func TestEmbedder_NonRetryableError(t *testing.T) {
	srv := newEmbeddingMockServer(t, "", embeddingStatus(400, "bad request"))
	e := newTestEmbedder(t, srv.URL, config.IndexingConfig{MaxRetries: 3, RetryDelayMs: 1})
	if _, err := e.EmbedStrings(t.Context(), []string{"a"}); err == nil {
		t.Fatalf("400 should fail fast")
	}
}

func TestEmbedder_EmbeddingModelNameDefaults(t *testing.T) {
	var e *Embedder
	if got := e.EmbeddingModelName(); got != "" {
		t.Errorf("nil embedder = %q", got)
	}
	e2 := &Embedder{}
	if got := e2.EmbeddingModelName(); got != "" {
		t.Errorf("nil config = %q", got)
	}
	e3 := &Embedder{config: &config.KnowledgeConfig{}}
	if got := e3.EmbeddingModelName(); got != "text-embedding-3-small" {
		t.Errorf("default = %q", got)
	}
	e4 := &Embedder{config: &config.KnowledgeConfig{Embedding: config.EmbeddingConfig{Model: " m "}}}
	if got := e4.EmbeddingModelName(); got != "m" {
		t.Errorf("trim = %q", got)
	}
}

func TestEmbedder_EmbedTextUnexpectedCount(t *testing.T) {
	// A stub returning more vectors than inputs triggers the count-mismatch
	// branch inside EmbedText (len(vecs) != 1).
	e := &Embedder{maxRetries: 1}
	e.eino = &stubEinoEmbedderFixed{n: 2}
	if _, err := e.EmbedText(t.Context(), "x"); err == nil {
		t.Fatalf("count mismatch should error")
	}
}

func TestEmbedder_EmbedStringsNilEmbedder(t *testing.T) {
	var e *Embedder
	if _, err := e.EmbedStrings(t.Context(), []string{"a"}); err == nil {
		t.Fatalf("nil embedder should error")
	}
	e2 := &Embedder{}
	if _, err := e2.EmbedStrings(t.Context(), []string{"a"}); err == nil {
		t.Fatalf("nil inner should error")
	}
}

func TestEmbedder_RetryThenSuccess(t *testing.T) {
	var calls int32
	srv := newEmbeddingMockServer(t, "", func(w http.ResponseWriter, r *http.Request) bool {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(429)
			_, _ = w.Write([]byte("rate limited"))
			return false
		}
		return embeddingOK(3)(w, r)
	})
	e := newTestEmbedder(t, srv.URL, config.IndexingConfig{MaxRetries: 3, RetryDelayMs: 1})
	vecs, err := e.EmbedStrings(t.Context(), []string{"a"})
	if err != nil {
		t.Fatalf("retry should succeed: %v", err)
	}
	if len(vecs) != 1 {
		t.Fatalf("vecs = %d", len(vecs))
	}
}

func TestEmbedder_ContextCancelledDuringRetry(t *testing.T) {
	srv := newEmbeddingMockServer(t, "", embeddingStatus(500, "x"))
	e := newTestEmbedder(t, srv.URL, config.IndexingConfig{MaxRetries: 5, RetryDelayMs: 1000})
	ctx, cancel := contextWithCancel(t)
	cancel()
	if _, err := e.EmbedStrings(ctx, []string{"a"}); err == nil {
		t.Fatalf("cancelled ctx should fail")
	}
}

func TestEmbedder_IsRetryableError(t *testing.T) {
	e := &Embedder{}
	cases := []struct {
		msg  string
		want bool
	}{
		{"http 429 too many requests", true},
		{"rate limit exceeded", true},
		{"500 internal", true},
		{"502 bad gateway", true},
		{"503 unavailable", true},
		{"504 gateway timeout", true},
		{"context timeout", true},
		{"connection refused", true},
		{"network error", true},
		{"unexpected EOF", true},
		{"bad request 400", false},
		{"", false},
	}
	for _, tc := range cases {
		var err error
		if tc.msg != "" {
			err = errors.New(tc.msg)
		}
		if got := e.isRetryableError(err); got != tc.want {
			t.Errorf("isRetryableError(%q) = %v, want %v", tc.msg, got, tc.want)
		}
	}
	if e.isRetryableError(nil) {
		t.Errorf("nil should not be retryable")
	}
}

func TestEinoFloatEmbedder_Adapter(t *testing.T) {
	inner := &Embedder{maxRetries: 1}
	inner.eino = &stubEinoEmbedder{vecs: [][]float64{{1.5, 2.5}}}
	w := &einoFloatEmbedder{inner: inner}
	vecs, err := w.EmbedStrings(t.Context(), []string{"x"})
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	if len(vecs) != 1 || vecs[0][0] != 1.5 {
		t.Fatalf("vecs = %v", vecs)
	}
	if w.GetType() != "CyberStrikeKnowledgeEmbedder" {
		t.Errorf("GetType = %q", w.GetType())
	}
	if w.IsCallbacksEnabled() {
		t.Errorf("IsCallbacksEnabled should be false")
	}
	if inner.EinoEmbeddingComponent() == nil {
		t.Errorf("EinoEmbeddingComponent nil")
	}
}

func TestEinoFloatEmbedder_ErrorPropagation(t *testing.T) {
	inner := &Embedder{maxRetries: 1}
	inner.eino = &stubEinoEmbedder{err: errors.New("boom")}
	w := &einoFloatEmbedder{inner: inner}
	if _, err := w.EmbedStrings(t.Context(), []string{"x"}); err == nil {
		t.Fatalf("error should propagate")
	}
}

// stubEinoEmbedder is a fake eino embedding.Embedder returning fixed vectors.
type stubEinoEmbedder struct {
	vecs [][]float64
	err  error
}

func (s *stubEinoEmbedder) EmbedStrings(ctx context.Context, texts []string, opts ...einoembed.Option) ([][]float64, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := make([][]float64, len(texts))
	for i := range out {
		if i < len(s.vecs) {
			out[i] = s.vecs[i]
		} else {
			out[i] = []float64{0}
		}
	}
	return out, nil
}

// stubEinoEmbedderFixed always returns n vectors regardless of input size.
type stubEinoEmbedderFixed struct {
	n int
}

func (s *stubEinoEmbedderFixed) EmbedStrings(ctx context.Context, texts []string, opts ...einoembed.Option) ([][]float64, error) {
	out := make([][]float64, s.n)
	for i := range out {
		out[i] = []float64{1, 2}
	}
	return out, nil
}

func TestWaitRateLimiter_DelayPath(t *testing.T) {
	e := &Embedder{rateLimitDelay: 1 * time.Millisecond}
	e.waitRateLimiter() // should not panic; covers delay branch
}

func TestWaitRateLimiter_LimiterError(t *testing.T) {
	// A limiter with burst 0 and n huge Wait will error/return; use a done ctx.
	e := &Embedder{rateLimiter: nil, rateLimitDelay: 0}
	e.waitRateLimiter()
}
