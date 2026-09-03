package knowledge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cyberstrike-ai/internal/config"

	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

func TestHTTPReranker_NilReceiver(t *testing.T) {
	var rr *HTTPReranker
	docs := []*schema.Document{{ID: "a", Content: "a"}}
	out, err := rr.Rerank(t.Context(), "q", docs)
	if err != nil {
		t.Fatalf("nil reranker should passthrough: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("out = %d", len(out))
	}
}

func TestHTTPReranker_EmptyQueryOrDocs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("server should not be called for empty query/docs")
	}))
	defer srv.Close()

	rr, err := NewHTTPReranker(&config.RerankConfig{Provider: "cohere", BaseURL: srv.URL, APIKey: "k"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	docs := []*schema.Document{{ID: "a", Content: "a"}}
	out, err := rr.Rerank(t.Context(), "  ", docs)
	if err != nil || len(out) != 1 {
		t.Fatalf("empty query: out=%d err=%v", len(out), err)
	}
	out2, err := rr.Rerank(t.Context(), "q", nil)
	if err != nil || len(out2) != 0 {
		t.Fatalf("nil docs: out=%d err=%v", len(out2), err)
	}
}

func TestHTTPReranker_SingleDocPassthrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("server should not be called for single doc")
	}))
	defer srv.Close()

	rr, err := NewHTTPReranker(&config.RerankConfig{Provider: "cohere", BaseURL: srv.URL, APIKey: "k"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	docs := []*schema.Document{{ID: "a", Content: "a"}}
	out, err := rr.Rerank(t.Context(), "q", docs)
	if err != nil || len(out) != 1 || out[0].ID != "a" {
		t.Fatalf("single doc: out=%v err=%v", out, err)
	}
}

func TestHTTPReranker_NilDocsInList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = jsonEncode(w, map[string]any{"results": []map[string]any{
			{"index": 0},
			{"index": 1},
		}})
	}))
	defer srv.Close()

	rr, err := NewHTTPReranker(&config.RerankConfig{Provider: "cohere", BaseURL: srv.URL, APIKey: "k"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// nil doc becomes "" text; nil doc at idx 1 filtered out of output.
	docs := []*schema.Document{{ID: "a", Content: "a"}, nil}
	out, err := rr.Rerank(t.Context(), "q", docs)
	if err != nil {
		t.Fatalf("nil doc: %v", err)
	}
	if len(out) != 1 || out[0].ID != "a" {
		t.Fatalf("out = %#v", out)
	}
}

func TestHTTPReranker_OutOfRangeIndex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = jsonEncode(w, map[string]any{"results": []map[string]any{
			{"index": 99},
			{"index": 0},
		}})
	}))
	defer srv.Close()

	rr, err := NewHTTPReranker(&config.RerankConfig{Provider: "cohere", BaseURL: srv.URL, APIKey: "k"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	docs := []*schema.Document{{ID: "a", Content: "a"}, {ID: "b", Content: "b"}}
	out, err := rr.Rerank(t.Context(), "q", docs)
	if err != nil {
		t.Fatalf("out-of-range: %v", err)
	}
	if len(out) != 1 || out[0].ID != "a" {
		t.Fatalf("out = %#v", out)
	}
}

func TestHTTPReranker_EmptyResultsFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = jsonEncode(w, map[string]any{"results": []map[string]any{}})
	}))
	defer srv.Close()

	rr, err := NewHTTPReranker(&config.RerankConfig{Provider: "cohere", BaseURL: srv.URL, APIKey: "k"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	docs := []*schema.Document{{ID: "a", Content: "a"}, {ID: "b", Content: "b"}}
	out, err := rr.Rerank(t.Context(), "q", docs)
	if err != nil {
		t.Fatalf("empty results: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("fallback should return original docs, got %d", len(out))
	}
}

func TestHTTPReranker_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(strings.Repeat("x", 600)))
	}))
	defer srv.Close()

	rr, err := NewHTTPReranker(&config.RerankConfig{Provider: "cohere", BaseURL: srv.URL, APIKey: "k"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	docs := []*schema.Document{{ID: "a", Content: "a"}, {ID: "b", Content: "b"}}
	if _, err := rr.Rerank(t.Context(), "q", docs); err == nil {
		t.Fatalf("500 should error")
	}
}

func TestHTTPReranker_BadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	rr, err := NewHTTPReranker(&config.RerankConfig{Provider: "cohere", BaseURL: srv.URL, APIKey: "k"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	docs := []*schema.Document{{ID: "a", Content: "a"}, {ID: "b", Content: "b"}}
	if _, err := rr.Rerank(t.Context(), "q", docs); err == nil {
		t.Fatalf("bad json should error")
	}
}

func TestHTTPReranker_DashScopeHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte("forbidden"))
	}))
	defer srv.Close()

	rr, err := NewHTTPReranker(&config.RerankConfig{Provider: "dashscope", BaseURL: srv.URL, APIKey: "k"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	docs := []*schema.Document{{ID: "a", Content: "a"}, {ID: "b", Content: "b"}}
	if _, err := rr.Rerank(t.Context(), "q", docs); err == nil {
		t.Fatalf("403 should error")
	}
}

func TestHTTPReranker_DashScopeBadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<xml>"))
	}))
	defer srv.Close()

	rr, err := NewHTTPReranker(&config.RerankConfig{Provider: "dashscope", BaseURL: srv.URL, APIKey: "k"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	docs := []*schema.Document{{ID: "a", Content: "a"}, {ID: "b", Content: "b"}}
	if _, err := rr.Rerank(t.Context(), "q", docs); err == nil {
		t.Fatalf("bad json should error")
	}
}

func TestHTTPReranker_ConnectionError(t *testing.T) {
	// Closed server -> connection refused.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()

	rr, err := NewHTTPReranker(&config.RerankConfig{Provider: "cohere", BaseURL: srv.URL, APIKey: "k"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	docs := []*schema.Document{{ID: "a", Content: "a"}, {ID: "b", Content: "b"}}
	if _, err := rr.Rerank(t.Context(), "q", docs); err == nil {
		t.Fatalf("connection error should error")
	}
}

func TestNewHTTPReranker_Errors(t *testing.T) {
	if _, err := NewHTTPReranker(nil, nil, nil); err == nil {
		t.Fatalf("nil config should error")
	}
	if _, err := NewHTTPReranker(&config.RerankConfig{}, nil, nil); err == nil {
		t.Fatalf("missing api key should error")
	}
	// fallback from openai config
	rr, err := NewHTTPReranker(&config.RerankConfig{}, &config.OpenAIConfig{APIKey: "k", BaseURL: "https://api.example.com/v1"}, zap.NewNop())
	if err != nil {
		t.Fatalf("openai fallback: %v", err)
	}
	if rr.apiKey != "k" || rr.baseURL != "https://api.example.com/v1" {
		t.Fatalf("fallback fields: %q %q", rr.apiKey, rr.baseURL)
	}
}

func TestHTTPReranker_URLBuilders(t *testing.T) {
	cases := []struct {
		provider  string
		baseURL   string
		wantPath  string
	}{
		{"cohere", "", "/v1/rerank"},
		{"cohere", "https://x.com", "/v1/rerank"},
		{"cohere", "https://x.com/v1", "/v1/rerank"},
		{"cohere", "https://x.com/v1/", "/v1/rerank"},
		{"dashscope", "", "https://dashscope.aliyuncs.com/api/v1/services/rerank/text-rerank/text-rerank"},
		{"dashscope", "https://dashscope.aliyuncs.com/compatible-mode/v1", "https://dashscope.aliyuncs.com/api/v1/services/rerank/text-rerank/text-rerank"},
		{"dashscope", "https://dashscope.aliyuncs.com/api/v1/services/rerank/text-rerank/text-rerank", "https://dashscope.aliyuncs.com/api/v1/services/rerank/text-rerank/text-rerank"},
		{"dashscope", "https://custom.example.com/rerank", "https://custom.example.com/rerank"},
	}
	for _, tc := range cases {
		rr, err := NewHTTPReranker(&config.RerankConfig{Provider: tc.provider, BaseURL: tc.baseURL, APIKey: "k"}, nil, nil)
		if err != nil {
			t.Fatalf("%v: %v", tc, err)
		}
		var got string
		if tc.provider == "dashscope" {
			got = rr.dashscopeRerankURL()
		} else {
			got = rr.cohereRerankURL()
		}
		if tc.provider == "dashscope" {
			if got != tc.wantPath {
				t.Errorf("dashscopeRerankURL(%q) = %q, want %q", tc.baseURL, got, tc.wantPath)
			}
		} else if !strings.HasSuffix(got, tc.wantPath) {
			t.Errorf("cohereRerankURL(%q) = %q, want suffix %q", tc.baseURL, got, tc.wantPath)
		}
	}
}

func TestTruncateForRerankLog(t *testing.T) {
	if got := truncateForRerankLog("  short  "); got != "short" {
		t.Errorf("trim = %q", got)
	}
	long := strings.Repeat("a", 600)
	got := truncateForRerankLog(long)
	if len(got) != 512+3 {
		t.Errorf("truncate len = %d", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("should end with ...")
	}
}

func TestNopDocumentReranker(t *testing.T) {
	rr := NopDocumentReranker{}
	docs := []*schema.Document{{ID: "a"}, {ID: "b"}}
	out, err := rr.Rerank(t.Context(), "q", docs)
	if err != nil || len(out) != 2 {
		t.Fatalf("nop: out=%d err=%v", len(out), err)
	}
}

func TestHTTPReranker_AuthHeaderSent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("auth header = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("content-type = %q", got)
		}
		_ = jsonEncode(w, map[string]any{"results": []map[string]any{{"index": 0}}})
	}))
	defer srv.Close()

	rr, err := NewHTTPReranker(&config.RerankConfig{Provider: "cohere", BaseURL: srv.URL, APIKey: "test-key"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	docs := []*schema.Document{{ID: "a", Content: "a"}, {ID: "b", Content: "b"}}
	if _, err := rr.Rerank(context.Background(), "q", docs); err != nil {
		t.Fatalf("rerank: %v", err)
	}
}
