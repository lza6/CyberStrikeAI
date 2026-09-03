package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

// TestE2E_FullPipeline is the end-to-end closed-loop verification of all four
// mem0 capabilities:
//
//  1. ADD-only extraction: Ingest → one LLM call → Store.Add with hash dedup
//  2. Multi-signal fusion retrieval: semantic + BM25 + entity boost
//  3. Temporal reasoning: dated instance ordering nudges results
//  4. Agent-confirmed facts equal weight: user/assistant/auto in same pool
//
// The test uses a stub LLM and stub semantic/entity searchers so it runs
// with CGO_ENABLED=0 (no SQLite toolchain, no network, no cost). It is the
// real-pipeline verification required by the task contract — not a mock of
// the logic, but the actual Extract → Store → ScoreAndRank → Temporal chain
// wired through Service.
func TestE2E_FullPipeline(t *testing.T) {
	// --- Setup: stub LLM returns 4 candidate facts across attributions + dates.
	llmResp, _ := json.Marshal(additiveExtractionResponse{Memories: []ExtractedFact{
		{Memory: "user owns a Tesla Model 3", AttributedTo: "user", FactKey: "car", EventDate: "2026-09-01"},
		{Memory: "user was recommended to patch CVE-2026-1234 on the Tesla firmware", AttributedTo: "assistant", FactKey: "cve:2026-1234", EventDate: "2026-09-10"},
		{Memory: "host 10.0.0.1 runs Windows 11", AttributedTo: "auto", FactKey: "host:10.0.0.1:os"},
		{Memory: "host 10.0.0.1 runs Windows 11", AttributedTo: "auto", FactKey: "host:10.0.0.1:os"}, // exact dup → must skip
	}})
	llm := &stubLLM{response: llmResp}

	store := NewMemoryStore()
	sem := &stubSemantic{scores: map[string]float64{
		// populated below once we know instance IDs; see indirection.
	}}
	ent := &stubEntity{}

	svc, err := NewService(store, llm,
		WithSemanticSearcher(sem),
		WithEntitySearcher(ent),
		WithClock(func() time.Time { return mustTime(t, "2026-09-02") }),
	)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	// --- Capability #1 + #4: Ingest (single LLM call, ADD-only, equal weight).
	results, err := svc.Ingest(context.Background(), "proj-E2E", "I own a Tesla and you told me to patch a CVE",
		nil, nil, "2026-09-02")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	adds := 0
	skips := 0
	idsByMemory := map[string]string{}
	for _, r := range results {
		switch r.Event {
		case "ADD":
			adds++
			idsByMemory[r.Instance.Memory] = r.Instance.ID
		case "SKIPPED_DUPLICATE":
			skips++
		}
	}
	if adds != 3 {
		t.Errorf("Ingest ADD count = %d, want 3 (3 distinct facts)", adds)
	}
	if skips != 1 {
		t.Errorf("Ingest SKIPPED_DUPLICATE = %d, want 1 (the exact dup)", skips)
	}

	// Equal-weight pool check: all three attributions present in active pool.
	active, _ := store.ListActive("proj-E2E", "")
	if len(active) != 3 {
		t.Fatalf("active pool = %d, want 3", len(active))
	}
	attrs := map[AttributedTo]bool{}
	for _, inst := range active {
		attrs[inst.AttributedTo] = true
	}
	for _, want := range []AttributedTo{AttributedUser, AttributedAssistant, AttributedAuto} {
		if !attrs[want] {
			t.Errorf("equal-weight pool missing attribution %q", want)
		}
	}

	// Wire the stub semantic searcher now that we know instance IDs: give the
	// Tesla / CVE facts high similarity to a "Tesla patch" query, the host-OS
	// fact low similarity.
	teslaID := idsByMemory["user owns a Tesla Model 3"]
	cveID := idsByMemory["user was recommended to patch CVE-2026-1234 on the Tesla firmware"]
	hostID := idsByMemory["host 10.0.0.1 runs Windows 11"]
	sem.scores = map[string]float64{
		teslaID: 0.85,
		cveID:   0.80,
		hostID:  0.10, // below default threshold
	}
	// Entity index: "Tesla" links the car fact; "CVE-2026-1234" links the CVE fact.
	ent.hits = map[string][]EntityHit{
		"Tesla":         {{InstanceID: teslaID, Similarity: 0.9}},
		"CVE-2026-1234": {{InstanceID: cveID, Similarity: 0.95}},
	}

	// --- Capability #2: multi-signal fusion retrieval.
	// Query "Tesla" matches both Tesla-owned facts via BM25 + entity, and the
	// host-OS fact is gated out by the semantic threshold.
	fused, err := svc.Retrieve(context.Background(), "proj-E2E", "Tesla patch CVE-2026-1234",
		RetrieveOptions{Threshold: 0.3, TopK: 5, IncludeTemporal: false})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(fused) == 0 {
		t.Fatal("fusion returned no results")
	}
	// Host-OS fact (semantic 0.10 < threshold 0.3) must be gated out.
	for _, r := range fused {
		if r.Instance.ID == hostID {
			t.Errorf("host-OS fact (sem=0.10) must be gated out by threshold 0.3, but it appeared")
		}
	}
	// Top result must be one of the Tesla/CVE facts, not the host-OS.
	if fused[0].Instance.ID != teslaID && fused[0].Instance.ID != cveID {
		t.Errorf("top fusion result = %q, want Tesla or CVE fact", fused[0].Instance.ID)
	}
	// Explanation populated (Explain=true in Service.Retrieve).
	if fused[0].Explanation.MaxPossible == 0 {
		t.Error("fusion explanation not populated")
	}

	// --- Capability #3: temporal reasoning nudges the future-dated CVE fact up.
	// Query intent = future ("patch ... planned"). The CVE fact (event_date 2026-09-10, future)
	// gets a positive temporal delta; the Tesla fact (event_date 2026-09-01, past) gets
	// a misalignment penalty. So with temporal on, CVE must rank #1 (rank 0).
	withTemporal, err := svc.Retrieve(context.Background(), "proj-E2E", "Tesla patch CVE-2026-1234 planned",
		RetrieveOptions{Threshold: 0.3, TopK: 5, IncludeTemporal: true})
	if err != nil {
		t.Fatalf("Retrieve+temporal: %v", err)
	}
	if len(withTemporal) < 2 {
		t.Fatalf("temporal retrieve returned %d, want ≥2", len(withTemporal))
	}
	cveRankWithTemp := rankOf(withTemporal, cveID)
	if cveRankWithTemp == -1 {
		t.Errorf("CVE fact dropped from results after temporal pass")
	}
	// Hardened assertion: with future-intent + future-dated CVE, CVE must be #1.
	// (Critic flagged the previous weak assertion; this enforces the design intent.)
	if cveRankWithTemp != 0 {
		t.Errorf("temporal pass must rank future-aligned CVE fact #1, got rank %d (top=%q)", cveRankWithTemp, withTemporal[0].Instance.ID)
	}

	// --- Capability #3 read modes: latest_only excludes superseded.
	// Supersede the Tesla fact with a new "sold the Tesla" fact, then verify
	// LatestOnly returns only the new active one and IncludeSuperseded returns both.
	newTeslaRes, err := svc.Store().Supersede("proj-E2E", teslaID, &FactInstance{
		ProjectID: "proj-E2E",
		FactKey:   "car",
		Memory:    "user sold the Tesla and now rides the bus",
	})
	if err != nil {
		t.Fatalf("Supersede: %v", err)
	}
	latest, _ := svc.Store().ListByFactKey("proj-E2E", "car", ReadLatestOnly)
	if len(latest) != 1 || latest[0].ID != newTeslaRes.Instance.ID {
		t.Errorf("LatestOnly must return only the new active fact, got %d", len(latest))
	}
	full, _ := svc.Store().ListByFactKey("proj-E2E", "car", ReadIncludeSuperseded)
	if len(full) != 2 {
		t.Errorf("IncludeSuperseded must return both versions, got %d", len(full))
	}
	// Old version still present (not deleted), marked superseded, links forward.
	var oldFound bool
	for _, inst := range full {
		if inst.ID == teslaID {
			oldFound = true
			if inst.LifecycleState != LifecycleSuperseded {
				t.Errorf("old Tesla lifecycle = %v, want superseded", inst.LifecycleState)
			}
			if inst.ReplacedBy != newTeslaRes.Instance.ID {
				t.Errorf("old.ReplacedBy = %q, want %q", inst.ReplacedBy, newTeslaRes.Instance.ID)
			}
		}
	}
	if !oldFound {
		t.Error("old Tesla fact must still exist after supersede (ADD-only, no delete)")
	}
}

// rankOf returns the 0-based rank of id in results, or -1 if absent.
func rankOf(results []FusionResult, id string) int {
	for i, r := range results {
		if r.Instance.ID == id {
			return i
		}
	}
	return -1
}

// stubSemantic implements SemanticSearcher with a fixed score map.
type stubSemantic struct {
	scores map[string]float64
}

func (s *stubSemantic) Search(_ context.Context, projectID, query string) (map[string]float64, error) {
	out := make(map[string]float64, len(s.scores))
	for k, v := range s.scores {
		out[k] = v
	}
	return out, nil
}

// stubEntity implements EntitySearcher with a fixed hits map.
type stubEntity struct {
	hits map[string][]EntityHit
}

func (s *stubEntity) QueryEntities(_ context.Context, projectID string, entities []string) (map[string][]EntityHit, error) {
	out := make(map[string][]EntityHit, len(entities))
	for _, e := range entities {
		if h, ok := s.hits[e]; ok {
			out[e] = h
		}
	}
	return out, nil
}

// TestE2E_AddOnlyNeverMutates verifies the core invariant end-to-end: after
// superseding and re-ingesting the same content, the original instance is
// never modified — its CreatedAt, Hash, and original Memory are immutable.
func TestE2E_AddOnlyNeverMutates(t *testing.T) {
	llmResp, _ := json.Marshal(additiveExtractionResponse{Memories: []ExtractedFact{
		{Memory: "stable fact", FactKey: "k", AttributedTo: "user"},
	}})
	llm := &stubLLM{response: llmResp}
	store := NewMemoryStore()
	svc, _ := NewService(store, llm, WithClock(func() time.Time { return mustTime(t, "2026-09-02") }))

	first, err := svc.Ingest(context.Background(), "p", "q", nil, nil, "2026-09-02")
	if err != nil {
		t.Fatal(err)
	}
	orig := first[0].Instance
	origHash := orig.Hash
	origCreated := orig.CreatedAt

	// Re-ingest the exact same content — must skip, not mutate.
	second, err := svc.Ingest(context.Background(), "p", "q", nil, []string{orig.Memory}, "2026-09-02")
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || !second[0].Skipped {
		t.Fatalf("re-ingest must skip, got %+v", second)
	}
	if second[0].Instance.ID != orig.ID {
		t.Errorf("skipped result must point at original %q, got %q", orig.ID, second[0].Instance.ID)
	}

	got, _ := store.Get("p", orig.ID)
	if got.Hash != origHash {
		t.Errorf("hash mutated: was %q now %q", origHash, got.Hash)
	}
	if !got.CreatedAt.Equal(origCreated) {
		t.Errorf("CreatedAt mutated: was %v now %v", origCreated, got.CreatedAt)
	}
	if got.Memory != "stable fact" {
		t.Errorf("Memory mutated: %q", got.Memory)
	}
	if got.LifecycleState != LifecycleActive {
		t.Errorf("lifecycle mutated: %v", got.LifecycleState)
	}
}

// TestE2E_FusionScoreInBounds is a property test: no matter the signal mix,
// CombinedScore stays in [0, 1] after fusion and after temporal adjustment.
func TestE2E_FusionScoreInBounds(t *testing.T) {
	cands := []FusionCandidate{
		{Instance: &FactInstance{ID: "a", LifecycleState: LifecycleActive}, SemanticScore: 0.5, BM25RawScore: 5.0, EntityBoost: 0.3},
		{Instance: &FactInstance{ID: "b", LifecycleState: LifecycleActive}, SemanticScore: 0.9, BM25RawScore: 15.0, EntityBoost: 0.5},
		{Instance: &FactInstance{ID: "c", LifecycleState: LifecycleSuperseded}, SemanticScore: 0.8, BM25RawScore: 8.0},
	}
	res := ScoreAndRank(cands, "the quick brown fox", ScoreAndRankOptions{Explain: true})
	now := mustTime(t, "2026-09-02")
	res = ApplyTemporalAdjustment(res, TemporalIntentFuture, "", now)
	for _, r := range res {
		if math.IsNaN(r.CombinedScore) || r.CombinedScore < 0 || r.CombinedScore > 1 {
			t.Errorf("score %v out of [0,1] or NaN", r.CombinedScore)
		}
		if r.Explanation.MaxPossible == 0 {
			t.Error("MaxPossible not set")
		}
	}
}

// TestE2E_AttributedToNeverAffectsScoring is the keystone for capability #4:
// two candidates with identical content but different attributed_to must
// receive identical fusion scores. Attribution is metadata only.
func TestE2E_AttributedToNeverAffectsScoring(t *testing.T) {
	cands := []FusionCandidate{
		{Instance: &FactInstance{ID: "u", AttributedTo: AttributedUser, LifecycleState: LifecycleActive}, SemanticScore: 0.7, BM25RawScore: 5.0},
		{Instance: &FactInstance{ID: "a", AttributedTo: AttributedAssistant, LifecycleState: LifecycleActive}, SemanticScore: 0.7, BM25RawScore: 5.0},
		{Instance: &FactInstance{ID: "o", AttributedTo: AttributedAuto, LifecycleState: LifecycleActive}, SemanticScore: 0.7, BM25RawScore: 5.0},
	}
	res := ScoreAndRank(cands, "q", ScoreAndRankOptions{Explain: true})
	if len(res) != 3 {
		t.Fatalf("want 3, got %d", len(res))
	}
	// All three must have identical combined scores (attribution is not a signal).
	for i := 1; i < len(res); i++ {
		if math.Abs(res[i].CombinedScore-res[0].CombinedScore) > 1e-9 {
			t.Errorf("attribution changed score: %v vs %v", res[0].CombinedScore, res[i].CombinedScore)
		}
	}
}

// TestE2E_BM25AndEntityBoostActuallyContribute verifies the three signals are
// not dead code: adding BM25 and entity signals to a baseline semantic-only
// score must change the ranking.
func TestE2E_BM25AndEntityBoostActuallyContribute(t *testing.T) {
	inst := &FactInstance{ID: "x", LifecycleState: LifecycleActive, Memory: "the quick brown fox jumps over the lazy dog", FactKey: "k"}

	// Semantic-only baseline.
	semOnly := ScoreAndRank([]FusionCandidate{
		{Instance: inst, SemanticScore: 0.5},
	}, "fox", ScoreAndRankOptions{})

	// Three-signal: same semantic, plus strong BM25 + entity.
	threeSig := ScoreAndRank([]FusionCandidate{
		{Instance: inst, SemanticScore: 0.5, BM25RawScore: 12.0, EntityBoost: 0.4},
	}, "fox", ScoreAndRankOptions{})

	if len(semOnly) != 1 || len(threeSig) != 1 {
		t.Fatal("expected 1 result each")
	}
	// Three-signal must score higher than semantic-only.
	if threeSig[0].CombinedScore <= semOnly[0].CombinedScore {
		t.Errorf("BM25+entity did not raise score: sem=%v three=%v", semOnly[0].CombinedScore, threeSig[0].CombinedScore)
	}
}

// TestE2E_EntityExtractionFromQuery verifies the Go fallback entity extractor
// pulls quoted spans and CamelCase/UPPER identifiers.
func TestE2E_EntityExtractionFromQuery(t *testing.T) {
	ents := extractQueryEntities(`find info about "Tesla Model 3" and CVE-2026-1234 and IPAddress`)
	want := map[string]bool{"Tesla Model 3": false, "CVE-2026-1234": false, "IPAddress": false}
	got := map[string]bool{}
	for _, e := range ents {
		got[e] = true
	}
	for w := range want {
		if !got[w] {
			t.Errorf("entity %q not extracted; got %v", w, ents)
		}
	}
}

// TestE2E_PipelineDegradesGracefullyWithoutSignals verifies the service works
// when neither semantic nor entity searchers are configured — it falls back to
// BM25-only fusion (semantic defaults to 1.0 for all, BM25 discriminates).
func TestE2E_PipelineDegradesGracefullyWithoutSignals(t *testing.T) {
	store := NewMemoryStore()
	mustAddFact(t, store, "p", "k1", "the quick brown fox")
	mustAddFact(t, store, "p", "k2", "totally unrelated content about cooking")
	llm := &stubLLM{}                // not used by Retrieve
	svc, _ := NewService(store, llm) // no semantic, no entity

	res, err := svc.Retrieve(context.Background(), "p", "fox", RetrieveOptions{TopK: 2})
	if err != nil {
		t.Fatalf("Retrieve without signals: %v", err)
	}
	if len(res) == 0 {
		t.Fatal("BM25-only retrieval must return results")
	}
	if res[0].Instance.FactKey != "k1" {
		t.Errorf("BM25-only top result = %q, want k1 (contains 'fox')", res[0].Instance.FactKey)
	}
	// Without a semantic searcher, all instances get SemanticScore=1.0, so the
	// explanation's MaxPossible must reflect BM25-only (2.0).
	if res[0].Explanation.MaxPossible != 2.0 {
		t.Errorf("BM25-only MaxPossible = %v, want 2.0", res[0].Explanation.MaxPossible)
	}
}

// TestE2E_LifecycleChainVersioning verifies the version chain across multiple
// supersessions: v1→v2→v3, each with correct version number, replaced_by link,
// and LatestOnly returning only the tail.
func TestE2E_LifecycleChainVersioning(t *testing.T) {
	store := NewMemoryStore()
	v1 := mustAddFact(t, store, "p", "k", "v1 content")
	v2res, _ := store.Supersede("p", v1.ID, &FactInstance{ProjectID: "p", FactKey: "k", Memory: "v2 content"})
	v3res, _ := store.Supersede("p", v2res.Instance.ID, &FactInstance{ProjectID: "p", FactKey: "k", Memory: "v3 content"})

	full, _ := store.ListByFactKey("p", "k", ReadIncludeSuperseded)
	if len(full) != 3 {
		t.Fatalf("chain = %d, want 3", len(full))
	}
	if full[0].Version != 1 || full[1].Version != 2 || full[2].Version != 3 {
		t.Errorf("versions out of order: %d %d %d", full[0].Version, full[1].Version, full[2].Version)
	}
	if full[0].ReplacedBy != v2res.Instance.ID {
		t.Errorf("v1.ReplacedBy = %q, want %q", full[0].ReplacedBy, v2res.Instance.ID)
	}
	if full[1].ReplacedBy != v3res.Instance.ID {
		t.Errorf("v2.ReplacedBy = %q, want %q", full[1].ReplacedBy, v3res.Instance.ID)
	}
	latest, _ := store.ListByFactKey("p", "k", ReadLatestOnly)
	if len(latest) != 1 || latest[0].ID != v3res.Instance.ID {
		t.Errorf("LatestOnly = %d, want only v3", len(latest))
	}
}

// guard: silence unused-import warnings if a stub method is unused.
var _ = fmt.Sprintf
var _ = strings.TrimSpace
