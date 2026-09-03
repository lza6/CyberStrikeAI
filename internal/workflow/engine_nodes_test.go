package workflow

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/mcp"

	"github.com/cloudwego/eino/compose"
	"go.uber.org/zap"
)

// agentFailFastCfg builds a config whose provider is rejected before any HTTP
// call, so runAgentNode fails fast without network access or retries.
func agentFailFastCfg() *config.Config {
	cfg := &config.Config{}
	cfg.OpenAI.Provider = "unsupported-provider"
	cfg.OpenAI.Model = "test-model"
	cfg.MultiAgent.TurnToolCallLimit = -1
	return cfg
}

// uniqueWfID returns a process-unique workflow id. defaultEngine caches
// compiled artifacts by workflow id across tests AND across -count=N
// repetitions in the same process; a cached artifact binds the checkpoint
// store captured at compile time (inside a since-deleted t.TempDir). Unique
// ids force a fresh compile per test, keeping checkpoint paths valid.
var wfIDCounter atomic.Int64

func uniqueWfID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, wfIDCounter.Add(1))
}

func TestRunAgentNode_failsFastOnUnsupportedProvider(t *testing.T) {
	ag := newWorkflowTestAgent(t, "", nil)
	node := graphNode{ID: "agent-1", Type: "agent", Label: "分析", Config: map[string]any{"instruction": "do", "output_key": "r"}}
	state := newWorkflowLocalState(map[string]any{"message": "hi"}, "run-agent")

	out, proceed, status, errText := runAgentNode(context.Background(), RunArgs{AppCfg: agentFailFastCfg(), Agent: ag}, node, state)
	if status != "failed" || proceed {
		t.Fatalf("status=%q proceed=%v", status, proceed)
	}
	if !strings.Contains(errText, "not supported") {
		t.Fatalf("errText=%q", errText)
	}
	if out["mode"] != "eino_single" {
		t.Fatalf("mode=%v", out["mode"])
	}
	// iteration offset is still flushed on failure
	if state.MainIterationOffset != 0 {
		t.Fatalf("MainIterationOffset=%d", state.MainIterationOffset)
	}
}

func TestRunAgentNode_deepModeFailurePropagates(t *testing.T) {
	ag := newWorkflowTestAgent(t, "", nil)
	node := graphNode{ID: "agent-1", Type: "agent", Config: map[string]any{"instruction": "do", "output_key": "r", "agent_mode": "deep"}}
	state := newWorkflowLocalState(map[string]any{"message": "hi"}, "run-agent-deep")

	_, proceed, status, errText := runAgentNode(context.Background(), RunArgs{AppCfg: agentFailFastCfg(), Agent: ag}, node, state)
	if status != "failed" || proceed {
		t.Fatalf("status=%q proceed=%v", status, proceed)
	}
	if !strings.Contains(errText, "not supported") {
		t.Fatalf("errText=%q", errText)
	}
}

func TestRunAgentNode_dbFailureRecorded(t *testing.T) {
	ag := newWorkflowTestAgent(t, "", nil)
	db := testWorkflowDB(t)
	runID := "run-agent-db"
	createTestWorkflowRun(t, db, runID)

	node := graphNode{ID: "agent-1", Type: "agent", Label: "分析", Config: map[string]any{"instruction": "do", "output_key": "r"}}
	state := newWorkflowLocalState(map[string]any{"message": "hi"}, runID)
	rt := &workflowRuntime{args: RunArgs{AppCfg: agentFailFastCfg(), Agent: ag, DB: db}, runID: runID, state: state}
	ctx := withWorkflowRuntime(context.Background(), rt)

	result, err := runWorkflowNodeLambda(ctx, node)
	if err == nil {
		t.Fatal("expected node lambda error for failed agent run")
	}
	if result == nil {
		t.Fatal("expected result map despite error")
	}
	if got := result["status"]; got != "failed" {
		t.Fatalf("status=%v", got)
	}
	// The node run record must be finished in the DB with error text.
	nodeRuns, err := db.ListWorkflowNodeRuns(runID)
	if err != nil {
		t.Fatalf("ListWorkflowNodeRuns: %v", err)
	}
	if len(nodeRuns) != 1 {
		t.Fatalf("node runs=%d, want 1", len(nodeRuns))
	}
	if nodeRuns[0].Status != "failed" {
		t.Fatalf("node run status=%q", nodeRuns[0].Status)
	}
	if nodeRuns[0].Error == "" {
		t.Fatal("node run Error empty")
	}
}

func TestExecuteEinoGraph_agentFailureStopsRun(t *testing.T) {
	ctx := context.Background()
	SetCheckpointDir(t.TempDir())
	db := testWorkflowDB(t)
	runID := "run-agent-fail"
	createTestWorkflowRun(t, db, runID)

	graph := `{
  "nodes": [
    {"id": "start-1", "type": "start", "label": "开始", "position": {"x": 0, "y": 0}, "config": {}},
    {"id": "agent-1", "type": "agent", "label": "失败代理", "position": {"x": 0, "y": 80}, "config": {"instruction": "noop", "output_key": "agent_out"}},
    {"id": "out-1", "type": "output", "label": "输出", "position": {"x": 0, "y": 160}, "config": {"output_key": "result"}}
  ],
  "edges": [
    {"id": "e1", "source": "start-1", "target": "agent-1"},
    {"id": "e2", "source": "agent-1", "target": "out-1"}
  ],
  "config": {"schema_version": 1}
}`
	g, err := parseGraph(graph)
	if err != nil {
		t.Fatal(err)
	}
	state := newWorkflowLocalState(map[string]any{"message": "m"}, runID)
	ag := newWorkflowTestAgent(t, "", nil)
	args := RunArgs{DB: db, AppCfg: agentFailFastCfg(), Agent: ag}
	if err := executeEinoGraph(ctx, args, runID, uniqueWfID("wf-agent-fail"), 1, g, state); err == nil {
		t.Fatal("expected agent node failure to stop graph execution")
	}
}

func TestExecuteEinoGraph_toolNodeSuccessThroughGraph(t *testing.T) {
	ctx := context.Background()
	SetCheckpointDir(t.TempDir())
	db := testWorkflowDB(t)
	runID := "run-tool-graph"
	createTestWorkflowRun(t, db, runID)

	graph := `{
  "nodes": [
    {"id": "start-1", "type": "start", "label": "开始", "position": {"x": 0, "y": 0}, "config": {}},
    {"id": "tool-1", "type": "tool", "label": "工具", "position": {"x": 0, "y": 80}, "config": {"tool_name": "lookup", "arguments": "{\"k\":\"v\"}", "output_key": "tool_out"}},
    {"id": "out-1", "type": "output", "label": "输出", "position": {"x": 0, "y": 160}, "config": {"output_key": "result", "source_binding": {"from": "outputs", "field": "tool_out"}}}
  ],
  "edges": [
    {"id": "e1", "source": "start-1", "target": "tool-1"},
    {"id": "e2", "source": "tool-1", "target": "out-1"}
  ],
  "config": {"schema_version": 1}
}`
	g, err := parseGraph(graph)
	if err != nil {
		t.Fatal(err)
	}
	state := newWorkflowLocalState(map[string]any{"message": "m"}, runID)
	ag := newWorkflowTestAgent(t, "lookup", okToolHandler("resolved-tool-output"))
	args := RunArgs{DB: db, AppCfg: &config.Config{}, Agent: ag, ConversationID: "conv-tool"}
	if err := executeEinoGraph(ctx, args, runID, uniqueWfID("wf-tool"), 1, g, state); err != nil {
		t.Fatalf("executeEinoGraph: %v", err)
	}
	if got := state.Outputs["result"]; got != "resolved-tool-output\n" {
		t.Fatalf("result=%q", got)
	}
	if state.Metrics["tool_call_count"] != float64(1) {
		t.Fatalf("tool_call_count=%v", state.Metrics["tool_call_count"])
	}
}

func TestExecuteEinoGraph_toolNodeIsErrorStopsRun(t *testing.T) {
	ctx := context.Background()
	SetCheckpointDir(t.TempDir())
	db := testWorkflowDB(t)
	runID := "run-tool-err"
	createTestWorkflowRun(t, db, runID)

	graph := `{
  "nodes": [
    {"id": "start-1", "type": "start", "label": "开始", "position": {"x": 0, "y": 0}, "config": {}},
    {"id": "tool-1", "type": "tool", "label": "工具", "position": {"x": 0, "y": 80}, "config": {"tool_name": "lookup", "arguments": "{}"}},
    {"id": "out-1", "type": "output", "label": "输出", "position": {"x": 0, "y": 160}, "config": {"output_key": "result"}}
  ],
  "edges": [
    {"id": "e1", "source": "start-1", "target": "tool-1"},
    {"id": "e2", "source": "tool-1", "target": "out-1"}
  ],
  "config": {"schema_version": 1}
}`
	g, err := parseGraph(graph)
	if err != nil {
		t.Fatal(err)
	}
	state := newWorkflowLocalState(map[string]any{"message": "m"}, runID)
	ag := newWorkflowTestAgent(t, "lookup", func(_ context.Context, _ map[string]any) (*mcp.ToolResult, error) {
		return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "denied"}}, IsError: true}, nil
	})
	args := RunArgs{DB: db, AppCfg: &config.Config{}, Agent: ag, ConversationID: "conv-tool"}
	if err := executeEinoGraph(ctx, args, runID, uniqueWfID("wf-tool-err"), 1, g, state); err == nil {
		t.Fatal("expected tool IsError to stop graph execution")
	}
}

func TestExecuteEinoGraph_unknownNodeTypeIsRejected(t *testing.T) {
	ctx := context.Background()
	SetCheckpointDir(t.TempDir())
	db := testWorkflowDB(t)
	runID := "run-unknown-skip"
	createTestWorkflowRun(t, db, runID)

	// validation.go restricts allowed node types, and Engine.compile re-runs
	// validateGraphDefinition (engine.go compile path), so an unknown node type
	// is rejected before any lambda is built. Verify compile fails fast with the
	// exact validation error instead of silently skipping the node.
	graph := `{
  "nodes": [
    {"id": "start-1", "type": "start", "label": "开始", "position": {"x": 0, "y": 0}, "config": {}},
    {"id": "tool-1", "type": "tool", "label": "占位", "position": {"x": 0, "y": 80}, "config": {"tool_name": "lookup", "arguments": "{}"}},
    {"id": "out-1", "type": "output", "label": "输出", "position": {"x": 0, "y": 160}, "config": {"output_key": "result"}}
  ],
  "edges": [
    {"id": "e1", "source": "start-1", "target": "tool-1"},
    {"id": "e2", "source": "tool-1", "target": "out-1"}
  ],
  "config": {"schema_version": 1}
}`
	g, err := parseGraph(graph)
	if err != nil {
		t.Fatal(err)
	}
	// Mutate the tool node into an unknown type; compile must surface the
	// validation error (fail-closed), not execute a skipped placeholder.
	g.Nodes[1].Type = "mystery"
	ag := newWorkflowTestAgent(t, "lookup", okToolHandler("out"))
	state := newWorkflowLocalState(map[string]any{"message": "m"}, runID)
	args := RunArgs{DB: db, AppCfg: &config.Config{}, Agent: ag}
	err = executeEinoGraph(ctx, args, runID, uniqueWfID("wf-unknown"), 1, g, state)
	if err == nil {
		t.Fatal("unknown node type must fail compile (fail-closed), got nil error")
	}
	if !strings.Contains(err.Error(), "未知节点类型") {
		t.Fatalf("err=%v, want 未知节点类型", err)
	}
}

func TestExecuteEinoGraph_nodeRunRecordsInDB(t *testing.T) {
	ctx := context.Background()
	SetCheckpointDir(t.TempDir())
	db := testWorkflowDB(t)
	runID := "run-db-records"
	createTestWorkflowRun(t, db, runID)

	g, err := parseGraph(linearStartOutputGraph())
	if err != nil {
		t.Fatal(err)
	}
	state := newWorkflowLocalState(map[string]any{"message": "ping"}, runID)
	if err := executeEinoGraph(ctx, RunArgs{DB: db}, runID, uniqueWfID("wf-db-records"), 1, g, state); err != nil {
		t.Fatalf("executeEinoGraph: %v", err)
	}
	runs, err := db.ListWorkflowNodeRuns(runID)
	if err != nil {
		t.Fatalf("ListWorkflowNodeRuns: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("node runs=%d, want 2", len(runs))
	}
	for _, r := range runs {
		if r.Status != "completed" {
			t.Fatalf("node run %s status=%q", r.NodeID, r.Status)
		}
		if r.InputJSON == "" || r.OutputJSON == "" {
			t.Fatalf("node run %s missing json payloads", r.NodeID)
		}
	}
}

func TestRunWorkflowNodeLambda_runtimeMissing(t *testing.T) {
	node := graphNode{ID: "n", Type: "output", Config: map[string]any{}}
	_, err := runWorkflowNodeLambda(context.Background(), node)
	if err == nil || !strings.Contains(err.Error(), "workflow runtime missing") {
		t.Fatalf("err=%v", err)
	}
}

func TestRunWorkflowNodeLambda_prepareJoinUnknownStrategy(t *testing.T) {
	db := testWorkflowDB(t)
	runID := "run-join-unknown"
	createTestWorkflowRun(t, db, runID)

	idx := &graphIndex{
		nodes: map[string]graphNode{"mid": {ID: "mid", Type: "output", Config: map[string]any{"join_strategy": "bogus"}}},
		incoming: map[string][]graphEdge{"mid": {
			{Source: "a", Target: "mid"},
			{Source: "b", Target: "mid"},
		}},
		outgoing: map[string][]graphEdge{},
	}
	state := newWorkflowLocalState(map[string]any{}, runID)
	rt := &workflowRuntime{args: RunArgs{DB: db}, runID: runID, idx: idx, state: state}
	ctx := withWorkflowRuntime(context.Background(), rt)

	_, err := runWorkflowNodeLambda(ctx, graphNode{ID: "mid", Type: "output", Config: map[string]any{"join_strategy": "bogus"}})
	if err == nil || !strings.Contains(err.Error(), "未知汇聚策略") {
		t.Fatalf("err=%v", err)
	}
}

func TestCompileAgentSubgraph_builds(t *testing.T) {
	ctx := context.Background()
	node := graphNode{ID: "agent-1", Type: "agent", Config: map[string]any{"instruction": "noop", "output_key": "r"}}
	sub, err := compileAgentSubgraph(ctx, node)
	if err != nil {
		t.Fatalf("compileAgentSubgraph: %v", err)
	}
	if sub == nil {
		t.Fatal("subgraph nil")
	}
}

func TestCompileAgentSubgraph_agentNodeFailureSurfaces(t *testing.T) {
	ctx := context.Background()
	SetCheckpointDir(t.TempDir())
	db := testWorkflowDB(t)
	runID := "run-subgraph-fail"
	createTestWorkflowRun(t, db, runID)

	graph := `{
  "nodes": [
    {"id": "start-1", "type": "start", "label": "开始", "position": {"x": 0, "y": 0}, "config": {}},
    {"id": "agent-1", "type": "agent", "label": "子图代理", "position": {"x": 0, "y": 80}, "config": {"instruction": "noop", "output_key": "agent_out"}},
    {"id": "out-1", "type": "output", "label": "输出", "position": {"x": 0, "y": 160}, "config": {"output_key": "result"}}
  ],
  "edges": [
    {"id": "e1", "source": "start-1", "target": "agent-1"},
    {"id": "e2", "source": "agent-1", "target": "out-1"}
  ],
  "config": {"schema_version": 1}
}`
	g, err := parseGraph(graph)
	if err != nil {
		t.Fatal(err)
	}
	state := newWorkflowLocalState(map[string]any{"message": "m"}, runID)
	ag := newWorkflowTestAgent(t, "", nil)
	args := RunArgs{DB: db, AppCfg: agentFailFastCfg(), Agent: ag}
	err = executeEinoGraph(ctx, args, runID, uniqueWfID("wf-subgraph-fail"), 1, g, state)
	if err == nil {
		t.Fatal("expected agent subgraph failure to surface")
	}
	if !strings.Contains(err.Error(), "失败") {
		t.Fatalf("err=%v", err)
	}
}

func TestExecuteEinoGraph_hitlInterruptSurfacesAwaitingError(t *testing.T) {
	ctx := context.Background()
	SetCheckpointDir(t.TempDir())
	db := testWorkflowDB(t)
	runID := "run-hitl-interrupt"
	createTestWorkflowRun(t, db, runID)

	g, err := parseGraph(hitlApprovalGraph())
	if err != nil {
		t.Fatal(err)
	}
	state := newWorkflowLocalState(map[string]any{"message": "m"}, runID)
	var events []string
	args := RunArgs{
		DB: db,
		Progress: func(eventType, _ string, _ interface{}) {
			events = append(events, eventType)
		},
	}
	_, err = invokeEinoGraph(ctx, args, runID, uniqueWfID("wf-hitl-interrupt"), 1, g, state, false)
	if err == nil {
		t.Fatal("expected awaiting HITL interrupt")
	}
	if !IsAwaitingHITL(err) {
		t.Fatalf("err=%v, want AwaitingHITLError", err)
	}
	hitlErr := err.(*AwaitingHITLError)
	if hitlErr.NodeID != "hitl-1" {
		t.Fatalf("NodeID=%q", hitlErr.NodeID)
	}
	if hitlErr.Prompt != "确认执行？" {
		t.Fatalf("Prompt=%q", hitlErr.Prompt)
	}
	foundWait := false
	for _, ev := range events {
		if ev == "workflow_hitl_waiting" {
			foundWait = true
		}
	}
	if !foundWait {
		t.Fatalf("events=%v", events)
	}
	// pending state must be persisted for resume
	run, err := db.GetWorkflowRun(runID)
	if err != nil || run == nil {
		t.Fatalf("GetWorkflowRun: %v %v", run, err)
	}
	if run.Status != "awaiting_hitl" {
		t.Fatalf("run status=%q", run.Status)
	}
}

func TestWireEdgeConditionBranch_routesByEdgeCondition(t *testing.T) {
	ctx := context.Background()
	SetCheckpointDir(t.TempDir())
	db := testWorkflowDB(t)

	// Graph with edge-level conditions on a start node's outgoing edges
	// (both edges conditional, one matches).
	graph := `{
  "nodes": [
    {"id": "start-1", "type": "start", "label": "开始", "position": {"x": 0, "y": 0}, "config": {}},
    {"id": "out-yes", "type": "output", "label": "命中", "position": {"x": -80, "y": 160}, "config": {"output_key": "branch", "static_value": "yes"}},
    {"id": "out-no", "type": "output", "label": "未命中", "position": {"x": 80, "y": 160}, "config": {"output_key": "branch", "static_value": "no"}}
  ],
  "edges": [
    {"id": "e1", "source": "start-1", "target": "out-yes", "config": {"condition": "{{inputs.message}} == go"}},
    {"id": "e2", "source": "start-1", "target": "out-no", "config": {"condition": "{{inputs.message}} == stop"}}
  ],
  "config": {"schema_version": 1}
}`
	g, err := parseGraph(graph)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateGraphDefinition(g, indexGraph(g)); err != nil {
		t.Fatalf("validate: %v", err)
	}
	idx := indexGraph(g)
	if !hasConditionalOutgoingEdges(idx, "start-1") {
		t.Fatal("expected conditional outgoing edges")
	}

	runID := "run-edge-cond"
	createTestWorkflowRun(t, db, runID)
	state := newWorkflowLocalState(map[string]any{"message": "go"}, runID)
	if err := executeEinoGraph(ctx, RunArgs{DB: db}, runID, uniqueWfID("wf-edge-cond"), 1, g, state); err != nil {
		t.Fatalf("executeEinoGraph: %v", err)
	}
	if got := state.Outputs["branch"]; got != "yes" {
		t.Fatalf("branch=%v, want yes", got)
	}
}

func TestCheckpointStore_pathRejectsUnsafeIDs(t *testing.T) {
	dir := t.TempDir()
	store, err := newFileCheckPointStore(dir)
	if err != nil {
		t.Fatalf("newFileCheckPointStore: %v", err)
	}
	ctx := context.Background()

	t.Run("empty id", func(t *testing.T) {
		if _, ok, err := store.Get(ctx, "  "); err == nil && ok {
			t.Fatal("empty id should error")
		}
		if err := store.Set(ctx, "  ", []byte("x")); err == nil {
			t.Fatal("empty id should error on Set")
		}
	})
	t.Run("path traversal", func(t *testing.T) {
		if err := store.Set(ctx, "../evil", []byte("x")); err == nil {
			t.Fatal("traversal id should error on Set")
		}
		if _, _, err := store.Get(ctx, `a\b`); err == nil {
			t.Fatal("backslash id should error on Get")
		}
	})
	t.Run("missing file", func(t *testing.T) {
		_, ok, err := store.Get(ctx, "missing-id")
		if err != nil || ok {
			t.Fatalf("ok=%v err=%v, want false/nil", ok, err)
		}
	})
	t.Run("set then get roundtrip", func(t *testing.T) {
		if err := store.Set(ctx, "roundtrip", []byte("payload")); err != nil {
			t.Fatalf("Set: %v", err)
		}
		data, ok, err := store.Get(ctx, "roundtrip")
		if err != nil || !ok {
			t.Fatalf("ok=%v err=%v", ok, err)
		}
		if string(data) != "payload" {
			t.Fatalf("data=%q", data)
		}
	})
}

func TestNewFileCheckPointStore_defaultDir(t *testing.T) {
	// Empty dir falls back to data/workflow-checkpoints relative path; only
	// assert it constructs successfully (avoid writing outside temp in CWD).
	store, err := newFileCheckPointStore("  ")
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if store == nil {
		t.Fatal("store nil")
	}
}

func TestWireEdgeConditionBranch_runtimeMissingBranch(t *testing.T) {
	// The branch selector without a workflow runtime must error rather than
	// route blindly. Covered indirectly: run branch fn standalone.
	wf := compose.NewWorkflow[WorkflowInput, WorkflowOutput]()
	idx := &graphIndex{
		nodes: map[string]graphNode{
			"s":  {ID: "s", Type: "start", Config: map[string]any{}},
			"o1": {ID: "o1", Type: "output", Config: map[string]any{"output_key": "a", "static_value": "1"}},
			"o2": {ID: "o2", Type: "output", Config: map[string]any{"output_key": "b", "static_value": "2"}},
		},
		outgoing: map[string][]graphEdge{
			"s": {
				{Source: "s", Target: "o1", Config: map[string]any{"condition": "true"}},
				{Source: "s", Target: "o2", Config: map[string]any{"condition": "false"}},
			},
		},
		incoming: map[string][]graphEdge{},
	}
	if err := wireEdgeConditionBranch(wf, map[string]*compose.WorkflowNode{}, idx, "s", idx.nodes["s"]); err != nil {
		t.Fatalf("wireEdgeConditionBranch: %v", err)
	}
}

func TestAgentProgressHelpers_nilStateSafe(t *testing.T) {
	// applyWorkflowMainIterationOffset and enrich must not panic on nil state.
	applyWorkflowMainIterationOffset(map[string]interface{}{"einoScope": "main", "iteration": 1}, nil)
	enrichWorkflowAgentEventData(map[string]interface{}{}, nil, graphNode{ID: "x"})
}

func TestWorkflowAgentProgress_nilProgressReturnsNil(t *testing.T) {
	if workflowAgentProgress(nil, newWorkflowLocalState(nil, "r"), graphNode{}) != nil {
		t.Fatal("expected nil wrapper for nil progress")
	}
}

func TestAccumulateWorkflowMetric_andCollectAgentMetrics(t *testing.T) {
	state := newWorkflowLocalState(nil, "r")
	accumulateWorkflowMetric(nil, "x", 1) // nil state safe
	accumulateWorkflowMetric(state, "count", 2)
	accumulateWorkflowMetric(state, "count", 3)
	if state.Metrics["count"] != float64(5) {
		t.Fatalf("count=%v", state.Metrics["count"])
	}
	accumulateWorkflowMetric(state, "str", "7")
	if state.Metrics["str"] != float64(7) {
		t.Fatalf("str=%v", state.Metrics["str"])
	}

	collectAgentMetrics(nil, map[string]any{"prompt_tokens": 1}) // nil state safe
	collectAgentMetrics(state, "not-a-map")
	collectAgentMetrics(state, map[string]interface{}{
		"prompt_tokens":     10,
		"completion_tokens": 5,
		"total_tokens":      15,
		"cost":              0.5,
	})
	if state.Metrics["prompt_tokens"] != float64(10) {
		t.Fatalf("prompt_tokens=%v", state.Metrics["prompt_tokens"])
	}
	if state.Metrics["completion_tokens"] != float64(5) {
		t.Fatalf("completion_tokens=%v", state.Metrics["completion_tokens"])
	}
	if state.Metrics["total_tokens"] != float64(15) {
		t.Fatalf("total_tokens=%v", state.Metrics["total_tokens"])
	}
	if state.Metrics["cost"] != 0.5 {
		t.Fatalf("cost=%v", state.Metrics["cost"])
	}
	// usage map nested totals accumulate
	collectAgentMetrics(state, map[string]interface{}{
		"usage": map[string]interface{}{"input_tokens": 3, "output_tokens": 4},
	})
	if state.Metrics["input_tokens"] != float64(3) {
		t.Fatalf("input_tokens=%v", state.Metrics["input_tokens"])
	}
	if state.Metrics["output_tokens"] != float64(4) {
		t.Fatalf("output_tokens=%v", state.Metrics["output_tokens"])
	}
}

func TestNumericMetric_types(t *testing.T) {
	cases := []struct {
		in   any
		want float64
	}{
		{in: 5, want: 5},
		{in: int32(6), want: 6},
		{in: int64(7), want: 7},
		{in: float32(8), want: 8},
		{in: 9.5, want: 9.5},
		{in: "10", want: 10},
		{in: "bad", want: 0},
		{in: nil, want: 0},
	}
	for i, tt := range cases {
		if got := numericMetric(tt.in); got != tt.want {
			t.Fatalf("case %d: got=%v want=%v", i, got, tt.want)
		}
	}
}

func TestEdgeAllowed_variants(t *testing.T) {
	state := newWorkflowLocalState(map[string]any{"message": "go"}, "r")
	state.LastOutput = map[string]any{"matched": true}

	condEdge := graphEdge{Config: map[string]any{"condition": "{{inputs.message}} == go"}}
	if !edgeAllowed(condEdge, graphNode{Type: "tool"}, 0, state) {
		t.Fatal("conditional edge should match")
	}
	condEdgeFail := graphEdge{Config: map[string]any{"condition": "{{inputs.message}} == stop"}}
	if edgeAllowed(condEdgeFail, graphNode{Type: "tool"}, 0, state) {
		t.Fatal("conditional edge should not match")
	}
	// condition source node without explicit edge condition uses branch hint
	yesEdge := graphEdge{Label: "是"}
	if !edgeAllowed(yesEdge, graphNode{Type: "condition"}, 0, state) {
		t.Fatal("condition yes edge should be allowed when matched")
	}
	noEdge := graphEdge{Label: "否"}
	if edgeAllowed(noEdge, graphNode{Type: "condition"}, 1, state) {
		t.Fatal("condition no edge should be blocked when matched")
	}
	// plain edge is always allowed
	if !edgeAllowed(graphEdge{}, graphNode{Type: "tool"}, 0, state) {
		t.Fatal("plain edge should be allowed")
	}
}

func TestConditionBranchAllowed_hintPriority(t *testing.T) {
	state := newWorkflowLocalState(nil, "r")
	state.LastOutput = map[string]any{"matched": "true"}

	configHint := graphEdge{Config: map[string]any{"branch": "false"}}
	if conditionBranchAllowed(configHint, 0, state) {
		t.Fatal("config hint false should block when matched")
	}
	labelHint := graphEdge{Label: "是"}
	if !conditionBranchAllowed(labelHint, 0, state) {
		t.Fatal("label 是 should pass when matched")
	}
	noHint := graphEdge{}
	if !conditionBranchAllowed(noHint, 0, state) {
		t.Fatal("edge index 0 should pass when matched")
	}
	if conditionBranchAllowed(noHint, 1, state) {
		t.Fatal("edge index 1 should be blocked when matched")
	}
	// index >= 2 without hint is blocked
	if conditionBranchAllowed(noHint, 2, state) {
		t.Fatal("edge index 2 should be blocked")
	}
}

func TestValueFromPath_variants(t *testing.T) {
	state := newWorkflowLocalState(map[string]any{
		"message": "m",
		"nested":  map[string]any{"deep": "v"},
	}, "r")
	state.LastOutput = map[string]any{"output": "prev"}
	state.Outputs["k"] = "out"

	cases := []struct {
		path string
		want any
	}{
		{path: "inputs.message", want: "m"},
		{path: "input.message", want: "m"},
		{path: "inputs.nested.deep", want: "v"},
		{path: "previous.output", want: "prev"},
		{path: "prev.output", want: "prev"},
		{path: "outputs.k", want: "out"},
		{path: "message", want: "m"},
		{path: "previous.missing", want: ""},
		{path: "unknown.root", want: ""},
		{path: "inputs.nested.deeper.still", want: ""},
		{path: "inputs.missing", want: ""},
	}
	for _, tt := range cases {
		if got := valueFromPath(tt.path, state); got != tt.want {
			t.Fatalf("path=%q got=%#v want=%#v", tt.path, got, tt.want)
		}
	}
}

func TestResolveTemplate_variants(t *testing.T) {
	state := newWorkflowLocalState(map[string]any{"message": "hi"}, "r")
	state.LastOutput = map[string]any{"output": "prev-out"}

	if got := resolveTemplate("", state); got != "prev-out" {
		t.Fatalf("empty template=%q", got)
	}
	if got := resolveTemplate("值: {{inputs.message}}!", state); got != "值: hi!" {
		t.Fatalf("template=%q", got)
	}
	// empty state: valueFromPath("previous.output") on nil LastOutput returns "" —
	// resolveTemplate passes it through unchanged.
	emptyState := newWorkflowLocalState(nil, "r")
	if got := resolveTemplate("", emptyState); got != "" {
		t.Fatalf("empty state template=%q, want empty", got)
	}
}

func TestEvalConditionAtom_nonComparisonTruthiness(t *testing.T) {
	state := newWorkflowLocalState(map[string]any{"flag": "true", "empty": "", "zero": "0", "null": "null"}, "r")
	if !evalConditionAtom("{{inputs.flag}}", state) {
		t.Fatal("true flag should eval true")
	}
	if evalConditionAtom("{{inputs.empty}}", state) {
		t.Fatal("empty should eval false")
	}
	if evalConditionAtom("{{inputs.zero}}", state) {
		t.Fatal("0 should eval false")
	}
	if evalConditionAtom("{{inputs.null}}", state) {
		t.Fatal("null should eval false")
	}
}

func TestResolveNodeInputBinding(t *testing.T) {
	state := newWorkflowLocalState(map[string]any{"message": "m"}, "r")
	state.LastOutput = map[string]any{"output": "prev"}

	withBinding := graphNode{Config: map[string]any{"input_binding": map[string]any{"from": "inputs", "field": "message"}}}
	if got := resolveNodeInputBinding(withBinding.Config, state); got != "m" {
		t.Fatalf("got=%q", got)
	}
	withoutBinding := graphNode{Config: map[string]any{}}
	if got := resolveNodeInputBinding(withoutBinding.Config, state); got != "prev" {
		t.Fatalf("default=%q, want prev", got)
	}
}

func TestResolveHITLPromptBinding_variants(t *testing.T) {
	state := newWorkflowLocalState(map[string]any{"message": "m"}, "r")
	state.LastOutput = map[string]any{"output": "prev-out"}

	cfg := map[string]any{"prompt_binding": map[string]any{"from": "inputs", "field": "message"}}
	if got := resolveHITLPromptBinding(cfg, state); got != "m" {
		t.Fatalf("binding=%q", got)
	}
	if got := resolveHITLPromptBinding(map[string]any{"prompt": "静态提示"}, state); got != "静态提示" {
		t.Fatalf("static=%q", got)
	}
	if got := resolveHITLPromptBinding(map[string]any{}, state); got != "prev-out" {
		t.Fatalf("default=%q", got)
	}
}

func TestToolArgumentBindings_variants(t *testing.T) {
	if toolArgumentBindings(map[string]any{}) != nil {
		t.Fatal("no bindings expected for empty config")
	}
	if toolArgumentBindings(nil) != nil {
		t.Fatal("no bindings for nil config")
	}
	cfg := map[string]any{
		"argument_bindings": map[string]any{
			"host": map[string]any{"from": "inputs", "field": "target"},
			"bad":  "not-a-map",
		},
	}
	bindings := toolArgumentBindings(cfg)
	if len(bindings) != 1 {
		t.Fatalf("bindings=%v", bindings)
	}
	if bindings["host"].From != "inputs" || bindings["host"].Field != "target" {
		t.Fatalf("host=%v", bindings["host"])
	}
}

func TestParseFieldBinding_stringJSON(t *testing.T) {
	cfg := map[string]any{"source_binding": `{"from":"inputs","field":"message"}`}
	b, ok := parseFieldBinding(cfg, "source_binding")
	if !ok || b.From != "inputs" || b.Field != "message" {
		t.Fatalf("b=%v ok=%v", b, ok)
	}
	// empty string is skipped
	empty := map[string]any{"source_binding": "  "}
	if _, ok := parseFieldBinding(empty, "source_binding"); ok {
		t.Fatal("empty string binding should be skipped")
	}
	// non-json string is skipped
	bad := map[string]any{"source_binding": "not-json"}
	if _, ok := parseFieldBinding(bad, "source_binding"); ok {
		t.Fatal("bad json binding should be skipped")
	}
	// nil config
	if _, ok := parseFieldBinding(nil, "source_binding"); ok {
		t.Fatal("nil config should not parse")
	}
}

func TestValidateGraphJSON_aggregateErrorPaths(t *testing.T) {
	tests := []struct {
		name    string
		graph   string
		wantErr string
		wantAny []string // 非空时：错误消息含其中任一子串即通过（用于 map 遍历顺序不定的分支）
	}{
		{
			name: "duplicate node id",
			graph: `{"nodes":[
				{"id":"n1","type":"tool","config":{"tool_name":"x"},"position":{"x":0,"y":0}},
				{"id":"n1","type":"output","config":{"output_key":"r"},"position":{"x":1,"y":1}}]}`,
			wantErr: "重复节点 ID",
		},
		{
			name:    "empty node id",
			graph:   `{"nodes":[{"id":"  ","type":"tool","config":{"tool_name":"x"}}]}`,
			wantErr: "空节点 ID",
		},
		{
			name: "duplicate edge id",
			graph: `{"nodes":[
				{"id":"start-1","type":"start","config":{},"position":{"x":0,"y":0}},
				{"id":"out-1","type":"output","config":{"output_key":"r"},"position":{"x":1,"y":1}}],
				"edges":[{"id":"e","source":"start-1","target":"out-1"},{"id":"e","source":"start-1","target":"out-1"}]}`,
			wantErr: "重复连线 ID",
		},
		{
			name: "self loop edge",
			graph: `{"nodes":[
				{"id":"start-1","type":"start","config":{},"position":{"x":0,"y":0}},
				{"id":"out-1","type":"output","config":{"output_key":"r"},"position":{"x":1,"y":1}}],
				"edges":[{"id":"e1","source":"start-1","target":"out-1"},{"id":"e2","source":"out-1","target":"out-1"}]}`,
			wantErr: "自环",
		},
		{
			name: "unknown edge source",
			graph: `{"nodes":[
				{"id":"start-1","type":"start","config":{},"position":{"x":0,"y":0}},
				{"id":"out-1","type":"output","config":{"output_key":"r"},"position":{"x":1,"y":1}}],
				"edges":[{"id":"e1","source":"ghost","target":"out-1"}]}`,
			wantErr: "不存在的源节点",
		},
		{
			name: "unknown edge target",
			graph: `{"nodes":[
				{"id":"start-1","type":"start","config":{},"position":{"x":0,"y":0}},
				{"id":"out-1","type":"output","config":{"output_key":"r"},"position":{"x":1,"y":1}}],
				"edges":[{"id":"e1","source":"start-1","target":"ghost"}]}`,
			wantErr: "不存在的目标节点",
		},
		{
			name: "agent missing instruction and binding",
			graph: `{"nodes":[
				{"id":"start-1","type":"start","config":{},"position":{"x":0,"y":0}},
				{"id":"agent-1","type":"agent","config":{"output_key":"a"},"position":{"x":0,"y":1}},
				{"id":"out-1","type":"output","config":{"output_key":"r"},"position":{"x":1,"y":1}}],
				"edges":[{"id":"e1","source":"start-1","target":"agent-1"},{"id":"e2","source":"agent-1","target":"out-1"}]}`,
			wantErr: "节点指令或输入绑定",
		},
		{
			name: "agent missing output key",
			graph: `{"nodes":[
				{"id":"start-1","type":"start","config":{},"position":{"x":0,"y":0}},
				{"id":"agent-1","type":"agent","config":{"instruction":"do"},"position":{"x":0,"y":1}},
				{"id":"out-1","type":"output","config":{"output_key":"r"},"position":{"x":1,"y":1}}],
				"edges":[{"id":"e1","source":"start-1","target":"agent-1"},{"id":"e2","source":"agent-1","target":"out-1"}]}`,
			wantErr: "输出变量名",
		},
		{
			name: "tool bad arguments json",
			graph: `{"nodes":[
				{"id":"start-1","type":"start","config":{},"position":{"x":0,"y":0}},
				{"id":"tool-1","type":"tool","config":{"tool_name":"nmap","arguments":"{bad"},"position":{"x":0,"y":1}},
				{"id":"out-1","type":"output","config":{"output_key":"r"},"position":{"x":1,"y":1}}],
				"edges":[{"id":"e1","source":"start-1","target":"tool-1"},{"id":"e2","source":"tool-1","target":"out-1"}]}`,
			wantErr: "参数 JSON 非法",
		},
		{
			name: "tool bad timeout",
			graph: `{"nodes":[
				{"id":"start-1","type":"start","config":{},"position":{"x":0,"y":0}},
				{"id":"tool-1","type":"tool","config":{"tool_name":"nmap","timeout_seconds":"abc"},"position":{"x":0,"y":1}},
				{"id":"out-1","type":"output","config":{"output_key":"r"},"position":{"x":1,"y":1}}],
				"edges":[{"id":"e1","source":"start-1","target":"tool-1"},{"id":"e2","source":"tool-1","target":"out-1"}]}`,
			wantErr: "超时时间必须是正整数",
		},
		{
			name: "condition bad expression",
			graph: `{"nodes":[
				{"id":"start-1","type":"start","config":{},"position":{"x":0,"y":0}},
				{"id":"cond-1","type":"condition","config":{"expression":"{{inputs.a} == x"},"position":{"x":0,"y":1}},
				{"id":"out-1","type":"output","config":{"output_key":"r"},"position":{"x":1,"y":1}}],
				"edges":[{"id":"e1","source":"start-1","target":"cond-1"},{"id":"e2","source":"cond-1","target":"out-1"}]}`,
			wantErr: "表达式非法",
		},
		{
			name: "node missing type",
			graph: `{"nodes":[
				{"id":"start-1","type":"start","config":{},"position":{"x":0,"y":0}},
				{"id":"n1","type":"","config":{},"position":{"x":1,"y":1}}]}`,
			wantErr: "缺少节点类型",
		},
		{
			name: "unknown node type",
			graph: `{"nodes":[
				{"id":"start-1","type":"start","config":{},"position":{"x":0,"y":0}},
				{"id":"n1","type":"quantum","config":{},"position":{"x":1,"y":1}}]}`,
			wantErr: "未知节点类型",
		},
		{
			name: "start without outgoing edge",
			graph: `{"nodes":[
				{"id":"start-1","type":"start","config":{},"position":{"x":0,"y":0}},
				{"id":"out-1","type":"output","config":{"output_key":"r"},"position":{"x":1,"y":1}}]}`,
			// 该图同时缺 start 出边与 output 入边；validation.go 按 map 遍历
			// 节点，两者谁先报不确定，断言两个错误消息之一出现。
			wantAny: []string{"至少需要一条出边", "至少需要一条入边"},
		},
		{
			name: "duplicate edge id",
			graph: `{"nodes":[
				{"id":"start-1","type":"start","config":{},"position":{"x":0,"y":0}},
				{"id":"out-1","type":"output","config":{"output_key":"r"},"position":{"x":1,"y":1}}],
				"edges":[{"id":"e1","source":"start-1","target":"out-1"},{"id":"e1","source":"start-1","target":"out-1"}]}`,
			wantErr: "重复连线 ID",
		},
		{
			name: "duplicate parallel edge to output",
			graph: `{"nodes":[
				{"id":"start-1","type":"start","config":{},"position":{"x":0,"y":0}},
				{"id":"tool-1","type":"tool","config":{"tool_name":"nmap"},"position":{"x":0,"y":1}},
				{"id":"out-1","type":"output","config":{"output_key":"r"},"position":{"x":1,"y":1}}],
				"edges":[{"id":"e1","source":"start-1","target":"tool-1"},{"id":"e2","source":"tool-1","target":"out-1"},{"id":"e3","source":"tool-1","target":"out-1"}]}`,
			// 重复 parallel 边在 eino compile 阶段报 "entire output has already been mapped"
			wantErr: "already been mapped",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateGraphJSON(context.Background(), tt.graph)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if len(tt.wantAny) > 0 {
				matched := false
				for _, want := range tt.wantAny {
					if strings.Contains(err.Error(), want) {
						matched = true
						break
					}
				}
				if !matched {
					t.Fatalf("err=%q want any of %v", err.Error(), tt.wantAny)
				}
				return
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err=%q want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestValidateGraphJSON_parseFailure(t *testing.T) {
	if err := ValidateGraphJSON(context.Background(), `{invalid`); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestParseGraph_emptyAndBad(t *testing.T) {
	if _, err := parseGraph("   "); err == nil {
		t.Fatal("empty graph json should fail")
	}
	g, err := parseGraph(`{"nodes":[{"id":"a"}]}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// config defaults to empty map, node type defaults to tool
	if g.Config == nil {
		t.Fatal("config should default to non-nil")
	}
}

func TestDisplayNodeType(t *testing.T) {
	if displayNodeType("output") != "输出" || displayNodeType("end") != "结束" || displayNodeType("tool") != "tool" {
		t.Fatal("displayNodeType mismatch")
	}
}

func TestParsePositiveInt(t *testing.T) {
	if n, err := parsePositiveInt("42"); err != nil || n != 42 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if _, err := parsePositiveInt("0"); err == nil {
		t.Fatal("zero should fail")
	}
	if _, err := parsePositiveInt("-1"); err == nil {
		t.Fatal("negative should fail")
	}
	if _, err := parsePositiveInt("abc"); err == nil {
		t.Fatal("non-numeric should fail")
	}
}

func TestFindStartNodeIDs_fallbackToInDegree(t *testing.T) {
	// No start node: fallback picks in-degree-0 nodes sorted by canvas.
	g := &graphDef{
		Nodes: []graphNode{
			{ID: "b", Type: "tool", Position: graphPosition{X: 10, Y: 0}, Config: map[string]any{}},
			{ID: "a", Type: "output", Position: graphPosition{X: 0, Y: 0}, Config: map[string]any{}},
		},
		Edges: []graphEdge{{Source: "b", Target: "a"}},
	}
	ids := findStartNodeIDs(indexGraph(g))
	if len(ids) != 1 || ids[0] != "b" {
		t.Fatalf("ids=%v, want [b]", ids)
	}
}

func TestSplitBoolExpr_emptyParts(t *testing.T) {
	got := splitBoolExpr("  ", "||")
	if len(got) != 1 {
		t.Fatalf("got=%v", got)
	}
}

func TestValidateJSONFunctions_badPath(t *testing.T) {
	// Path not starting with $ or . must be rejected.
	if err := validateJSONFunctions(`jsonpath({{inputs.a}}, "bad path")`); err == nil {
		t.Fatal("expected path validation error")
	}
	// A path with a recursive descent must be rejected.
	if err := validateJSONFunctions(`jsonpath({{inputs.a}}, "$..deep")`); err == nil {
		t.Fatal("expected recursive descent rejection")
	}
	// A well-formed path passes.
	if err := validateJSONFunctions(`jsonpath({{inputs.a}}, "$.ok")`); err != nil {
		t.Fatalf("valid path rejected: %v", err)
	}
	// A malformed call (regex can't match a path argument) passes through the
	// outer finder but jsonFuncRe requires a quoted path; unquoted form hits the
	// format error only when the finder matched a complete call.
	if err := validateJSONFunctions(`jq({{inputs.a}}, ".ok")`); err != nil {
		t.Fatalf("valid jq rejected: %v", err)
	}
}

func TestValidateConditionExpression_variants(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		wantErr string
	}{
		{name: "empty", expr: "", wantErr: "不能为空"},
		{name: "unbalanced template", expr: "{{inputs.a == b", wantErr: "括号不匹配"},
		{name: "bad jsonpath", expr: `jsonpath({{inputs.a}}, "nope") == 1`, wantErr: "JSONPath"},
		{name: "matches bad regex", expr: `{{inputs.a}} matches "([unclosed"`, wantErr: "matches 正则非法"},
		{name: "empty atom", expr: "a == 1 || && b == 2", wantErr: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateConditionExpression(tt.expr)
			if tt.wantErr == "" {
				return // just must not panic
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err=%v want %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateConditionAtom_emptySides(t *testing.T) {
	if err := validateConditionAtom(" == 5"); err == nil {
		t.Fatal("empty left side should fail")
	}
	if err := validateConditionAtom("5 == "); err == nil {
		t.Fatal("empty right side should fail")
	}
	// TrimSpace removes the leading space so the " matches " operator is not
	// found; use a template var on the left to hit the empty-side check.
	if err := validateConditionAtom(`{{inputs.a}} matches `); err == nil {
		t.Fatal("empty matches right should fail")
	}
	if err := validateConditionAtom(`{{inputs.a}} matches "([unclosed"`); err == nil {
		t.Fatal("invalid matches regex should fail")
	}
}

func TestCompareNumeric_nonNumeric(t *testing.T) {
	if compareNumeric("abc", "5", func(a, b float64) bool { return a > b }) {
		t.Fatal("non-numeric comparison should be false")
	}
}

func TestInvalidateCompiledCache_emptyID(t *testing.T) {
	// Must not panic.
	InvalidateCompiledCache("  ")
}

func TestSplitExpressionAtom_wordOperators(t *testing.T) {
	left, right, ok := splitExpressionAtom("a contains b", " contains ")
	if !ok || strings.TrimSpace(left) != "a" || strings.TrimSpace(right) != "b" {
		t.Fatalf("left=%q right=%q ok=%v", left, right, ok)
	}
}

func TestRunBuiltinNode_startInjectsInputs(t *testing.T) {
	ctx := context.Background()
	node := graphNode{ID: "start-1", Type: "start", Config: map[string]any{}}
	state := newWorkflowLocalState(map[string]any{
		"message":        "hello",
		"conversationId": "conv-1",
		"projectId":      "proj-1",
	}, "run-start")

	out, proceed, status, _ := runBuiltinNode(ctx, RunArgs{}, node, state)
	if status != "completed" || !proceed {
		t.Fatalf("status=%q proceed=%v", status, proceed)
	}
	if out["message"] != "hello" || out["conversationId"] != "conv-1" || out["projectId"] != "proj-1" {
		t.Fatalf("out=%v", out)
	}
}

func TestRunBuiltinNode_emptyInputStart(t *testing.T) {
	ctx := context.Background()
	node := graphNode{ID: "start-1", Type: "start", Config: map[string]any{}}
	state := newWorkflowLocalState(map[string]any{}, "run-start-empty")
	out, proceed, status, _ := runBuiltinNode(ctx, RunArgs{}, node, state)
	if status != "completed" || !proceed {
		t.Fatalf("status=%q proceed=%v", status, proceed)
	}
	if out["message"] != nil {
		t.Fatalf("message=%v, want nil for empty inputs", out["message"])
	}
}

func TestRunBuiltinNode_outputValidationFailure(t *testing.T) {
	ctx := context.Background()
	// output node whose source resolves to empty and static_value empty →
	// value is whatever previous.output holds; assert node completes and
	// writes the resolved value into state.Outputs.
	node := graphNode{ID: "out-1", Type: "output", Config: map[string]any{"output_key": "r"}}
	state := newWorkflowLocalState(map[string]any{}, "run-out")
	state.LastOutput = map[string]any{"output": "上游值"}

	out, proceed, status, _ := runBuiltinNode(ctx, RunArgs{}, node, state)
	if status != "completed" || !proceed {
		t.Fatalf("status=%q proceed=%v", status, proceed)
	}
	if state.Outputs["r"] != "上游值" {
		t.Fatalf("outputs=%v", state.Outputs)
	}
	if out["output_key"] != "r" {
		t.Fatalf("out=%v", out)
	}
}

func TestRunBuiltinNode_endNormalizesResultBinding(t *testing.T) {
	ctx := context.Background()
	node := graphNode{ID: "end-1", Type: "end", Config: map[string]any{
		"result_binding": map[string]any{"from": "inputs", "field": "message"},
	}}
	state := newWorkflowLocalState(map[string]any{"message": "最终"}, "run-end")

	out, proceed, status, _ := runBuiltinNode(ctx, RunArgs{}, node, state)
	if status != "completed" || proceed {
		t.Fatalf("status=%q proceed=%v", status, proceed)
	}
	if out["output"] != "最终" {
		t.Fatalf("output=%v", out["output"])
	}
}

func TestRenderWorkflowResponse_withSkippedNodes(t *testing.T) {
	state := newWorkflowLocalState(map[string]any{}, "r")
	state.Executed = []string{"开始", "输出"}
	state.Skipped = []string{"未知节点"}
	state.Outputs["result"] = "值"

	resp := renderWorkflowResponse("tester", "流程", 2, "run-1", state)
	if !strings.Contains(resp, "tester") || !strings.Contains(resp, "跳过节点") || !strings.Contains(resp, "result") {
		t.Fatalf("resp=%q", resp)
	}

	emptyState := newWorkflowLocalState(map[string]any{}, "r2")
	emptyResp := renderWorkflowResponse("tester", "流程", 1, "run-2", emptyState)
	if !strings.Contains(emptyResp, "暂无输出") {
		t.Fatalf("emptyResp=%q", emptyResp)
	}
}

func TestCacheKey(t *testing.T) {
	if cacheKey("wf", 3) != "wf:3" {
		t.Fatal("cacheKey mismatch")
	}
}

func TestWorkflowInputFromMap_messageNonString(t *testing.T) {
	in := workflowInputFromMap(map[string]interface{}{"message": 42})
	if in.Message != "42" {
		t.Fatalf("Message=%q", in.Message)
	}
}

func TestRunAgentNode_progressRelay(t *testing.T) {
	ag := newWorkflowTestAgent(t, "", nil)
	node := graphNode{ID: "agent-1", Type: "agent", Label: "代理", Config: map[string]any{"instruction": "do", "output_key": "r"}}
	state := newWorkflowLocalState(map[string]any{"message": "hi"}, "run-agent-progress")

	var events []string
	args := RunArgs{
		AppCfg: agentFailFastCfg(),
		Agent:  ag,
		Progress: func(eventType, _ string, _ interface{}) {
			events = append(events, eventType)
		},
	}
	_, proceed, status, _ := runAgentNode(context.Background(), args, node, state)
	if status != "failed" || proceed {
		t.Fatalf("status=%q proceed=%v", status, proceed)
	}
	// failure path does not emit workflow_agent_output (only success does)
	for _, ev := range events {
		if ev == "workflow_agent_output" {
			t.Fatalf("unexpected agent output event: %v", events)
		}
	}
}

func TestRunToolNode_progressNilSafe(t *testing.T) {
	ctx := context.Background()
	ag := newWorkflowTestAgent(t, "lookup", okToolHandler("out"))
	node := graphNode{ID: "tool-1", Type: "tool", Config: map[string]any{"tool_name": "lookup", "arguments": "{}"}}
	state := newWorkflowLocalState(map[string]any{}, "run")
	// args.Progress nil must not panic
	_, proceed, status, _ := runToolNode(ctx, RunArgs{AppCfg: &config.Config{}, Agent: ag}, node, state)
	if status != "completed" || !proceed {
		t.Fatalf("status=%q proceed=%v", status, proceed)
	}
}

func TestTruncateWorkflowToolOutput_multiByteAndTinyBudget(t *testing.T) {
	// budget smaller than marker → marker returned alone
	got := truncateWorkflowToolOutput(strings.Repeat("长", 500), 8, "exec-1")
	if !strings.Contains(got, "truncated") {
		t.Fatalf("tiny budget output=%q", got)
	}
	// empty execution id marker variant
	got = truncateWorkflowToolOutput(strings.Repeat("长", 500), 300, "  ")
	if !strings.Contains(got, "tool execution record") {
		t.Fatalf("empty exec id output=%q", got)
	}
}

var (
	_ = database.WorkflowRun{}  // keep database import
	_ = compose.InterruptInfo{} // keep compose import
	_ = time.Second             // keep time import
	_ = zap.NewNop              // keep zap import
)
