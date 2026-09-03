package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"cyberstrike-ai/internal/database"
	"go.uber.org/zap"
)

// skipIfNoCGO works around the go-sqlite3 stub when CGO is unavailable.
// go-sqlite3 requires cgo; without it, NewDB returns "Binary was compiled
// with 'CGO_ENABLED=0'" and any DB-backed test is meaningless. This helper
// keeps the test file valid in both CGO and non-CGO environments.
func skipIfNoCGO(t *testing.T, db *database.DB, err error) {
	t.Helper()
	if err != nil || db == nil {
		if err != nil && strings.Contains(err.Error(), "CGO_ENABLED") {
			t.Skipf("跳过：go-sqlite3 需要 cgo，当前环境无 C 编译器: %v", err)
		}
		if err != nil {
			t.Fatalf("NewDB: %v", err)
		}
	}
}

// hitlTestDB mirrors testWorkflowDB but centralizes the skip behavior.
func hitlTestDB(t *testing.T) *database.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := database.NewDB(dir+"hitl.db", zap.NewNop())
	skipIfNoCGO(t, db, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func seedWorkflowRunAwaiting(t *testing.T, db *database.DB, runID, nodeID string, pending map[string]any) {
	t.Helper()
	if err := db.CreateWorkflowRun(&database.WorkflowRun{
		ID:              runID,
		WorkflowID:      "wf-hitl",
		WorkflowVersion: 1,
		Status:          "awaiting_hitl",
		StartedAt:       time.Now(),
	}); err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	raw, _ := json.Marshal(pending)
	if err := db.SetWorkflowRunAwaitingHITL(runID, nodeID, string(raw)); err != nil {
		t.Fatalf("SetWorkflowRunAwaitingHITL: %v", err)
	}
}

func TestNotifyHITLDecision_signalsActiveWaiter(t *testing.T) {
	const runID = "run-notify"
	ch := registerHITLWaiter(runID)
	defer unregisterHITLWaiter(runID, ch)

	if !NotifyHITLDecision(runID, HITLDecision{Approved: true, Comment: "ok"}) {
		t.Fatal("NotifyHITLDecision should signal an active waiter")
	}
	select {
	case d := <-ch:
		if !d.Approved || d.Comment != "ok" {
			t.Fatalf("received decision = %+v, want approved/ok", d)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter channel did not receive decision in time")
	}
}

func TestNotifyHITLDecision_returnsFalseWhenNoWaiter(t *testing.T) {
	if NotifyHITLDecision("run-nobody", HITLDecision{Approved: true}) {
		t.Fatal("NotifyHITLDecision should return false when no waiter registered")
	}
}

func TestReadHITLDecisionFromDB_approvedAndRejected(t *testing.T) {
	db := hitlTestDB(t)
	tests := []struct {
		name     string
		decision string
		comment  string
		approved bool
	}{
		{"approved", "approved", "looks good", true},
		{"approve alias", "approve", "", true},
		{"rejected", "rejected", "no go", false},
		{"reject alias", "reject", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runID := "run-" + tt.name
			seedWorkflowRunAwaiting(t, db, runID, "hitl-1", map[string]any{
				"nodeId":   "hitl-1",
				"decision": tt.decision,
				"comment":  tt.comment,
			})
			d, ok, err := readHITLDecisionFromDB(db, runID)
			if err != nil {
				t.Fatalf("readHITLDecisionFromDB: %v", err)
			}
			if !ok {
				t.Fatal("expected decision to be present")
			}
			if d.Approved != tt.approved {
				t.Fatalf("approved = %v, want %v", d.Approved, tt.approved)
			}
			if d.Comment != tt.comment {
				t.Fatalf("comment = %q, want %q", d.Comment, tt.comment)
			}
		})
	}
}

func TestReadHITLDecisionFromDB_emptyWhenNoDecision(t *testing.T) {
	db := hitlTestDB(t)
	const runID = "run-no-decision"
	seedWorkflowRunAwaiting(t, db, runID, "hitl-1", map[string]any{
		"nodeId": "hitl-1",
		// no decision key
	})
	d, ok, err := readHITLDecisionFromDB(db, runID)
	if err != nil {
		t.Fatalf("readHITLDecisionFromDB: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false when decision key missing")
	}
	if d.Approved || d.Comment != "" {
		t.Fatalf("expected zero decision, got %+v", d)
	}
}

func TestReadHITLDecisionFromDB_nilDBReturnsFalse(t *testing.T) {
	d, ok, err := readHITLDecisionFromDB(nil, "run-x")
	if err != nil {
		t.Fatalf("nil db should not return error: %v", err)
	}
	if ok {
		t.Fatal("nil db should return ok=false")
	}
	if d.Approved || d.Comment != "" {
		t.Fatalf("nil db should return zero decision, got %+v", d)
	}
}

func TestWaitWorkflowHITLDecisionWithChannel_returnsDBDecisionImmediately(t *testing.T) {
	db := hitlTestDB(t)
	const runID = "run-wait-db"
	seedWorkflowRunAwaiting(t, db, runID, "hitl-1", map[string]any{
		"nodeId":   "hitl-1",
		"decision": "approved",
		"comment":  "auto",
	})
	ch := make(chan HITLDecision, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	d, err := waitWorkflowHITLDecisionWithChannel(ctx, db, runID, ch)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if !d.Approved || d.Comment != "auto" {
		t.Fatalf("decision = %+v, want approved/auto", d)
	}
}

func TestWaitWorkflowHITLDecisionWithChannel_picksUpChannelDecision(t *testing.T) {
	db := hitlTestDB(t)
	const runID = "run-wait-ch"
	seedWorkflowRunAwaiting(t, db, runID, "hitl-1", map[string]any{"nodeId": "hitl-1"})
	ch := make(chan HITLDecision, 1)
	ch <- HITLDecision{Approved: false, Comment: "manual reject"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	d, err := waitWorkflowHITLDecisionWithChannel(ctx, db, runID, ch)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if d.Approved {
		t.Fatal("decision should be rejected")
	}
	if d.Comment != "manual reject" {
		t.Fatalf("comment = %q, want 'manual reject'", d.Comment)
	}
}

func TestWaitWorkflowHITLDecisionWithChannel_contextCancelled(t *testing.T) {
	db := hitlTestDB(t)
	const runID = "run-wait-cancel"
	seedWorkflowRunAwaiting(t, db, runID, "hitl-1", map[string]any{"nodeId": "hitl-1"})
	ch := make(chan HITLDecision, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := waitWorkflowHITLDecisionWithChannel(ctx, db, runID, ch)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if err != context.DeadlineExceeded {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
}
