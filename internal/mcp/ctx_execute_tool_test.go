package mcp

import (
	"context"
	"strings"
	"testing"

	"cyberstrike-ai/internal/ctxsandbox"
)

func TestRegisterCtxExecuteTool_NilGuards(t *testing.T) {
	// Must not panic on nil inputs.
	RegisterCtxExecuteTool(nil, &ctxsandbox.Engine{})
	RegisterCtxExecuteTool(NewServer(nil), nil)
}

func TestCtxExecute_MissingCommand(t *testing.T) {
	s, _, handler := newCtxExecuteServer(t)
	defer s.ClearTools()
	res, err := handler(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !res.IsError {
		t.Fatal("missing command should be an error result")
	}
	if !strings.Contains(res.Content[0].Text, "command") {
		t.Fatalf("error text should mention command: %q", res.Content[0].Text)
	}
}

func TestCtxExecute_EmptyCommandArray(t *testing.T) {
	s, _, handler := newCtxExecuteServer(t)
	defer s.ClearTools()
	res, _ := handler(context.Background(), map[string]interface{}{"command": []interface{}{}})
	if !res.IsError {
		t.Fatal("empty command array should be an error result")
	}
}

func TestCtxExecute_SmallOutputVerbatim(t *testing.T) {
	s, _, handler := newCtxExecuteServer(t)
	defer s.ClearTools()
	res, err := handler(context.Background(), map[string]interface{}{
		"command": []interface{}{"echo", "small-payload"},
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.IsError {
		t.Fatalf("small output should not error: %q", res.Content[0].Text)
	}
	if !strings.Contains(res.Content[0].Text, "small-payload") {
		t.Fatalf("verbatim payload missing: %q", res.Content[0].Text)
	}
}

func TestCtxExecute_LargeOutputIndexed(t *testing.T) {
	s, idx, handler := newCtxExecuteServer(t)
	defer s.ClearTools()
	// Produce ~200KB of stdout so we clear the 102400-byte force-index path.
	// Each line ≈ 32 bytes (numbered "line N: portscan result banner N"); 4000
	// lines reliably exceeds 100KB even with short early-line padding.
	res, err := handler(context.Background(), map[string]interface{}{
		"command": []interface{}{"sh", "-c", "for i in $(seq 1 4000); do printf 'line %d: portscan result banner %d\\n' $i $i; done"},
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Content[0].Text == "" {
		t.Fatal("expected non-empty result")
	}
	// Either force-indexed (ctx_search pointer) or intent-verdict; both are
	// valid reductions. The hard requirement is: index has chunks AND raw
	// content is NOT returned wholesale.
	if idx.Size() == 0 {
		t.Fatalf("index should hold chunks after large output; raw text was: %q",
			truncateForLog(res.Content[0].Text, 200))
	}
	if !strings.Contains(res.Content[0].Text, "ctx_search") {
		t.Fatalf("result should mention ctx_search; got: %q",
			truncateForLog(res.Content[0].Text, 200))
	}
	// Raw payload must not have leaked wholesale: the result text should be
	// far smaller than the ~200KB we produced.
	if len(res.Content[0].Text) > 5000 {
		t.Fatalf("result text too large (%d bytes) — reduction ineffective: %q",
			len(res.Content[0].Text), truncateForLog(res.Content[0].Text, 200))
	}
}

func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func TestCtxExecute_IntentVerdictNoRawLeak(t *testing.T) {
	s, _, handler := newCtxExecuteServer(t)
	defer s.ClearTools()
	// Produce >5KB AND >100KB so the large-output force-index path applies
	// (intent verdict is level 1 for 5–100KB; here we also assert no raw
	// leak under either reduction path). ~200 sections × ~80 bytes ≈ 16KB
	// is below 100KB, so instead generate ~150KB to hit the force-index
	// path deterministically, then assert no raw detail-line leakage.
	res, err := handler(context.Background(), map[string]interface{}{
		"command": []interface{}{"sh", "-c", "for i in $(seq 1 3000); do echo \"section $i: open port 22 ssh\"; echo \"  detail line $i\"; done"},
		"intent":  "port 22 ssh",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if strings.Contains(res.Content[0].Text, "detail line") {
		t.Fatalf("verdict/pointer leaked raw detail lines: %q",
			truncateForLog(res.Content[0].Text, 300))
	}
	if !strings.Contains(res.Content[0].Text, "execute:sh") {
		t.Fatalf("result should surface indexed source label or pointer: %q",
			truncateForLog(res.Content[0].Text, 300))
	}
}

func TestCtxExecute_BuiltinWhitelist(t *testing.T) {
	s, _, _ := newCtxExecuteServer(t)
	defer s.ClearTools()
	if !IsCtxExecuteTool(ctxExecuteToolName) {
		t.Fatal("IsCtxExecuteTool should recognise its own name")
	}
	if IsCtxExecuteTool("definitely-not-a-tool") {
		t.Fatal("IsCtxExecuteTool false positive")
	}
}

// newCtxExecuteServer builds a Server with ctx_execute registered against a
// fresh in-memory sandbox index, returning the handler for direct invocation
// in tests (bypassing the JSON-RPC dispatch layer).
func newCtxExecuteServer(t *testing.T) (*Server, *ctxsandbox.MemoryIndex, ToolHandler) {
	t.Helper()
	idx := ctxsandbox.NewMemoryIndex()
	engine := &ctxsandbox.Engine{Index: idx}
	s := NewServer(nil)
	RegisterCtxExecuteTool(s, engine)
	s.mu.RLock()
	h, ok := s.tools[ctxExecuteToolName]
	s.mu.RUnlock()
	if !ok {
		t.Fatal("ctx_execute tool not registered")
	}
	return s, idx, h
}
