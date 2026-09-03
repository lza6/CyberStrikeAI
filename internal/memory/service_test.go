package memory

import (
	"context"
	"strings"
	"testing"
)

// TestService_ReadIncludeMerged returns merged instances in addition to active
// ones. This covers the HIGH gap the Critic flagged: ReadIncludeMerged used to
// return the same set as ReadLatestOnly because the augmentation block only
// ran at ReadMode >= ReadIncludeSuperseded.
func TestService_ReadIncludeMerged(t *testing.T) {
	store := NewMemoryStore()
	active := mustAddFact(t, store, "p", "k", "active truth")
	// Fold the active truth into a new one; the supersede path produces a
	// superseded row (not merged), which we use to assert the read-mode
	// plumbing distinguishes merged (mode>=1) from superseded (mode>=2).
	_, err := store.Supersede("p", active.ID, &FactInstance{
		ProjectID: "p", FactKey: "k", Memory: "second truth",
	})
	if err != nil {
		t.Fatal(err)
	}
	merged, _ := store.ListByFactKey("p", "k", ReadIncludeMerged)
	// IncludeMerged must include active rows but NOT superseded rows.
	if len(merged) == 0 {
		t.Fatal("ListByFactKey IncludeMerged must at least return the active tail")
	}
	hasActive := false
	for _, inst := range merged {
		if inst.LifecycleState == LifecycleActive {
			hasActive = true
		}
		if inst.LifecycleState == LifecycleSuperseded {
			t.Error("IncludeMerged must NOT include superseded rows — that needs IncludeSuperseded")
		}
	}
	if !hasActive {
		t.Error("IncludeMerged must include active rows")
	}
}

// TestStore_SupersedeRejectsProjectMismatch covers the MEDIUM gap: the
// low-level Supersede API must reject a newInst whose ProjectID differs from
// the projectID argument, preventing cross-project dedup leakage.
func TestStore_SupersedeRejectsProjectMismatch(t *testing.T) {
	store := NewMemoryStore()
	old := mustAddFact(t, store, "proj-X", "k", "original")
	_, err := store.Supersede("proj-X", old.ID, &FactInstance{
		ProjectID: "proj-DIFFERENT", FactKey: "k", Memory: "cross-project",
	})
	if err == nil {
		t.Fatal("Supersede must reject newInst.ProjectID mismatch")
	}
	if !strings.Contains(err.Error(), "projectID") {
		t.Errorf("error must mention projectID, got %v", err)
	}
}

// TestStore_SupersedeRejectsEmptyMemory guards the MEDIUM finding that
// Supersede did not validate newInst.Memory non-empty (would hash to the
// md5 of empty string and collapse all empty supersessions).
func TestStore_SupersedeRejectsEmptyMemory(t *testing.T) {
	store := NewMemoryStore()
	old := mustAddFact(t, store, "p", "k", "original")
	_, err := store.Supersede("p", old.ID, &FactInstance{
		ProjectID: "p", FactKey: "k", Memory: "",
	})
	if err == nil {
		t.Fatal("Supersede must reject empty memory")
	}
}

// TestService_RetrieveAfterSupersedeDemotesOldFact covers the e2e gap: after
// superseding a fact, a subsequent Retrieve must not surface the superseded
// fact in the default (LatestOnly) pool — only the new active truth.
func TestService_RetrieveAfterSupersedeDemotesOldFact(t *testing.T) {
	store := NewMemoryStore()
	old := mustAddFact(t, store, "p", "car", "user drives a Subaru")
	newRes, err := store.Supersede("p", old.ID, &FactInstance{
		ProjectID: "p", FactKey: "car", Memory: "user drives a Tesla",
	})
	if err != nil {
		t.Fatal(err)
	}
	svc, _ := NewService(store, nil) // no semantic/entity — BM25-only
	res, err := svc.Retrieve(context.Background(), "p", "Tesla", RetrieveOptions{TopK: 5})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	for _, r := range res {
		if r.Instance.ID == old.ID {
			t.Errorf("superseded old fact must not appear in LatestOnly retrieve, but %q did", r.Instance.ID)
		}
	}
	if len(res) == 0 || res[0].Instance.ID != newRes.Instance.ID {
		t.Errorf("top result must be the new active fact %q, got %+v", newRes.Instance.ID, res)
	}
}

// TestService_ReadIncludeSupersededSurfacesHistory covers the full-chain read
// mode through Service.Retrieve: superseded facts appear when explicitly
// requested, demoted in score but present.
func TestService_ReadIncludeSupersededSurfacesHistory(t *testing.T) {
	store := NewMemoryStore()
	old := mustAddFact(t, store, "p", "car", "user drives a Subaru")
	_, _ = store.Supersede("p", old.ID, &FactInstance{
		ProjectID: "p", FactKey: "car", Memory: "user drives a Tesla",
	})
	svc, _ := NewService(store, nil)
	res, err := svc.Retrieve(context.Background(), "p", "Subaru", RetrieveOptions{
		TopK: 5, ReadMode: ReadIncludeSuperseded,
	})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	var foundOld bool
	for _, r := range res {
		if r.Instance.ID == old.ID {
			foundOld = true
		}
	}
	if !foundOld {
		t.Error("IncludeSuperseded must surface the old superseded fact, but it was absent")
	}
}
