package mcp

import (
	"context"
	"strings"
	"testing"

	"cyberstrike-ai/internal/ctxsandbox"
)

func TestRegisterCtxSearchTool_NilGuards(t *testing.T) {
	RegisterCtxSearchTool(nil, ctxsandbox.NewMemoryIndex())
	s := NewServer(nil)
	RegisterCtxSearchTool(s, nil) // nil index: registered but will no-op on query
}

func TestCtxSearch_MissingQueries(t *testing.T) {
	s, _, h := newCtxSearchServer(t)
	defer s.ClearTools()
	res, err := h(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !res.IsError {
		t.Fatal("missing queries should be an error result")
	}
}

func TestCtxSearch_EmptyQueriesArray(t *testing.T) {
	s, _, h := newCtxSearchServer(t)
	defer s.ClearTools()
	res, _ := h(context.Background(), map[string]interface{}{"queries": []interface{}{}})
	if !res.IsError {
		t.Fatal("empty queries array should error")
	}
}

func TestCtxSearch_NoHitsReturnsEmpty(t *testing.T) {
	s, _, h := newCtxSearchServer(t)
	defer s.ClearTools()
	res, err := h(context.Background(), map[string]interface{}{
		"queries": []interface{}{"nonexistent-term"},
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.IsError {
		t.Fatalf("no hits should not be an error: %q", res.Content[0].Text)
	}
	if !strings.Contains(res.Content[0].Text, "无匹配") {
		t.Fatalf("should report no matches: %q", res.Content[0].Text)
	}
}

func TestCtxSearch_RetrievesIndexedContent(t *testing.T) {
	s, idx, h := newCtxSearchServer(t)
	defer s.ClearTools()
	// Seed the index with a chunk whose content we then retrieve.
	idx.IndexPlaintext("open port 22 ssh banner: SSH-2.0-OpenSSH_8.9\nversion 2.0\n", "execute:nmap")
	res, err := h(context.Background(), map[string]interface{}{
		"queries": []interface{}{"ssh banner"},
		"source":  "execute:nmap",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.IsError {
		t.Fatalf("retrieval should not error: %q", res.Content[0].Text)
	}
	// Full content must come back (this is the retrieval loop closure).
	if !strings.Contains(res.Content[0].Text, "SSH-2.0-OpenSSH_8.9") {
		t.Fatalf("retrieved content missing full section: %q", res.Content[0].Text)
	}
	if !strings.Contains(res.Content[0].Text, "execute:nmap") {
		t.Fatalf("source label should be shown: %q", res.Content[0].Text)
	}
}

func TestCtxSearch_MultiQueryOrSemantics(t *testing.T) {
	s, idx, h := newCtxSearchServer(t)
	defer s.ClearTools()
	idx.IndexPlaintext("alpha section about nmap portscan", "execute:sh")
	idx.IndexPlaintext("beta section about webshell upload", "execute:sh")
	// Two queries; each matches a different section. Both should appear.
	res, _ := h(context.Background(), map[string]interface{}{
		"queries": []interface{}{"nmap", "webshell"},
	})
	if !strings.Contains(res.Content[0].Text, "nmap") {
		t.Fatalf("query 1 hit missing: %q", res.Content[0].Text)
	}
	if !strings.Contains(res.Content[0].Text, "webshell") {
		t.Fatalf("query 2 hit missing: %q", res.Content[0].Text)
	}
}

func TestCtxSearch_PerSectionCapPreventsReflood(t *testing.T) {
	s, idx, h := newCtxSearchServer(t)
	defer s.ClearTools()
	// One huge section: retrieval must cap per-section so we don't re-flood.
	big := strings.Repeat("X", 50_000)
	idx.IndexPlaintext("huge "+big, "execute:sh")
	res, _ := h(context.Background(), map[string]interface{}{
		"queries": []interface{}{"huge"},
	})
	if len(res.Content[0].Text) > ctxSearchTotalMaxBytes+500 {
		t.Fatalf("retrieval re-flooded context: got %d bytes", len(res.Content[0].Text))
	}
	if !strings.Contains(res.Content[0].Text, "…") {
		t.Fatalf("truncated content should carry ellipsis: %q",
			truncateForLog(res.Content[0].Text, 200))
	}
}

func TestCtxSearch_SourceScopeFilters(t *testing.T) {
	s, idx, h := newCtxSearchServer(t)
	defer s.ClearTools()
	idx.IndexPlaintext("shared term in nmap run", "execute:nmap")
	idx.IndexPlaintext("shared term in curl run", "execute:curl")
	// Scope to nmap → must not leak curl's section.
	res, _ := h(context.Background(), map[string]interface{}{
		"queries": []interface{}{"shared term"},
		"source":  "execute:nmap",
	})
	if strings.Contains(res.Content[0].Text, "curl") {
		t.Fatalf("source scope leaked cross-source content: %q",
			truncateForLog(res.Content[0].Text, 200))
	}
	if !strings.Contains(res.Content[0].Text, "nmap") {
		t.Fatalf("scoped content missing: %q",
			truncateForLog(res.Content[0].Text, 200))
	}
}

func TestIsCtxSearchTool(t *testing.T) {
	if !IsCtxSearchTool(ctxSearchToolName) {
		t.Fatal("should recognise its own name")
	}
	if IsCtxSearchTool("not-a-tool") {
		t.Fatal("false positive")
	}
}

func newCtxSearchServer(t *testing.T) (*Server, *ctxsandbox.MemoryIndex, ToolHandler) {
	t.Helper()
	idx := ctxsandbox.NewMemoryIndex()
	s := NewServer(nil)
	RegisterCtxSearchTool(s, idx)
	s.mu.RLock()
	h, ok := s.tools[ctxSearchToolName]
	s.mu.RUnlock()
	if !ok {
		t.Fatal("ctx_search tool not registered")
	}
	return s, idx, h
}
