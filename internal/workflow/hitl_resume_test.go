package workflow

import (
	"context"
	"strings"
	"testing"
	"time"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"

	"github.com/cloudwego/eino/compose"
	"go.uber.org/zap"
)

// hitlApprovalGraph is the minimal start→hitl→output graph used to drive the
// HITL interrupt/resume path end-to-end.
func hitlApprovalGraph() string {
	return `{
  "nodes": [
    {"id": "start-1", "type": "start", "label": "开始", "position": {"x": 0, "y": 0}, "config": {}},
    {"id": "hitl-1", "type": "hitl", "label": "人工确认", "position": {"x": 0, "y": 80}, "config": {"prompt": "确认执行？", "reviewer": "reviewer-1"}},
    {"id": "out-1", "type": "output", "label": "输出", "position": {"x": 0, "y": 160}, "config": {"output_key": "result", "source_binding": {"from": "inputs", "field": "message"}}}
  ],
  "edges": [
    {"id": "e1", "source": "start-1", "target": "hitl-1"},
    {"id": "e2", "source": "hitl-1", "target": "out-1"}
  ],
  "config": {"schema_version": 1}
}`
}

func upsertWorkflowDef(t *testing.T, db *database.DB, id string, graph string) {
	t.Helper()
	if err := db.UpsertWorkflowDefinition(&database.WorkflowDefinition{
		ID:        id,
		Name:      "HITL 流",
		Version:   1,
		GraphJSON: graph,
		Enabled:   true,
	}); err != nil {
		t.Fatalf("UpsertWorkflowDefinition: %v", err)
	}
}

func TestRunRoleBoundWorkflow_awaitingHitlThenResumeApproved(t *testing.T) {
	ctx := context.Background()
	SetCheckpointDir(t.TempDir())
	db := testWorkflowDB(t)
	wfID := uniqueWfID("wf-hitl")
	upsertWorkflowDef(t, db, wfID, hitlApprovalGraph())
	role := config.RoleConfig{Name: "tester", WorkflowID: wfID, WorkflowPolicy: "auto"}

	// Non-streaming run (Progress == nil) returns immediately in awaiting state.
	result, err := RunRoleBoundWorkflow(ctx, RunArgs{
		DB:          db,
		Logger:      zap.NewNop(),
		Role:        role,
		UserMessage: "hitl-payload",
	})
	if err != nil {
		t.Fatalf("RunRoleBoundWorkflow: %v", err)
	}
	if result == nil || !result.AwaitingHITL || result.Status != "awaiting_hitl" {
		t.Fatalf("result=%+v", result)
	}
	if result.RunID == "" {
		t.Fatal("RunID empty")
	}
	if !strings.Contains(result.Response, "等待人工审批") {
		t.Fatalf("response=%q", result.Response)
	}
	run, err := db.GetWorkflowRun(result.RunID)
	if err != nil || run == nil {
		t.Fatalf("GetWorkflowRun: %v %v", run, err)
	}
	if run.Status != "awaiting_hitl" || run.PendingHITLNodeID != "hitl-1" {
		t.Fatalf("run.Status=%q pending=%q", run.Status, run.PendingHITLNodeID)
	}
	if !strings.Contains(run.PendingHITLJSON, "确认执行") {
		t.Fatalf("pendingJSON=%q", run.PendingHITLJSON)
	}

	// Resume approved: HITL node proceeds, workflow completes.
	resumed, err := ResumeWorkflowRun(ctx, RunArgs{DB: db, Logger: zap.NewNop(), Role: role}, result.RunID, true, "通过")
	if err != nil {
		t.Fatalf("ResumeWorkflowRun: %v", err)
	}
	if resumed == nil || resumed.Status != "completed" {
		t.Fatalf("resumed=%+v", resumed)
	}
	run, err = db.GetWorkflowRun(result.RunID)
	if err != nil || run == nil {
		t.Fatalf("GetWorkflowRun after resume: %v %v", run, err)
	}
	if run.Status != "completed" {
		t.Fatalf("run.Status=%q, want completed", run.Status)
	}
}

func TestRunRoleBoundWorkflow_hitlRejectResume(t *testing.T) {
	ctx := context.Background()
	SetCheckpointDir(t.TempDir())
	db := testWorkflowDB(t)
	rejectID := uniqueWfID("wf-hitl-reject")
	upsertWorkflowDef(t, db, rejectID, hitlApprovalGraph())
	role := config.RoleConfig{Name: "tester", WorkflowID: rejectID, WorkflowPolicy: "auto"}

	result, err := RunRoleBoundWorkflow(ctx, RunArgs{DB: db, Logger: zap.NewNop(), Role: role, UserMessage: "payload"})
	if err != nil {
		t.Fatalf("RunRoleBoundWorkflow: %v", err)
	}

	resumed, err := ResumeWorkflowRun(ctx, RunArgs{DB: db, Logger: zap.NewNop(), Role: role}, result.RunID, false, "风险太高")
	if err != nil {
		t.Fatalf("ResumeWorkflowRun: %v", err)
	}
	if resumed == nil || resumed.Status != "rejected" {
		t.Fatalf("resumed=%+v", resumed)
	}
	if !strings.Contains(resumed.Response, "被拒绝") {
		t.Fatalf("response=%q", resumed.Response)
	}
	run, _ := db.GetWorkflowRun(result.RunID)
	if run == nil || run.Status != "rejected" {
		t.Fatalf("run.Status=%v", run)
	}
}

func TestRunRoleBoundWorkflow_hitlRejectDefaultComment(t *testing.T) {
	ctx := context.Background()
	SetCheckpointDir(t.TempDir())
	db := testWorkflowDB(t)
	reject2ID := uniqueWfID("wf-hitl-reject2")
	upsertWorkflowDef(t, db, reject2ID, hitlApprovalGraph())
	role := config.RoleConfig{Name: "tester", WorkflowID: reject2ID, WorkflowPolicy: "auto"}

	result, _ := RunRoleBoundWorkflow(ctx, RunArgs{DB: db, Logger: zap.NewNop(), Role: role, UserMessage: "payload"})
	resumed, err := ResumeWorkflowRun(ctx, RunArgs{DB: db, Logger: zap.NewNop(), Role: role}, result.RunID, false, "")
	if err != nil {
		t.Fatalf("ResumeWorkflowRun: %v", err)
	}
	if resumed == nil || resumed.Status != "rejected" {
		t.Fatalf("resumed=%+v", resumed)
	}
}

func TestRunRoleBoundWorkflow_streamingHitlApprovedWakesWaiter(t *testing.T) {
	ctx := context.Background()
	SetCheckpointDir(t.TempDir())
	db := testWorkflowDB(t)
	streamID := uniqueWfID("wf-hitl-stream")
	upsertWorkflowDef(t, db, streamID, hitlApprovalGraph())
	role := config.RoleConfig{Name: "tester", WorkflowID: streamID, WorkflowPolicy: "auto"}

	// Streaming run (Progress != nil) parks in waitWorkflowHITLDecisionWithChannel.
	// Approve via DB after the pause is persisted.
	done := make(chan *RunResult, 1)
	errCh := make(chan error, 1)
	go func() {
		res, runErr := RunRoleBoundWorkflow(ctx, RunArgs{
			DB:          db,
			Logger:      zap.NewNop(),
			Role:        role,
			UserMessage: "stream-payload",
			Progress:    func(eventType, _ string, _ interface{}) {},
		})
		if runErr != nil {
			errCh <- runErr
			return
		}
		done <- res
	}()

	// Wait until the run reaches awaiting_hitl in DB, then record approval.
	var runID string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		runs, _ := db.ListWorkflowRunsAwaitingHITL(10)
		for _, r := range runs {
			if r.Status == "awaiting_hitl" {
				runID = r.ID
			}
		}
		if runID != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if runID == "" {
		t.Fatal("run never reached awaiting_hitl")
	}
	if err := db.RecordWorkflowRunHITLDecision(runID, true, "同意"); err != nil {
		t.Fatalf("RecordWorkflowRunHITLDecision: %v", err)
	}

	select {
	case res := <-done:
		if res == nil || res.Status != "completed" {
			t.Fatalf("res=%+v, want completed after approval", res)
		}
	case err := <-errCh:
		t.Fatalf("RunRoleBoundWorkflow: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("streaming run did not finish after approval")
	}
}

func TestRunRoleBoundWorkflow_disabledAndMissingWorkflow(t *testing.T) {
	ctx := context.Background()
	SetCheckpointDir(t.TempDir())
	db := testWorkflowDB(t)

	t.Run("missing workflow", func(t *testing.T) {
		_, err := RunRoleBoundWorkflow(ctx, RunArgs{
			DB:     db,
			Logger: zap.NewNop(),
			Role:   config.RoleConfig{Name: "tester", WorkflowID: "wf-nope", WorkflowPolicy: "auto"},
		})
		if err == nil || !strings.Contains(err.Error(), "不存在") {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("disabled workflow", func(t *testing.T) {
		disabledID := uniqueWfID("wf-disabled")
		if err := db.UpsertWorkflowDefinition(&database.WorkflowDefinition{
			ID: disabledID, Name: "禁用流", Version: 1, GraphJSON: linearStartOutputGraph(), Enabled: false,
		}); err != nil {
			t.Fatal(err)
		}
		_, err := RunRoleBoundWorkflow(ctx, RunArgs{
			DB:     db,
			Logger: zap.NewNop(),
			Role:   config.RoleConfig{Name: "tester", WorkflowID: disabledID, WorkflowPolicy: "auto"},
		})
		if err == nil || !strings.Contains(err.Error(), "已禁用") {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("nil db", func(t *testing.T) {
		_, err := RunRoleBoundWorkflow(ctx, RunArgs{Role: config.RoleConfig{Name: "tester", WorkflowID: "wf-x"}})
		if err == nil || !strings.Contains(err.Error(), "db is nil") {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("empty workflow id", func(t *testing.T) {
		_, err := RunRoleBoundWorkflow(ctx, RunArgs{DB: db, Role: config.RoleConfig{Name: "tester"}})
		if err == nil || !strings.Contains(err.Error(), "角色未绑定工作流") {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestShouldAutoRunRoleWorkflow(t *testing.T) {
	cases := []struct {
		name string
		role config.RoleConfig
		want bool
	}{
		{name: "empty id", role: config.RoleConfig{WorkflowID: ""}, want: false},
		{name: "auto policy", role: config.RoleConfig{WorkflowID: "wf", WorkflowPolicy: "auto"}, want: true},
		{name: "default policy", role: config.RoleConfig{WorkflowID: "wf"}, want: true},
		{name: "manual policy", role: config.RoleConfig{WorkflowID: "wf", WorkflowPolicy: "manual"}, want: false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShouldAutoRunRoleWorkflow(tt.role); got != tt.want {
				t.Fatalf("got=%v want=%v", got, tt.want)
			}
		})
	}
}

func TestHasTerminalNode(t *testing.T) {
	t.Run("output terminal", func(t *testing.T) {
		g, err := parseGraph(linearStartOutputGraph())
		if err != nil {
			t.Fatal(err)
		}
		if !hasTerminalNode(indexGraph(g)) {
			t.Fatal("linear graph has an output terminal")
		}
	})
	t.Run("single node with no outgoing", func(t *testing.T) {
		g := &graphDef{
			Nodes: []graphNode{{ID: "n1", Type: "tool", Config: map[string]any{}}},
		}
		if !hasTerminalNode(indexGraph(g)) {
			t.Fatal("node without outgoing edges is terminal")
		}
	})
	t.Run("end node type", func(t *testing.T) {
		g := &graphDef{
			Nodes: []graphNode{
				{ID: "a", Type: "tool", Config: map[string]any{}},
				{ID: "b", Type: "end", Config: map[string]any{}},
			},
			Edges: []graphEdge{{Source: "a", Target: "b"}},
		}
		if !hasTerminalNode(indexGraph(g)) {
			t.Fatal("end node is terminal")
		}
	})
}

func TestExtractAwaitingHITL_nonInterruptError(t *testing.T) {
	g, err := parseGraph(hitlApprovalGraph())
	if err != nil {
		t.Fatal(err)
	}
	art, err := defaultEngine.compile(context.Background(), g)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	db := testWorkflowDB(t)
	createTestWorkflowRun(t, db, "run-extract")
	state := newWorkflowLocalState(map[string]any{"message": "x"}, "run-extract")

	// A plain error does not become an AwaitingHITLError.
	if got := extractAwaitingHITL(context.DeadlineExceeded, art, "run-extract", RunArgs{DB: db}, state); got != nil {
		t.Fatalf("extractAwaitingHITL = %v, want nil for non-interrupt error", got)
	}
}

func TestNextHITLNodeID(t *testing.T) {
	cases := []struct {
		name    string
		info    *compose.InterruptInfo
		hitlIDs []string
		want    string
	}{
		{name: "before nodes match", info: &compose.InterruptInfo{BeforeNodes: []string{"hitl-2", "hitl-1"}}, hitlIDs: []string{"hitl-1", "hitl-2"}, want: "hitl-2"},
		{name: "before nodes no hitl", info: &compose.InterruptInfo{BeforeNodes: []string{"other"}}, hitlIDs: []string{"hitl-1"}, want: "other"},
		{name: "nil info with hitl", info: nil, hitlIDs: []string{"hitl-1"}, want: "hitl-1"},
		{name: "nil info no hitl", info: nil, hitlIDs: nil, want: ""},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := nextHITLNodeID(tt.info, tt.hitlIDs); got != tt.want {
				t.Fatalf("got=%q want=%q", got, tt.want)
			}
		})
	}
}

func TestWorkflowInterruptMetadata(t *testing.T) {
	empty := workflowInterruptMetadata(nil)
	if len(empty) != 0 {
		t.Fatalf("nil info metadata=%v", empty)
	}
	meta := workflowInterruptMetadata(&compose.InterruptInfo{BeforeNodes: []string{"a", "b"}})
	if got := meta["resumeTarget"]; got != "a" {
		t.Fatalf("resumeTarget=%v", got)
	}
	if got := meta["beforeNodes"]; got == nil {
		t.Fatal("beforeNodes missing")
	}
}

func TestFirstString(t *testing.T) {
	if firstString(nil) != "" {
		t.Fatal("nil slice should yield empty")
	}
	if firstString([]string{"x", "y"}) != "x" {
		t.Fatal("first element expected")
	}
}

func TestNewWorkflowRuntime(t *testing.T) {
	args := RunArgs{ConversationID: "c1"}
	rt := newWorkflowRuntime(args, "run-rt", nil, map[string]any{"message": "m"})
	if rt.runID != "run-rt" || rt.state == nil {
		t.Fatalf("rt=%+v", rt)
	}
	if rt.state.Inputs["message"] != "m" {
		t.Fatalf("inputs=%v", rt.state.Inputs)
	}
	if rt.state.WorkflowRunID != "run-rt" {
		t.Fatalf("WorkflowRunID=%q", rt.state.WorkflowRunID)
	}
}

func TestToStateInputs(t *testing.T) {
	in := WorkflowInput{
		Message:         "m",
		ConversationID:  "c",
		ProjectID:       "p",
		Role:            "tester",
		WorkflowID:      "wf",
		WorkflowVersion: 3,
	}
	state := in.toStateInputs()
	if state["message"] != "m" || state["conversationId"] != "c" || state["projectId"] != "p" {
		t.Fatalf("state=%v", state)
	}
	if state["role"] != "tester" || state["workflowId"] != "wf" {
		t.Fatalf("state=%v", state)
	}
	if state["workflowVersion"] != 3 {
		t.Fatalf("workflowVersion=%v", state["workflowVersion"])
	}
}

func TestWorkflowInputFromMap_variants(t *testing.T) {
	tests := []struct {
		name    string
		in      map[string]interface{}
		version int
	}{
		{name: "nil map", in: nil},
		{name: "int version", in: map[string]interface{}{"workflowVersion": 2}, version: 2},
		{name: "int64 version", in: map[string]interface{}{"workflowVersion": int64(3)}, version: 3},
		{name: "float version", in: map[string]interface{}{"workflowVersion": 4.0}, version: 4},
		{name: "string version", in: map[string]interface{}{"workflowVersion": "5"}, version: 5},
		{name: "bad string version", in: map[string]interface{}{"workflowVersion": "abc"}},
		{name: "message as number", in: map[string]interface{}{"message": 42}, version: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := workflowInputFromMap(tt.in)
			if got.WorkflowVersion != tt.version {
				t.Fatalf("WorkflowVersion=%d want=%d", got.WorkflowVersion, tt.version)
			}
			if tt.name == "message as number" && got.Message != "42" {
				t.Fatalf("Message=%q", got.Message)
			}
		})
	}
}

func TestTruncateWorkflowPreview(t *testing.T) {
	if got := truncateWorkflowPreview("  ", 5); got != "" {
		t.Fatalf("whitespace=%q", got)
	}
	if got := truncateWorkflowPreview("short", 0); got != "short" {
		t.Fatalf("limit 0=%q", got)
	}
	long := strings.Repeat("中", 30)
	got := truncateWorkflowPreview(long, 10)
	if len([]rune(got)) != 13 || !strings.HasSuffix(got, "...") {
		t.Fatalf("preview=%q (runes=%d)", got, len([]rune(got)))
	}
}
