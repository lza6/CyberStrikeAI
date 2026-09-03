package memory

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// stubLLM returns a canned JSON response so the extractor's single-pass logic
// can be tested without any network or cost. It records the prompt it received
// so tests can assert the additive-prompt shape.
type stubLLM struct {
	response []byte
	err      error
	lastPrompt string
}

func (s *stubLLM) GenerateJSON(_ context.Context, prompt string) ([]byte, error) {
	s.lastPrompt = prompt
	if s.err != nil {
		return nil, s.err
	}
	return s.response, nil
}

func TestExtract_SingleLLMCallProducesCandidates(t *testing.T) {
	resp, _ := json.Marshal(additiveExtractionResponse{
		Memories: []ExtractedFact{
			{Memory: "user owns a Tesla Model 3", AttributedTo: "user", FactKey: "car"},
			{Memory: "user was recommended to patch CVE-2026-1234", AttributedTo: "assistant", FactKey: "cve:2026-1234"},
		},
	})
	llm := &stubLLM{response: resp}
	out, err := Extract(context.Background(), llm, "I own a Tesla and you told me to patch a CVE", nil, nil, "2026-09-02", "2026-09-02")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("want 2 candidates, got %d", len(out))
	}
	if out[0].Memory != "user owns a Tesla Model 3" {
		t.Errorf("candidate 0 = %q", out[0].Memory)
	}
	if out[1].AttributedTo != "assistant" {
		t.Errorf("candidate 1 attributed_to = %q, want assistant", out[1].AttributedTo)
	}
}

func TestExtract_FiltersEmptyMemories(t *testing.T) {
	resp, _ := json.Marshal(additiveExtractionResponse{
		Memories: []ExtractedFact{
			{Memory: "real fact", FactKey: "k1"},
			{Memory: "   ", FactKey: "k2"},
			{Memory: "", FactKey: "k3"},
		},
	})
	llm := &stubLLM{response: resp}
	out, err := Extract(context.Background(), llm, "q", nil, nil, "2026-09-02", "2026-09-02")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("empty memories must be filtered, got %d", len(out))
	}
	if out[0].Memory != "real fact" {
		t.Errorf("survivor = %q", out[0].Memory)
	}
}

func TestExtract_DefaultsAttributedToAuto(t *testing.T) {
	resp, _ := json.Marshal(additiveExtractionResponse{
		Memories: []ExtractedFact{{Memory: "x", FactKey: "k"}},
	})
	llm := &stubLLM{response: resp}
	out, _ := Extract(context.Background(), llm, "q", nil, nil, "2026-09-02", "2026-09-02")
	if out[0].AttributedTo != string(AttributedAuto) {
		t.Errorf("default attributed_to = %q, want auto", out[0].AttributedTo)
	}
}

func TestExtract_LLMErrorPropagates(t *testing.T) {
	llm := &stubLLM{err: errors.New("upstream 500")}
	_, err := Extract(context.Background(), llm, "q", nil, nil, "2026-09-02", "2026-09-02")
	if err == nil {
		t.Fatal("LLM error must propagate")
	}
}

func TestExtract_InvalidJSONErrors(t *testing.T) {
	llm := &stubLLM{response: []byte("not json")}
	_, err := Extract(context.Background(), llm, "q", nil, nil, "2026-09-02", "2026-09-02")
	if err == nil {
		t.Fatal("invalid JSON must error")
	}
}

func TestExtract_EmptyResponseErrors(t *testing.T) {
	llm := &stubLLM{response: []byte{}}
	_, err := Extract(context.Background(), llm, "q", nil, nil, "2026-09-02", "2026-09-02")
	if err == nil {
		t.Fatal("empty response must error")
	}
}

func TestExtract_NilLLMErrors(t *testing.T) {
	_, err := Extract(context.Background(), nil, "q", nil, nil, "2026-09-02", "2026-09-02")
	if err == nil {
		t.Fatal("nil LLM must error")
	}
}

func TestExtract_PromptContainsExistingMemoriesAsDedupHint(t *testing.T) {
	resp, _ := json.Marshal(additiveExtractionResponse{Memories: []ExtractedFact{}})
	llm := &stubLLM{response: resp}
	_, _ = Extract(context.Background(), llm, "q", nil, []string{"already known fact"}, "2026-09-02", "2026-09-02")
	if !strings.Contains(llm.lastPrompt, "already known fact") {
		t.Errorf("existing memories must appear in prompt as dedup hint; prompt was:\n%s", llm.lastPrompt)
	}
	if !strings.Contains(llm.lastPrompt, "DO NOT MUTATE") {
		t.Error("prompt must instruct the LLM not to mutate existing memories")
	}
}

// TestExtractAndStore_SingleCallThenADDOnly is the keystone end-to-end for
// capability #1 + #4: one LLM call produces candidates, each is ADDed, and
// duplicates (same content hash) are skipped — never UPDATED. Agent-attributed
// and auto-attributed facts land in the same active pool with equal weight.
func TestExtractAndStore_SingleCallThenADDOnly(t *testing.T) {
	resp, _ := json.Marshal(additiveExtractionResponse{
		Memories: []ExtractedFact{
			{Memory: "user owns a Tesla", AttributedTo: "user", FactKey: "car"},
			{Memory: "user was told to patch CVE-X", AttributedTo: "assistant", FactKey: "cve"},
			{Memory: "user owns a Tesla", AttributedTo: "user", FactKey: "car"}, // dup → skipped
		},
	})
	llm := &stubLLM{response: resp}
	store := NewMemoryStore()

	results, err := ExtractAndStore(context.Background(), llm, store, "proj-EXT", "q", nil, nil, "2026-09-02", "2026-09-02")
	if err != nil {
		t.Fatalf("ExtractAndStore: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("want 3 AddResults, got %d", len(results))
	}
	adds, skips := 0, 0
	for _, r := range results {
		switch r.Event {
		case "ADD":
			adds++
		case "SKIPPED_DUPLICATE":
			skips++
		}
	}
	if adds != 2 {
		t.Errorf("ADD events = %d, want 2", adds)
	}
	if skips != 1 {
		t.Errorf("SKIPPED_DUPLICATE events = %d, want 1", skips)
	}

	// Store has exactly 2 instances (the dup was deduped, not stored as a 3rd).
	active, _ := store.ListActive("proj-EXT", "")
	if len(active) != 2 {
		t.Errorf("stored active count = %d, want 2 (dedup skipped the dup)", len(active))
	}

	// Both attributions present in the same pool — equal weight (cap #4).
	attrs := map[string]bool{}
	for _, inst := range active {
		attrs[string(inst.AttributedTo)] = true
	}
	if !attrs["user"] || !attrs["assistant"] {
		t.Errorf("active pool attributions = %v, must include user and assistant", attrs)
	}
}

func TestPreComputeHash_MatchesContentHash(t *testing.T) {
	if PreComputeHash("x") != ContentHash("x") {
		t.Error("PreComputeHash must equal ContentHash")
	}
}
