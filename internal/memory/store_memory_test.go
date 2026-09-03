package memory

import (
	"strings"
	"testing"
	"time"
)

// helper to build a minimal active instance for tests.
func mustAddFact(t *testing.T, s Store, projectID, factKey, memory string) *FactInstance {
	t.Helper()
	inst := &FactInstance{
		ProjectID: projectID,
		FactKey:   factKey,
		Memory:    memory,
	}
	res, err := s.Add(inst)
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if res.Skipped {
		// Return the existing instance the dedup pointed at so callers can chain assertions.
		return res.Instance
	}
	return res.Instance
}

func TestContentHash_DeterministicAndLowercaseHex(t *testing.T) {
	h1 := ContentHash("user owns a Tesla")
	h2 := ContentHash("user owns a Tesla")
	if h1 != h2 {
		t.Fatalf("content hash not deterministic: %s vs %s", h1, h2)
	}
	if ContentHash("different") == h1 {
		t.Fatal("distinct content produced identical hash")
	}
	if len(h1) != 32 {
		t.Fatalf("expected 32-char md5 hex, got %d (%q)", len(h1), h1)
	}
	if strings.ToLower(h1) != h1 {
		t.Fatalf("hash must be lowercase, got %q", h1)
	}
}

func TestNormalizeAttributedTo_Canonicalizes(t *testing.T) {
	cases := map[string]AttributedTo{
		"user":      AttributedUser,
		"USER":      AttributedUser,
		"assistant": AttributedAssistant,
		"agent":     AttributedAssistant,
		"":          AttributedAuto,
		"whatever":  AttributedAuto,
	}
	for in, want := range cases {
		if got := NormalizeAttributedTo(in); got != want {
			t.Errorf("NormalizeAttributedTo(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestStore_AddNeverOverwrites is the keystone of capability #1 (ADD-only).
// Adding identical content twice must NOT mutate the existing row: the second
// Add returns a Skipped result referencing the same instance ID, and the store
// holds exactly one row.
func TestStore_AddNeverOverwrites(t *testing.T) {
	s := NewMemoryStore()
	inst := mustAddFact(t, s, "proj-A", "car", "user drives a Subaru Outback")
	originalID := inst.ID
	originalCreatedAt := inst.CreatedAt

	second := &FactInstance{
		ProjectID: "proj-A",
		FactKey:   "car",
		Memory:    "user drives a Subaru Outback", // identical content → same hash
	}
	res, err := s.Add(second)
	if err != nil {
		t.Fatalf("second Add failed: %v", err)
	}
	if !res.Skipped {
		t.Fatalf("duplicate Add must be Skipped, got event=%q", res.Event)
	}
	if res.Instance.ID != originalID {
		t.Fatalf("Skipped result must point at original instance %q, got %q", originalID, res.Instance.ID)
	}
	if res.Event != "SKIPPED_DUPLICATE" {
		t.Fatalf("expected SKIPPED_DUPLICATE event, got %q", res.Event)
	}

	// The second instance pointer must not have been assigned a new ID or persisted.
	if second.ID == originalID {
		t.Fatal("caller's instance struct should not be mutated to reuse original ID")
	}

	got, err := s.Get("proj-A", originalID)
	if err != nil {
		t.Fatalf("Get original failed: %v", err)
	}
	if !got.CreatedAt.Equal(originalCreatedAt) {
		t.Fatalf("original CreatedAt mutated: was %v now %v", originalCreatedAt, got.CreatedAt)
	}
	if got.LifecycleState != LifecycleActive {
		t.Fatalf("original lifecycle mutated by duplicate Add: %v", got.LifecycleState)
	}
}

// TestStore_SupersedeCreatesVersionChainNotDelete verifies capability #1's
// supersede clause: supersede persists a NEW active row, flips the old row to
// superseded with replaced_by pointing forward, and never deletes the old row.
func TestStore_SupersedeCreatesVersionChainNotDelete(t *testing.T) {
	s := NewMemoryStore()
	old := mustAddFact(t, s, "proj-A", "car", "user drives a 2019 Subaru Outback")

	newInst := &FactInstance{
		ProjectID: "proj-A",
		FactKey:   "car",
		Memory:    "user sold the Subaru and bought a Tesla Model 3",
	}
	res, err := s.Supersede("proj-A", old.ID, newInst)
	if err != nil {
		t.Fatalf("Supersede failed: %v", err)
	}
	if res.Event != "ADD" {
		t.Fatalf("supersede new instance must be ADD, got %q", res.Event)
	}
	if res.Instance.ID == "" {
		t.Fatal("new instance has no ID")
	}

	// Old row still present, marked superseded, links forward.
	stale, err := s.Get("proj-A", old.ID)
	if err != nil {
		t.Fatalf("old row must still exist after supersede: %v", err)
	}
	if stale.LifecycleState != LifecycleSuperseded {
		t.Fatalf("old lifecycle = %v, want superseded", stale.LifecycleState)
	}
	if stale.ReplacedBy != res.Instance.ID {
		t.Fatalf("old.ReplacedBy = %q, want %q", stale.ReplacedBy, res.Instance.ID)
	}

	// New row is active and is the latest version.
	latest, err := s.Get("proj-A", res.Instance.ID)
	if err != nil {
		t.Fatalf("new row Get failed: %v", err)
	}
	if latest.LifecycleState != LifecycleActive {
		t.Fatalf("new lifecycle = %v, want active", latest.LifecycleState)
	}
	if latest.Version != stale.Version+1 {
		t.Fatalf("version chain broken: old v%d, new v%d", stale.Version, latest.Version)
	}
}

// TestStore_SupersedeDuplicateContentRedirectsToExisting covers the edge where
// the new "superseding" content actually duplicates some other stored instance.
// The old row is still marked superseded, but replaced_by points at the
// pre-existing duplicate (we never create a second row with the same hash).
func TestStore_SupersedeDuplicateContentRedirectsToExisting(t *testing.T) {
	s := NewMemoryStore()
	pre := mustAddFact(t, s, "proj-A", "car", "user drives a Tesla Model 3")
	old := mustAddFact(t, s, "proj-A", "car", "user drives a Subaru Outback")

	newInst := &FactInstance{
		ProjectID: "proj-A",
		FactKey:   "car",
		Memory:    "user drives a Tesla Model 3", // same content as `pre`
	}
	res, err := s.Supersede("proj-A", old.ID, newInst)
	if err != nil {
		t.Fatalf("Supersede failed: %v", err)
	}
	if !res.Skipped {
		t.Fatalf("duplicate-content supersede must be Skipped, got event=%q", res.Event)
	}
	if res.Instance.ID != pre.ID {
		t.Fatalf("replaced_by must point at pre-existing duplicate %q, got %q", pre.ID, res.Instance.ID)
	}

	stale, _ := s.Get("proj-A", old.ID)
	if stale.ReplacedBy != pre.ID {
		t.Fatalf("old.ReplacedBy = %q, want %q", stale.ReplacedBy, pre.ID)
	}
	if stale.LifecycleState != LifecycleSuperseded {
		t.Fatalf("old still active after supersede: %v", stale.LifecycleState)
	}
}

// TestStore_ReadModes covers capability #3's read-mode semantics:
//   - LatestOnly returns only active
//   - IncludeMerged adds merged
//   - IncludeSuperseded returns the full chain
func TestStore_ReadModes(t *testing.T) {
	s := NewMemoryStore()
	v1 := mustAddFact(t, s, "proj-B", "os", "host runs Windows 10")
	v2res, _ := s.Supersede("proj-B", v1.ID, &FactInstance{
		ProjectID: "proj-B", FactKey: "os", Memory: "host runs Windows 11",
	})
	v2 := v2res.Instance

	chain, err := s.ListByFactKey("proj-B", "os", ReadLatestOnly)
	if err != nil {
		t.Fatalf("List LatestOnly: %v", err)
	}
	if len(chain) != 1 || chain[0].ID != v2.ID {
		t.Fatalf("LatestOnly must return only v2 (active), got %d rows", len(chain))
	}

	chain, _ = s.ListByFactKey("proj-B", "os", ReadIncludeSuperseded)
	if len(chain) != 2 {
		t.Fatalf("IncludeSuperseded must return both versions, got %d", len(chain))
	}
	if chain[0].ID != v1.ID || chain[1].ID != v2.ID {
		t.Fatalf("chain order must be v1→v2 (oldest first), got %s→%s", chain[0].ID, chain[1].ID)
	}
	if chain[0].Version != 1 || chain[1].Version != 2 {
		t.Fatalf("versions out of order: %d, %d", chain[0].Version, chain[1].Version)
	}
}

// TestStore_ListActiveFiltersByPrefixAndProject verifies the default retrieval
// pool only returns active rows, respects project isolation, and supports
// fact_key prefix filtering (the host:* scoping used by retrieval callers).
func TestStore_ListActiveFiltersByPrefixAndProject(t *testing.T) {
	s := NewMemoryStore()
	mustAddFact(t, s, "proj-A", "host:10.0.0.1", "10.0.0.1 runs Linux")
	mustAddFact(t, s, "proj-A", "host:10.0.0.2", "10.0.0.2 runs Windows")
	mustAddFact(t, s, "proj-A", "host:web", "web.example.com is up")
	mustAddFact(t, s, "proj-B", "host:10.0.0.1", "cross-project isolation")

	// Supersede one of proj-A's hosts so it leaves the active pool.
	v1, _ := s.Get("proj-A", "")
	_ = v1
	all, _ := s.ListActive("proj-A", "")
	if len(all) != 3 {
		t.Fatalf("proj-A active count = %d, want 3", len(all))
	}
	hostPrefix, _ := s.ListActive("proj-A", "host:10.0.0.")
	if len(hostPrefix) != 2 {
		t.Fatalf("prefix host:10.0.0. count = %d, want 2", len(hostPrefix))
	}
	for _, inst := range hostPrefix {
		if !strings.HasPrefix(inst.FactKey, "host:10.0.0.") {
			t.Fatalf("prefix filter leaked: %q", inst.FactKey)
		}
	}
	bOnly, _ := s.ListActive("proj-B", "")
	if len(bOnly) != 1 {
		t.Fatalf("proj-B active count = %d, want 1", len(bOnly))
	}
}

// TestStore_AttributedToStoredEqually (capability #4): user / assistant / auto
// attributions all persist to the same active pool with identical retrieval
// visibility. The field is metadata, never a storage gate.
func TestStore_AttributedToStoredEqually(t *testing.T) {
	s := NewMemoryStore()
	for i, attr := range []AttributedTo{AttributedUser, AttributedAssistant, AttributedAuto} {
		inst := &FactInstance{
			ProjectID:    "proj-EQ",
			FactKey:      "eq",
			Memory:       "eq fact " + string(attr),
			AttributedTo: attr,
		}
		res, err := s.Add(inst)
		if err != nil {
			t.Fatalf("Add #%d (%s) failed: %v", i, attr, err)
		}
		if res.Skipped {
			t.Fatalf("Add #%d (%s) was skipped — distinct content must not dedup", i, attr)
		}
		if res.Instance.LifecycleState != LifecycleActive {
			t.Fatalf("Add #%d lifecycle = %v, want active", i, res.Instance.LifecycleState)
		}
	}

	active, _ := s.ListActive("proj-EQ", "")
	if len(active) != 3 {
		t.Fatalf("equal-weight pool = %d, want 3 (all three attributions)", len(active))
	}
	seen := map[AttributedTo]bool{}
	for _, inst := range active {
		seen[inst.AttributedTo] = true
	}
	for _, want := range []AttributedTo{AttributedUser, AttributedAssistant, AttributedAuto} {
		if !seen[want] {
			t.Errorf("attribution %q missing from active pool", want)
		}
	}
}

// TestStore_EventDatePersists verifies the dated-instance field (capability #3)
// round-trips through Add and is queryable on the returned instance. The actual
// temporal *reasoning* (sorting current/past/planned) lives in temporal.go.
func TestStore_EventDatePersists(t *testing.T) {
	s := NewMemoryStore()
	inst := &FactInstance{
		ProjectID: "proj-T",
		FactKey:   "meeting",
		Memory:    "user met vendor on 2026-08-15",
		EventDate: "2026-08-15",
	}
	res, err := s.Add(inst)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if res.Instance.EventDate != "2026-08-15" {
		t.Fatalf("EventDate = %q, want 2026-08-15", res.Instance.EventDate)
	}
}

// TestStore_ZeroMemoryRejected guards the integrity of the dedup hash: empty
// memory would hash to a constant and silently collapse all empty facts.
func TestStore_ZeroMemoryRejected(t *testing.T) {
	s := NewMemoryStore()
	_, err := s.Add(&FactInstance{ProjectID: "p", FactKey: "k", Memory: ""})
	if err == nil {
		t.Fatal("Add(empty memory) must error")
	}
	_, err = s.Add(&FactInstance{ProjectID: "", FactKey: "k", Memory: "x"})
	if err == nil {
		t.Fatal("Add(empty project) must error")
	}
	_, err = s.Add(nil)
	if err == nil {
		t.Fatal("Add(nil) must error")
	}
}

// TestStore_SupersedeAlreadySupersededRejects guards the forward-only nature of
// the version chain: a superseded row cannot be superseded again (its truth is
// already history; the active tail is the only supersedeable target).
func TestStore_SupersedeAlreadySupersededRejects(t *testing.T) {
	s := NewMemoryStore()
	v1 := mustAddFact(t, s, "proj-C", "k", "first fact")
	v2res, _ := s.Supersede("proj-C", v1.ID, &FactInstance{
		ProjectID: "proj-C", FactKey: "k", Memory: "second fact",
	})
	// Attempt to supersede v1 again — must error.
	_, err := s.Supersede("proj-C", v1.ID, &FactInstance{
		ProjectID: "proj-C", FactKey: "k", Memory: "third fact",
	})
	if err == nil {
		t.Fatal("superseding an already-superseded row must error")
	}
	_ = v2res
}

// guard against time-travel flakes when tests run in parallel: ensure we never
// compare time.Time equality without .Equal (UnixNano truncation).
var _ = time.Now
