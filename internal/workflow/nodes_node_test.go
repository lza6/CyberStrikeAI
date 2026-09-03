package workflow

import (
	"context"
	"strings"
	"testing"

	"cyberstrike-ai/internal/agent"
	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/mcp"

	"go.uber.org/zap"
)

// newWorkflowTestAgent builds an agent::Agent with a single registered MCP tool.
// toolName == "" means no tool is registered (used to exercise not-found paths).
func newWorkflowTestAgent(t *testing.T, toolName string, handler mcp.ToolHandler) *agent.Agent {
	t.Helper()
	mcpSrv := mcp.NewServer(zap.NewNop())
	if strings.TrimSpace(toolName) != "" {
		mcpSrv.RegisterTool(mcp.Tool{Name: toolName, InputSchema: map[string]any{"type": "object"}}, handler)
	}
	return agent.NewAgent(
		&config.OpenAIConfig{APIKey: "test-key", BaseURL: "https://api.test.com", Model: "test-model"},
		&config.AgentConfig{MaxIterations: 5},
		mcpSrv, nil, zap.NewNop(), 5,
	)
}

func okToolHandler(output string) mcp.ToolHandler {
	return func(_ context.Context, _ map[string]any) (*mcp.ToolResult, error) {
		return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: output}}}, nil
	}
}

func TestRunBuiltinNode_unknownTypeIsSkipped(t *testing.T) {
	ctx := context.Background()
	node := graphNode{ID: "x-1", Type: "bogus", Config: map[string]any{}}
	state := newWorkflowLocalState(map[string]any{}, "run-unknown")
	out, proceed, status, reason := runBuiltinNode(ctx, RunArgs{}, node, state)
	if status != "skipped" || !proceed {
		t.Fatalf("status=%q proceed=%v, want skipped/true", status, proceed)
	}
	if reason != "未知节点类型" {
		t.Fatalf("reason=%q", reason)
	}
	if out["status"] != "skipped" {
		t.Fatalf("out.status=%v", out["status"])
	}
}

func TestRunToolNode_emptyToolNameFails(t *testing.T) {
	ctx := context.Background()
	node := graphNode{ID: "tool-1", Type: "tool", Config: map[string]any{}}
	state := newWorkflowLocalState(map[string]any{}, "run")
	out, proceed, status, errText := runToolNode(ctx, RunArgs{AppCfg: &config.Config{}}, node, state)
	if status != "failed" || proceed {
		t.Fatalf("status=%q proceed=%v", status, proceed)
	}
	if errText != "工具节点未选择 MCP 工具" {
		t.Fatalf("errText=%q", errText)
	}
	if out["node_type"] != "tool" {
		t.Fatalf("out=%#v", out)
	}
}

func TestRunToolNode_nilAgentFails(t *testing.T) {
	ctx := context.Background()
	node := graphNode{ID: "tool-1", Type: "tool", Config: map[string]any{"tool_name": "lookup", "arguments": `{}`}}
	state := newWorkflowLocalState(map[string]any{}, "run")
	_, proceed, status, errText := runToolNode(ctx, RunArgs{AppCfg: &config.Config{}}, node, state)
	if status != "failed" || proceed {
		t.Fatalf("status=%q proceed=%v", status, proceed)
	}
	if !strings.Contains(errText, "Agent 为空") {
		t.Fatalf("errText=%q", errText)
	}
}

func TestRunToolNode_invalidJSONArgsFails(t *testing.T) {
	ctx := context.Background()
	node := graphNode{ID: "tool-1", Type: "tool", Config: map[string]any{"tool_name": "lookup", "arguments": `{not-json`}}
	state := newWorkflowLocalState(map[string]any{}, "run")
	_, proceed, status, errText := runToolNode(ctx, RunArgs{AppCfg: &config.Config{}, Agent: newWorkflowTestAgent(t, "lookup", okToolHandler("out"))}, node, state)
	if status != "failed" || proceed {
		t.Fatalf("status=%q proceed=%v", status, proceed)
	}
	if !strings.Contains(errText, "不是合法 JSON") {
		t.Fatalf("errText=%q", errText)
	}
}

func TestRunToolNode_success(t *testing.T) {
	ctx := context.Background()
	ag := newWorkflowTestAgent(t, "lookup", okToolHandler("tool-output"))
	node := graphNode{ID: "tool-1", Type: "tool", Config: map[string]any{"tool_name": "lookup", "arguments": `{"k":"v"}`, "output_key": "result"}}
	state := newWorkflowLocalState(map[string]any{}, "run")

	var progressEvents []string
	args := RunArgs{
		AppCfg:         &config.Config{},
		Agent:          ag,
		ConversationID: "conv-1",
		Progress: func(eventType, _ string, _ interface{}) {
			progressEvents = append(progressEvents, eventType)
		},
	}
	out, proceed, status, errText := runToolNode(ctx, args, node, state)
	if status != "completed" || !proceed || errText != "" {
		t.Fatalf("status=%q proceed=%v errText=%q", status, proceed, errText)
	}
	if out["is_error"] != false {
		t.Fatalf("is_error=%v", out["is_error"])
	}
	if state.Outputs["result"] != "tool-output\n" {
		t.Fatalf("state.Outputs[result]=%q", state.Outputs["result"])
	}
	found := false
	for _, ev := range progressEvents {
		if ev == "workflow_tool_start" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected workflow_tool_start event, got %v", progressEvents)
	}
}

func TestRunToolNode_toolIsErrorFails(t *testing.T) {
	ctx := context.Background()
	ag := newWorkflowTestAgent(t, "lookup", func(_ context.Context, _ map[string]any) (*mcp.ToolResult, error) {
		return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "boom"}}, IsError: true}, nil
	})
	node := graphNode{ID: "tool-1", Type: "tool", Label: "工具", Config: map[string]any{"tool_name": "lookup", "arguments": `{}`}}
	state := newWorkflowLocalState(map[string]any{}, "run")
	_, proceed, status, errText := runToolNode(ctx, RunArgs{AppCfg: &config.Config{}, Agent: ag}, node, state)
	if status != "failed" || proceed {
		t.Fatalf("status=%q proceed=%v", status, proceed)
	}
	if errText != "boom" {
		t.Fatalf("errText=%q", errText)
	}
}

func TestRunToolNode_toolNotFoundFails(t *testing.T) {
	ctx := context.Background()
	ag := newWorkflowTestAgent(t, "real-tool", okToolHandler("real"))
	node := graphNode{ID: "tool-1", Type: "tool", Config: map[string]any{"tool_name": "ghost", "arguments": `{}`}}
	state := newWorkflowLocalState(map[string]any{}, "run")
	_, proceed, status, errText := runToolNode(ctx, RunArgs{AppCfg: &config.Config{}, Agent: ag}, node, state)
	if status != "failed" || proceed {
		t.Fatalf("status=%q proceed=%v", status, proceed)
	}
	if strings.TrimSpace(errText) == "" {
		t.Fatalf("errText empty")
	}
}

func TestRunAgentNode_guardFailsWhenConfigOrAgentMissing(t *testing.T) {
	ctx := context.Background()
	node := graphNode{ID: "agent-1", Type: "agent", Config: map[string]any{"instruction": "do", "output_key": "r"}}
	state := newWorkflowLocalState(map[string]any{"message": "hi"}, "run")

	_, proceed, status, errText := runAgentNode(ctx, RunArgs{AppCfg: &config.Config{}, Agent: nil}, node, state)
	if status != "failed" || proceed {
		t.Fatalf("case nil agent: status=%q proceed=%v", status, proceed)
	}
	if !strings.Contains(errText, "Agent 为空") {
		t.Fatalf("errText=%q", errText)
	}

	_, proceed, status, _ = runAgentNode(ctx, RunArgs{Agent: newWorkflowTestAgent(t, "", nil)}, node, state)
	if status != "failed" || proceed {
		t.Fatalf("case nil appcfg: status=%q proceed=%v", status, proceed)
	}
}

func TestBuildAgentNodeMessage_variants(t *testing.T) {
	state := newWorkflowLocalState(map[string]any{"message": "m"}, "run")
	state.LastOutput = map[string]any{"output": "prev-out"}

	tests := []struct {
		name        string
		instruction string
		upstream    string
		wantSub     string
	}{
		{name: "instruction+upstream", instruction: "继续", upstream: "up-val", wantSub: "上游输入"},
		{name: "instruction-only", instruction: "纯指令", upstream: "", wantSub: "纯指令"},
		{name: "upstream-only", instruction: "", upstream: "up-only", wantSub: "请基于上游节点输出继续处理"},
		{name: "neither", instruction: "", upstream: "", wantSub: "请基于上游节点输出继续处理"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := graphNode{Config: map[string]any{"instruction": tt.instruction}}
			got := buildAgentNodeMessage(node, state, tt.upstream)
			if !strings.Contains(got, tt.wantSub) {
				t.Fatalf("msg=%q want substring %q", got, tt.wantSub)
			}
		})
	}
}

func TestWorkflowAgentProgress_enrichesAndRoutes(t *testing.T) {
	state := newWorkflowLocalState(map[string]any{}, "run2")
	state.WorkflowRunID = "wf-run-1"
	node := graphNode{ID: "agent-1", Type: "agent"}

	var seen []string
	progress := func(eventType, _ string, _ interface{}) { seen = append(seen, eventType) }
	wrapped := workflowAgentProgress(progress, state, node)
	if wrapped == nil {
		t.Fatal("wrapped callback is nil")
	}

	// iteration: einoScope main, applies offset and enriches.
	wrapped("iteration", "", map[string]interface{}{"einoScope": "main", "iteration": 7, "prompt_tokens": 10})
	// response_start is swallowed (not relayed).
	wrapped("response_start", "", map[string]interface{}{"x": 1})
	// arbitrary event relayed.
	wrapped("tool_call", "", map[string]any{"cmd": "echo"})

	if len(seen) != 2 {
		t.Fatalf("relayed events=%v, want [iteration tool_call]", seen)
	}
	if seen[0] != "iteration" || seen[1] != "tool_call" {
		t.Fatalf("seen=%v", seen)
	}
	if state.SegmentMaxIteration != 7 {
		t.Fatalf("SegmentMaxIteration=%d want 7", state.SegmentMaxIteration)
	}
	if state.Metrics["prompt_tokens"] != float64(10) {
		t.Fatalf("Metrics[prompt_tokens]=%v", state.Metrics["prompt_tokens"])
	}
}

func TestEnrichWorkflowAgentEventData(t *testing.T) {
	state := newWorkflowLocalState(map[string]any{}, "run3")
	state.WorkflowRunID = "wf-run-3"
	node := graphNode{ID: "agent-9"}

	data := map[string]interface{}{"cmd": "ls"}
	enrichWorkflowAgentEventData(data, state, node)
	if data["workflowNodeId"] != "agent-9" {
		t.Fatalf("workflowNodeId=%v", data["workflowNodeId"])
	}
	if data["workflowRunId"] != "wf-run-3" {
		t.Fatalf("workflowRunId=%v", data["workflowRunId"])
	}

	// Non-map payload is ignored without panic.
	enrichWorkflowAgentEventData("not-a-map", state, node)
	enrichWorkflowAgentEventData(nil, state, node)
}

func TestApplyWorkflowMainIterationOffset_scopeFiltered(t *testing.T) {
	state := newWorkflowLocalState(map[string]any{}, "run4")
	state.MainIterationOffset = 10

	// Non-main scope ignored.
	applyWorkflowMainIterationOffset(map[string]interface{}{"einoScope": "sub", "iteration": 5}, state)
	if state.SegmentMaxIteration != 0 {
		t.Fatalf("SegmentMaxIteration=%d, want 0", state.SegmentMaxIteration)
	}

	// Main scope applies offset.
	applyWorkflowMainIterationOffset(map[string]interface{}{"einoScope": "main", "iteration": 3}, state)
	if state.SegmentMaxIteration != 3 {
		t.Fatalf("SegmentMaxIteration=%d, want 3", state.SegmentMaxIteration)
	}
	if state.Metrics != nil && len(state.Metrics) != 0 {
		t.Fatalf("Metrics=%v, want empty (metrics collected in workflowAgentProgress path)", state.Metrics)
	}
}

func TestIterationNumberFromProgressData(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]interface{}
		want int
	}{
		{name: "int", in: map[string]interface{}{"iteration": 5}, want: 5},
		{name: "int32", in: map[string]interface{}{"iteration": int32(6)}, want: 6},
		{name: "int64", in: map[string]interface{}{"iteration": int64(7)}, want: 7},
		{name: "float64", in: map[string]interface{}{"iteration": float64(8)}, want: 8},
		{name: "float32", in: map[string]interface{}{"iteration": float32(9)}, want: 9},
		{name: "missing", in: map[string]interface{}{}, want: 0},
		{name: "string", in: map[string]interface{}{"iteration": "abc"}, want: 0},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := iterationNumberFromProgressData(tt.in); got != tt.want {
				t.Fatalf("got=%d want=%d", got, tt.want)
			}
		})
	}
}

func TestRunHITLNode_defaultApproved(t *testing.T) {
	node := graphNode{ID: "hitl-1", Type: "hitl", Config: map[string]any{"prompt": "继续?", "reviewer": "reviewer-1"}, Label: "人工确认"}
	state := newWorkflowLocalState(map[string]any{}, "run-hitl")

	var events []string
	args := RunArgs{Progress: func(eventType, _ string, _ interface{}) { events = append(events, eventType) }}
	out, proceed, status, errText := runHITLNode(args, node, state)
	if status != "completed" || !proceed || errText != "" {
		t.Fatalf("status=%q proceed=%v errText=%q", status, proceed, errText)
	}
	if out["approved"] != true {
		t.Fatalf("approved=%v", out["approved"])
	}
	if len(events) == 0 || events[0] != "workflow_hitl_checkpoint" {
		t.Fatalf("events=%v", events)
	}
}

func TestRunHITLNode_rejected(t *testing.T) {
	node := graphNode{ID: "hitl-1", Type: "hitl", Config: map[string]any{"prompt": "继续?", "reviewer": "human"}, Label: "人工确认"}
	state := newWorkflowLocalState(map[string]any{"_hitl_approved": false, "_hitl_comment": "不许"}, "run-hitl")

	out, proceed, status, errText := runHITLNode(RunArgs{}, node, state)
	if status != "failed" || proceed {
		t.Fatalf("status=%q proceed=%v", status, proceed)
	}
	if errText != "不许" {
		t.Fatalf("errText=%q, want comment", errText)
	}
	if out["approved"] != false {
		t.Fatalf("approved=%v", out["approved"])
	}
}

func TestRunHITLNode_rejectedDefaultReason(t *testing.T) {
	node := graphNode{ID: "hitl-1", Type: "hitl", Config: map[string]any{}, Label: "人工确认"}
	state := newWorkflowLocalState(map[string]any{"_hitl_approved": "false"}, "run-hitl")
	_, proceed, status, errText := runHITLNode(RunArgs{}, node, state)
	if status != "failed" || proceed {
		t.Fatalf("status=%q proceed=%v", status, proceed)
	}
	if errText != "人工审批已拒绝" {
		t.Fatalf("errText=%q", errText)
	}
	// default reviewer is "human"
	state2 := newWorkflowLocalState(map[string]any{}, "run-hitl2")
	out, _, _, _ := runHITLNode(RunArgs{}, node, state2)
	if out["reviewer"] != "human" {
		t.Fatalf("reviewer=%v", out["reviewer"])
	}
}
