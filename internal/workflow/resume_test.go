package workflow

import (
	"context"
	"strings"
	"testing"
	"time"

	"cyberstrike-ai/internal/database"

	"github.com/cloudwego/eino/compose"
)

func startHitlOutputGraph() string {
	return `{
  "nodes": [
    {"id": "start-1", "type": "start", "label": "开始", "position": {"x": 0, "y": 0}, "config": {}},
    {"id": "hitl-1", "type": "hitl", "label": "审批", "position": {"x": 0, "y": 80}, "config": {"prompt": "允许执行吗？"}},
    {"id": "out-1", "type": "output", "label": "输出", "position": {"x": 0, "y": 160}, "config": {"output_key": "result", "source_binding": {"from": "previous", "field": "output"}}}
  ],
  "edges": [
    {"id": "e1", "source": "start-1", "target": "hitl-1"},
    {"id": "e2", "source": "hitl-1", "target": "out-1"}
  ],
  "config": {"schema_version": 1}
}`
}

// TestExtractAwaitingHITL_endToEndInterrupt drives the full Eino interrupt path:
// the run pauses before the hitl node, extractAwaitingHITL records the pending
// decision in the DB, and the error surfaces as AwaitingHITLError.
func TestExtractAwaitingHITL_endToEndInterrupt(t *testing.T) {
	db := hitlTestDB(t)
	ctx := context.Background()
	SetCheckpointDir(t.TempDir())
	createTestWorkflowRun(t, db, "run-extract")
	g, err := parseGraph(startHitlOutputGraph())
	if err != nil {
		t.Fatal(err)
	}
	state := newWorkflowLocalState(map[string]interface{}{"message": "ping"}, "run-extract")
	args := RunArgs{DB: db}
	err = executeEinoGraph(ctx, args, "run-extract", "wf-extract", 1, g, state)
	if !IsAwaitingHITL(err) {
		t.Fatalf("executeEinoGraph err = %v, want AwaitingHITLError", err)
	}
	hitl, ok := err.(*AwaitingHITLError)
	if !ok {
		t.Fatalf("err type = %T, want *AwaitingHITLError", err)
	}
	if hitl.NodeID != "hitl-1" {
		t.Fatalf("hitl node = %q, want hitl-1", hitl.NodeID)
	}
	if hitl.NodeLabel == "" {
		t.Fatal("hitl node label should not be empty")
	}
	run, err := db.GetWorkflowRun("run-extract")
	if err != nil {
		t.Fatal(err)
	}
	if run == nil || run.Status != "awaiting_hitl" || run.PendingHITLNodeID != "hitl-1" {
		t.Fatalf("run not seeded awaiting: %+v", run)
	}
	if strings.TrimSpace(run.PendingHITLJSON) == "" {
		t.Fatal("pending_hitl_json should be populated")
	}
}

func TestExtractAwaitingHITL_noInterruptReturnsNil(t *testing.T) {
	err := extractAwaitingHITL(nil, &compiledArtifact{hitlIDs: []string{"hitl-1"}}, "run-x", RunArgs{}, &WorkflowLocalState{})
	if err != nil {
		t.Fatalf("extractAwaitingHITL(nil err) = %v, want nil", err)
	}
}

func TestNextHITLNodeID_prioritizesBeforeNodes(t *testing.T) {
	info := &compose.InterruptInfo{BeforeNodes: []string{"hitl-a", "hitl-b"}}
	if got := nextHITLNodeID(info, []string{"hitl-a", "hitl-b"}); got != "hitl-a" {
		t.Fatalf("nextHITLNodeID = %q, want hitl-a", got)
	}
	if got := nextHITLNodeID(info, []string{"hitl-b"}); got != "hitl-b" {
		t.Fatalf("nextHITLNodeID with only hitl-b = %q, want hitl-b", got)
	}
}

func TestNextHITLNodeID_fallsBackToFirstHitl(t *testing.T) {
	if got := nextHITLNodeID(nil, []string{"hitl-1", "hitl-2"}); got != "hitl-1" {
		t.Fatalf("nextHITLNodeID(nil) = %q, want hitl-1", got)
	}
	if got := nextHITLNodeID(nil, nil); got != "" {
		t.Fatalf("nextHITLNodeID(nil, nil) = %q, want empty", got)
	}
}

func TestWorkflowInterruptMetadata_preservesBeforeNodes(t *testing.T) {
	meta := workflowInterruptMetadata(&compose.InterruptInfo{BeforeNodes: []string{"hitl-1"}})
	if len(meta) == 0 {
		t.Fatal("metadata should not be empty")
	}
	if arr, _ := meta["beforeNodes"].([]string); len(arr) != 1 || arr[0] != "hitl-1" {
		t.Fatalf("beforeNodes = %#v, want [hitl-1]", meta["beforeNodes"])
	}
	if meta["resumeTarget"] != "hitl-1" {
		t.Fatalf("resumeTarget = %v, want hitl-1", meta["resumeTarget"])
	}
}

func TestWorkflowInterruptMetadata_nilIsEmpty(t *testing.T) {
	if m := workflowInterruptMetadata(nil); len(m) != 0 {
		t.Fatalf("metadata(nil) = %#v, want empty", m)
	}
}

func upsertResumeWorkflow(t *testing.T, db *database.DB, id, graph string) {
	t.Helper()
	if err := db.UpsertWorkflowDefinition(&database.WorkflowDefinition{
		ID:        id,
		Name:      "审批流程",
		Version:   1,
		GraphJSON: graph,
		Enabled:   true,
	}); err != nil {
		t.Fatalf("UpsertWorkflowDefinition: %v", err)
	}
}

// createResumeWorkflowRun 用指定 workflowID 建 run（eino_compile_test.go 的 helper 绑死 test-wf，不能复用）。
func createResumeWorkflowRun(t *testing.T, db *database.DB, runID, workflowID string) {
	t.Helper()
	if err := db.CreateWorkflowRun(&database.WorkflowRun{
		ID:         runID,
		WorkflowID: workflowID,
		Status:     "running",
	}); err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
}

// TestResumeWorkflowRun_approved pauses at HITL, then approves and completes.
func TestResumeWorkflowRun_approved(t *testing.T) {
	db := hitlTestDB(t)
	ctx := context.Background()
	SetCheckpointDir(t.TempDir())
	upsertResumeWorkflow(t, db, "wf-resume-ok", startHitlOutputGraph())
	createResumeWorkflowRun(t, db, "run-resume-ok", "wf-resume-ok")

	g, err := parseGraph(startHitlOutputGraph())
	if err != nil {
		t.Fatal(err)
	}
	state := newWorkflowLocalState(map[string]interface{}{"message": "ack"}, "run-resume-ok")
	args := RunArgs{DB: db}
	if err := executeEinoGraph(ctx, args, "run-resume-ok", "wf-resume-ok", 1, g, state); !IsAwaitingHITL(err) {
		t.Fatalf("expected HITL pause, got %v", err)
	}

	result, err := ResumeWorkflowRun(ctx, args, "run-resume-ok", true, "批准执行")
	if err != nil {
		t.Fatalf("ResumeWorkflowRun approved: %v", err)
	}
	if result == nil || result.Status != "completed" {
		t.Fatalf("resume result = %+v, want status completed", result)
	}
	run, err := db.GetWorkflowRun("run-resume-ok")
	if err != nil {
		t.Fatal(err)
	}
	if run == nil || run.Status != "completed" {
		t.Fatalf("run status = %v, want completed", run.Status)
	}
	if strings.TrimSpace(run.OutputJSON) == "" {
		t.Fatal("output_json should be populated on completion")
	}
}

// TestResumeWorkflowRun_rejected pauses at HITL, then rejects and terminates.
func TestResumeWorkflowRun_rejected(t *testing.T) {
	db := hitlTestDB(t)
	ctx := context.Background()
	SetCheckpointDir(t.TempDir())
	upsertResumeWorkflow(t, db, "wf-resume-no", startHitlOutputGraph())
	createResumeWorkflowRun(t, db, "run-resume-no", "wf-resume-no")

	g, err := parseGraph(startHitlOutputGraph())
	if err != nil {
		t.Fatal(err)
	}
	state := newWorkflowLocalState(map[string]interface{}{"message": "ack"}, "run-resume-no")
	args := RunArgs{DB: db}
	if err := executeEinoGraph(ctx, args, "run-resume-no", "wf-resume-no", 1, g, state); !IsAwaitingHITL(err) {
		t.Fatalf("expected HITL pause, got %v", err)
	}

	result, err := ResumeWorkflowRun(ctx, args, "run-resume-no", false, "风险过高，拒绝")
	if err != nil {
		t.Fatalf("ResumeWorkflowRun rejected: %v", err)
	}
	if result == nil || result.Status != "rejected" {
		t.Fatalf("resume result = %+v, want status rejected", result)
	}
	run, err := db.GetWorkflowRun("run-resume-no")
	if err != nil {
		t.Fatal(err)
	}
	if run == nil || run.Status != "rejected" {
		t.Fatalf("run status = %v, want rejected", run.Status)
	}
	if !strings.Contains(run.Error, "拒绝") {
		t.Fatalf("run.error = %q, want contains '拒绝'", run.Error)
	}
}

// TestResumeWorkflowRun_notAwaitingRejects verifies non-awaiting runs are refused.
func TestResumeWorkflowRun_notAwaitingRejects(t *testing.T) {
	db := hitlTestDB(t)
	ctx := context.Background()
	const runID = "run-not-awaiting"
	if err := db.CreateWorkflowRun(&database.WorkflowRun{
		ID: runID, WorkflowID: "wf-x", WorkflowVersion: 1, Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	_, err := ResumeWorkflowRun(ctx, RunArgs{DB: db}, runID, true, "")
	if err == nil {
		t.Fatal("expected error for non-awaiting run")
	}
	if !strings.Contains(err.Error(), "不在等待审批状态") {
		t.Fatalf("err = %v, want contains '不在等待审批状态'", err.Error())
	}
}
