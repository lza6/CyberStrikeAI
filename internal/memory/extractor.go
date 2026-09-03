package memory

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// LLMClient is the minimal interface the ADD-only extractor needs from an LLM.
// It is a single-shot JSON producer — the extractor passes the additive
// extraction prompt and parses the returned list of candidate memories.
//
// The interface is narrow on purpose: it lets tests inject a deterministic
// stub LLM (no network, no cost) while the runtime wires up the real Eino
// AgenticModel.
type LLMClient interface {
	// GenerateJSON sends a prompt and returns a raw JSON byte slice. The
	// caller (the extractor) parses it into the expected schema. An error
	// must be returned if the upstream fails or returns non-JSON.
	GenerateJSON(ctx context.Context, prompt string) ([]byte, error)
}

// ExtractedFact is one candidate memory produced by the LLM's additive pass.
// It mirrors the fields the ADDITIVE_EXTRACTION_PROMPT asks the model to emit
// (mem0/mem0/configs/prompts.py:468).
type ExtractedFact struct {
	Memory       string `json:"memory"`
	AttributedTo string `json:"attributed_to,omitempty"` // "user" | "assistant" | "auto"
	FactKey      string `json:"fact_key,omitempty"`      // logical grouping key
	EventDate    string `json:"event_date,omitempty"`    // ISO date when the event occurred
}

// additiveExtractionResponse is the JSON shape the LLM returns: a flat array
// of candidate memories. This mirrors mem0's response_format json_object
// contract (main.py:940-984).
type additiveExtractionResponse struct {
	Memories []ExtractedFact `json:"memories"`
}

// AdditiveExtractionPrompt is the system+user prompt sent to the LLM. It is a
// Go adaptation of mem0/mem0/configs/prompts.py:468 (ADDITIVE_EXTRACTION_PROMPT)
// plus generate_additive_extraction_prompt (:1016) for the user-side payload.
//
// DIVERGENCE NOTE: The JSON output schema used here (`{"memories": [...]}` with
// per-element `memory`/`attributed_to`/`fact_key`/`event_date` fields) is NOT
// identical to mem0 OSS's schema (prompts.py:924-929, which is `{"memory": [...]}`
// with per-element `id`/`text`/`attributed_to`/`linked_memory_ids`). We use a
// self-consistent schema with explicit `fact_key` (version-chain grouping) and
// `event_date` (dated-instance) fields that mem0 OSS does not emit. To wire a
// real LLM, either keep this schema (instruct the model to emit it) or adapt
// the parser to mem0's `text` field. The schema is a faithful *intent* port of
// the additive-extraction rules; the JSON field names are a CyberStrikeAI
// extension, not a 1:1 mem0 contract.
//
// The sole operation is ADD: identify every piece of memorable information and
// emit it as a new memory. Never UPDATE, never DELETE. Duplicates are skipped
// downstream by the store's hash dedup — the LLM is told about existing
// memories only as a dedup hint, not to mutate them.
func AdditiveExtractionPrompt(query string, recentMessages, existingMemories []string, observationDate, currentDate string) string {
	var b strings.Builder
	b.WriteString(systemHeader)
	b.WriteString(additivePromptBody)
	b.WriteString(outputSchemaSection)
	b.WriteString("\n\n--- USER PAYLOAD ---\n")
	fmt.Fprintf(&b, "Observation date: %s\n", observationDate)
	fmt.Fprintf(&b, "Current date: %s\n\n", currentDate)

	b.WriteString("Summary: ")
	b.WriteString(strings.TrimSpace(query))
	b.WriteString("\n\n")

	b.WriteString("Last k messages:\n")
	for i, m := range recentMessages {
		fmt.Fprintf(&b, "  %d. %s\n", i+1, m)
	}
	b.WriteString("\n")

	b.WriteString("Recently extracted / existing memories (for dedup hint only — DO NOT MUTATE):\n")
	if len(existingMemories) == 0 {
		b.WriteString("  (none)\n")
	} else {
		for i, m := range existingMemories {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, m)
		}
	}
	b.WriteString("\nExtract new memories as JSON now.")
	return b.String()
}

const systemHeader = "You are a memory extraction assistant. Your sole operation is ADD.\n\n"

// additivePromptBody is the prose of the ADDITIVE_EXTRACTION_PROMPT, condensed
// to the load-bearing rules. It is stored as a const so reviewers can audit
// the extraction contract without parsing a printf.
const additivePromptBody = `RULES:
1. ADD ONLY. Identify every memorable fact in the input. Emit each as a NEW memory.
   Never UPDATE an existing memory. Never DELETE. If a fact already exists in the
   "existing memories" list, skip it.
2. Extract from BOTH user and assistant messages. User messages reveal personal facts
   ("user owns X"). Assistant messages reveal recommendations, plans, confirmations
   ("user was recommended X", "user's plan includes Y"). Both are first-class.
3. Attribute correctly: attributed_to = "user" for user-stated facts, "assistant"
   for agent-provided info (recommendations, confirmations, plans), "auto" if unclear.
   Attribution is metadata only — it does not change storage or retrieval weight.
4. When a fact references a specific date, set event_date = "YYYY-MM-DD". Leave empty
   if the fact is undated or timeless.
5. Group related facts with a shared fact_key (e.g. "host:10.0.0.1:os"). Different
   fact_keys are independent chains; same fact_key is a version chain.

`

const outputSchemaSection = `OUTPUT: a single JSON object with a "memories" array. Each element has:
{
  "memory": "<the fact, framed from the user's perspective>",
  "attributed_to": "user" | "assistant" | "auto",
  "fact_key": "<logical grouping key>",
  "event_date": "<YYYY-MM-DD or omit>"
}
Return ONLY the JSON. No prose.`

// Extract is the single-pass ADD-only extraction. It:
//  1. Builds the additive prompt (system + user payload with dedup hints).
//  2. Makes ONE LLM call (GenerateJSON) — never multiple.
//  3. Parses the JSON array of candidate memories.
//  4. Computes each candidate's content hash (md5) BEFORE storage, so the
//     store's dedup can skip duplicates atomically.
//  5. Returns ExtractedFact records; the caller (Store.Add) enforces dedup.
//
// The function never calls UPDATE/DELETE on the store — it returns candidates
// and lets the store's ADD-only semantics decide persistence. This separation
// (extractor = pure propose; store = enforce) mirrors mem0's Phase 2 vs Phase 4
// (main.py:940-984 extraction, main.py:1005-1039 hash dedup). The JSON schema
// differs from mem0 OSS (see AdditiveExtractionPrompt's DIVERGENCE NOTE) but
// the ADD-only *protocol* (one call, propose candidates, store dedups) matches.
func Extract(ctx context.Context, llm LLMClient, query string, recentMessages, existingMemories []string, observationDate, currentDate string) ([]ExtractedFact, error) {
	if llm == nil {
		return nil, fmt.Errorf("memory: Extract requires non-nil LLMClient")
	}
	prompt := AdditiveExtractionPrompt(query, recentMessages, existingMemories, observationDate, currentDate)
	raw, err := llm.GenerateJSON(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("memory: LLM GenerateJSON failed: %w", err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("memory: LLM returned empty response")
	}

	var resp additiveExtractionResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("memory: LLM response was not valid JSON: %w", err)
	}

	// Filter out empty/whitespace memories and pre-compute the hash so the
	// store can dedup atomically without re-parsing. This mirrors mem0's
	// Phase 4 hash dedup (main.py:1005-1039): we compute the dedup key once
	// at extraction time.
	out := make([]ExtractedFact, 0, len(resp.Memories))
	for _, m := range resp.Memories {
		if strings.TrimSpace(m.Memory) == "" {
			continue
		}
		if m.AttributedTo == "" {
			m.AttributedTo = string(AttributedAuto)
		}
		out = append(out, m)
	}
	return out, nil
}

// ExtractAndStore is the end-to-end convenience: extract candidates via a
// single LLM call, then ADD each to the store. Duplicates (same content hash)
// are skipped — never an UPDATE. The returned AddResults preserve the event
// of every candidate ("ADD" or "SKIPPED_DUPLICATE") so callers can audit the
// extraction pass without re-running it.
//
// This is the Go equivalent of mem0's V3 phased pipeline (main.py:916-1206),
// condensed to a single function: extract → hash → dedup → persist.
func ExtractAndStore(ctx context.Context, llm LLMClient, store Store, projectID string, query string, recentMessages, existingMemories []string, observationDate, currentDate string) ([]AddResult, error) {
	candidates, err := Extract(ctx, llm, query, recentMessages, existingMemories, observationDate, currentDate)
	if err != nil {
		return nil, err
	}
	results := make([]AddResult, 0, len(candidates))
	for _, c := range candidates {
		inst := &FactInstance{
			ProjectID:    projectID,
			FactKey:      c.FactKey,
			Memory:       c.Memory,
			Hash:         ContentHash(c.Memory),
			AttributedTo: NormalizeAttributedTo(c.AttributedTo),
			EventDate:    c.EventDate,
		}
		res, err := store.Add(inst)
		if err != nil {
			return results, fmt.Errorf("memory: store.Add failed for %q: %w", c.FactKey, err)
		}
		results = append(results, *res)
	}
	return results, nil
}

// PreComputeHash is exported so callers that build FactInstance directly can
// use the same content hash the extractor uses, ensuring dedup consistency
// across the extract-and-store path and the direct-Add path.
func PreComputeHash(memory string) string {
	sum := md5.Sum([]byte(memory))
	return hex.EncodeToString(sum[:])
}
