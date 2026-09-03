package workflow

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// builderNode 是测试辅助：快速构造 graphNode。
func builderNode(id, typ string, cfg map[string]any) graphNode {
	return graphNode{ID: id, Type: typ, Label: id, Config: cfg}
}

func TestRunBuiltinNode_startNodeOutputsMessage(t *testing.T) {
	state := newWorkflowLocalState(map[string]any{"message": "hello", "conversationId": "c1", "projectId": "p1"}, "run-start")
	out, proceed, status, errText := runBuiltinNode(context.Background(), RunArgs{}, builderNode("start-1", "start", map[string]any{}), state)
	if status != "completed" || !proceed || errText != "" {
		t.Fatalf("start node: status=%s proceed=%v err=%q", status, proceed, errText)
	}
	if out["node_id"] != "start-1" || out["kind"] != "start" {
		t.Fatalf("start node envelope = %#v", out)
	}
	if got := out["message"]; got != "hello" {
		t.Fatalf("start node message = %v, want hello", got)
	}
}

func TestRunBuiltinNode_conditionNodeEvaluatesExpression(t *testing.T) {
	state := newWorkflowLocalState(map[string]any{"score": 9}, "run-cond")
	state.LastOutput = map[string]any{"output": "ok"}
	out, proceed, status, errText := runBuiltinNode(context.Background(), RunArgs{}, builderNode("cond-1", "condition", map[string]any{
		"expression": "{{inputs.score}} >= 9",
	}), state)
	if status != "completed" || !proceed || errText != "" {
		t.Fatalf("condition node: status=%s proceed=%v err=%q", status, proceed, errText)
	}
	if got := out["matched"]; got != true {
		t.Fatalf("condition matched = %v, want true", got)
	}
}

func TestRunBuiltinNode_outputNodeWritesKey(t *testing.T) {
	state := newWorkflowLocalState(map[string]any{}, "run-out")
	state.LastOutput = map[string]any{"output": "value-1"}
	out, proceed, status, errText := runBuiltinNode(context.Background(), RunArgs{}, builderNode("out-1", "output", map[string]any{
		"output_key": "result",
	}), state)
	if status != "completed" || !proceed || errText != "" {
		t.Fatalf("output node: status=%s proceed=%v err=%q", status, proceed, errText)
	}
	if got := state.Outputs["result"]; got != "value-1" {
		t.Fatalf("output node state.Outputs[result] = %v, want value-1", got)
	}
	if out["output_key"] != "result" {
		t.Fatalf("output node envelope output_key = %v", out["output_key"])
	}
}

func TestRunBuiltinNode_endNodeDoesNotProceed(t *testing.T) {
	state := newWorkflowLocalState(map[string]any{}, "run-end")
	state.LastOutput = map[string]any{"output": "final"}
	_, proceed, status, errText := runBuiltinNode(context.Background(), RunArgs{}, builderNode("end-1", "end", map[string]any{}), state)
	if status != "completed" || proceed || errText != "" {
		t.Fatalf("end node: status=%s proceed=%v err=%q", status, proceed, errText)
	}
}

func TestRunBuiltinNode_hitlNodeApproved(t *testing.T) {
	state := newWorkflowLocalState(map[string]any{"_hitl_approved": true}, "run-hitl-ok")
	state.LastOutput = map[string]any{"output": "请审批"}
	out, proceed, status, errText := runBuiltinNode(context.Background(), RunArgs{}, builderNode("hitl-1", "hitl", map[string]any{
		"prompt": "继续吗？",
	}), state)
	if status != "completed" || !proceed || errText != "" {
		t.Fatalf("hitl approved: status=%s proceed=%v err=%q", status, proceed, errText)
	}
	if got := out["approved"]; got != true {
		t.Fatalf("hitl approved flag = %v, want true", got)
	}
}

func TestRunBuiltinNode_hitlNodeRejectedReturnsFailed(t *testing.T) {
	state := newWorkflowLocalState(map[string]any{"_hitl_approved": false, "_hitl_comment": "no go"}, "run-hitl-no")
	state.LastOutput = map[string]any{"output": "请审批"}
	out, proceed, status, errText := runBuiltinNode(context.Background(), RunArgs{}, builderNode("hitl-1", "hitl", map[string]any{
		"prompt": "继续吗？",
	}), state)
	if status != "failed" || proceed {
		t.Fatalf("hitl rejected: status=%s proceed=%v, want failed/false", status, proceed)
	}
	if errText != "no go" {
		t.Fatalf("hitl rejected errText = %q, want 'no go'", errText)
	}
	if got := out["approved"]; got != false {
		t.Fatalf("hitl rejected approved flag = %v, want false", got)
	}
}

func TestRunBuiltinNode_unknownNodeSkipped(t *testing.T) {
	out, proceed, status, errText := runBuiltinNode(context.Background(), RunArgs{}, builderNode("weird-1", "mystery", map[string]any{}), &WorkflowLocalState{})
	if status != "skipped" || !proceed {
		t.Fatalf("unknown node: status=%s proceed=%v, want skipped/true", status, proceed)
	}
	if errText != "未知节点类型" {
		t.Fatalf("unknown node errText = %q, want '未知节点类型'", errText)
	}
	if out["status"] != "skipped" {
		t.Fatalf("unknown node out status = %v", out["status"])
	}
}

func TestRunBuiltinNode_toolNodeFailsWithoutAgent(t *testing.T) {
	state := newWorkflowLocalState(map[string]any{}, "run-tool")
	state.LastOutput = map[string]any{"output": "target"}
	out, proceed, status, errText := runBuiltinNode(context.Background(), RunArgs{}, builderNode("tool-1", "tool", map[string]any{
		"tool_name": "nmap",
		"arguments": "{}",
	}), state)
	if status != "failed" || proceed {
		t.Fatalf("tool no agent: status=%s proceed=%v, want failed/false", status, proceed)
	}
	if !strings.Contains(errText, "Agent") {
		t.Fatalf("tool no agent errText = %q, want contains 'Agent'", errText)
	}
	if got := out["tool_name"]; got != "nmap" {
		t.Fatalf("tool node envelope tool_name = %v, want nmap", got)
	}
}

func TestRunBuiltinNode_agentNodeFailsWithoutAppCfg(t *testing.T) {
	state := newWorkflowLocalState(map[string]any{}, "run-agent")
	state.LastOutput = map[string]any{"output": "ctx"}
	out, proceed, status, errText := runBuiltinNode(context.Background(), RunArgs{}, builderNode("agent-1", "agent", map[string]any{
		"instruction": "do something",
		"output_key":  "a1",
	}), state)
	if status != "failed" || proceed {
		t.Fatalf("agent no cfg: status=%s proceed=%v, want failed/false", status, proceed)
	}
	if !strings.Contains(errText, "应用配置") && !strings.Contains(errText, "Agent") {
		t.Fatalf("agent no cfg errText = %q", errText)
	}
	if out["kind"] != "agent" {
		t.Fatalf("agent node envelope kind = %v", out["kind"])
	}
}

func TestRunDelayNode_happyPath(t *testing.T) {
	state := newWorkflowLocalState(map[string]any{}, "run-delay")
	out, proceed, status, errText := runBuiltinNode(context.Background(), RunArgs{}, builderNode("delay-1", "delay", map[string]any{
		"duration_ms": "10",
	}), state)
	if status != "completed" || !proceed || errText != "" {
		t.Fatalf("delay happy: status=%s proceed=%v err=%q", status, proceed, errText)
	}
	if got := out["duration_ms"]; got != int64(10) && got != 10 {
		t.Fatalf("delay duration_ms = %v (%T), want 10", got, got)
	}
	if out["kind"] != "delay" {
		t.Fatalf("delay envelope kind = %v", out["kind"])
	}
}

func TestRunDelayNode_missingDurationFails(t *testing.T) {
	state := newWorkflowLocalState(map[string]any{}, "run-delay-2")
	out, proceed, status, errText := runBuiltinNode(context.Background(), RunArgs{}, builderNode("delay-2", "delay", map[string]any{}), state)
	if status != "failed" || proceed {
		t.Fatalf("delay missing: status=%s proceed=%v, want failed/false", status, proceed)
	}
	if !strings.Contains(errText, "duration_ms") {
		t.Fatalf("delay missing errText = %q, want contains 'duration_ms'", errText)
	}
	if out["status"] != "failed" {
		t.Fatalf("delay missing out status = %v", out["status"])
	}
}

func TestRunDelayNode_cancelledByContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 提前取消，保证 select 命中 ctx.Done()
	state := newWorkflowLocalState(map[string]any{}, "run-delay-3")
	out, proceed, status, errText := runBuiltinNode(ctx, RunArgs{}, builderNode("delay-3", "delay", map[string]any{
		"duration_ms": "10000",
	}), state)
	if status != "failed" || proceed {
		t.Fatalf("delay cancel: status=%s proceed=%v, want failed/false", status, proceed)
	}
	if !strings.Contains(errText, "取消") {
		t.Fatalf("delay cancel errText = %q, want contains '取消'", errText)
	}
	if got := out["status"]; got != "cancelled" {
		t.Fatalf("delay cancel out status = %v, want cancelled", got)
	}
}

func TestRunLoopNode_iteratesCountTimes(t *testing.T) {
	state := newWorkflowLocalState(map[string]any{}, "run-loop")
	state.LastOutput = map[string]any{"output": "seed"}
	out, proceed, status, errText := runBuiltinNode(context.Background(), RunArgs{}, builderNode("loop-1", "loop", map[string]any{
		"count":            "3",
		"item_key":         "loop_item",
		"output_key":       "loop_result",
		"body_instruction": "step",
	}), state)
	if status != "completed" || !proceed || errText != "" {
		t.Fatalf("loop count: status=%s proceed=%v err=%q", status, proceed, errText)
	}
	results, ok := state.Outputs["loop_result"].([]any)
	if !ok {
		t.Fatalf("loop results type = %T, want []any", state.Outputs["loop_result"])
	}
	if len(results) != 3 {
		t.Fatalf("loop results len = %d, want 3", len(results))
	}
	if got, _ := out["item_count"].(int); got != 3 {
		t.Fatalf("loop out item_count = %v, want 3", out["item_count"])
	}
}

func TestRunLoopNode_itemsArrayOverridesCount(t *testing.T) {
	state := newWorkflowLocalState(map[string]any{}, "run-loop-items")
	state.LastOutput = map[string]any{"output": "seed"}
	out, _, _, _ := runBuiltinNode(context.Background(), RunArgs{}, builderNode("loop-2", "loop", map[string]any{
		"items":      []any{"a", "b"},
		"output_key": "loop_result",
	}), state)
	results, ok := state.Outputs["loop_result"].([]any)
	if !ok {
		t.Fatalf("loop items results type = %T", state.Outputs["loop_result"])
	}
	if len(results) != 2 {
		t.Fatalf("loop items len = %d, want 2", len(results))
	}
	if got, _ := out["item_count"].(int); got != 2 {
		t.Fatalf("loop items out item_count = %v, want 2", got)
	}
}

func TestRunParallelNode_fanOutAndMerge(t *testing.T) {
	state := newWorkflowLocalState(map[string]any{}, "run-parallel")
	state.LastOutput = map[string]any{"output": "seed"}
	out, proceed, status, errText := runBuiltinNode(context.Background(), RunArgs{}, builderNode("par-1", "parallel", map[string]any{
		"branches":      []any{map[string]any{"instruction": "b1"}, map[string]any{"instruction": "b2"}},
		"output_key":    "par_result",
		"join_strategy": "all_merge",
	}), state)
	if status != "completed" || !proceed || errText != "" {
		t.Fatalf("parallel: status=%s proceed=%v err=%q", status, proceed, errText)
	}
	if _, ok := state.Outputs["par_result"]; !ok {
		t.Fatalf("parallel output_key missing in state.Outputs: %#v", state.Outputs)
	}
	if got, _ := out["branch_count"].(int); got != 2 {
		t.Fatalf("parallel branch_count = %v, want 2", got)
	}
}

func TestRunParallelNode_emptyBranchesFails(t *testing.T) {
	state := newWorkflowLocalState(map[string]any{}, "run-parallel-2")
	out, proceed, status, errText := runBuiltinNode(context.Background(), RunArgs{}, builderNode("par-2", "parallel", map[string]any{}), state)
	if status != "failed" || proceed {
		t.Fatalf("parallel empty: status=%s proceed=%v, want failed/false", status, proceed)
	}
	if !strings.Contains(errText, "branches") {
		t.Fatalf("parallel empty errText = %q, want contains 'branches'", errText)
	}
	if out["status"] != "failed" {
		t.Fatalf("parallel empty out status = %v", out["status"])
	}
}

func TestDryRunNode_delaySimulated(t *testing.T) {
	state := newWorkflowLocalState(map[string]any{}, "dry-delay")
	out, proceed, status, _ := dryRunNode(builderNode("d-1", "delay", map[string]any{"duration_ms": "1000"}), state)
	if status != "simulated" || !proceed {
		t.Fatalf("dry delay: status=%s proceed=%v, want simulated/true", status, proceed)
	}
	if got := out["simulated"]; got != true {
		t.Fatalf("dry delay simulated flag = %v, want true", got)
	}
}

func TestDryRunNode_loopSimulated(t *testing.T) {
	state := newWorkflowLocalState(map[string]any{}, "dry-loop")
	out, proceed, status, _ := dryRunNode(builderNode("l-1", "loop", map[string]any{"count": "2", "output_key": "lr"}), state)
	if status != "simulated" || !proceed {
		t.Fatalf("dry loop: status=%s proceed=%v, want simulated/true", status, proceed)
	}
	if got, _ := out["item_count"].(int); got != 2 {
		t.Fatalf("dry loop item_count = %v, want 2", got)
	}
	if _, ok := state.Outputs["lr"]; !ok {
		t.Fatalf("dry loop state.Outputs[lr] missing")
	}
}

func TestDryRunNode_parallelSimulated(t *testing.T) {
	state := newWorkflowLocalState(map[string]any{}, "dry-par")
	out, proceed, status, _ := dryRunNode(builderNode("p-1", "parallel", map[string]any{
		"branches":   []any{map[string]any{"instruction": "x"}, map[string]any{"instruction": "y"}, map[string]any{"instruction": "z"}},
		"output_key": "pr",
	}), state)
	if status != "simulated" || !proceed {
		t.Fatalf("dry parallel: status=%s proceed=%v, want simulated/true", status, proceed)
	}
	if got, _ := out["branch_count"].(int); got != 3 {
		t.Fatalf("dry parallel branch_count = %v, want 3", got)
	}
}

// TestDryRunGraphJSON_delayLoopParallelE2E 端到端 dry-run：三种新节点在完整图里模拟执行并产出 trace。
func TestDryRunGraphJSON_delayLoopParallelE2E(t *testing.T) {
	graph := `{
  "nodes": [
    {"id": "s", "type": "start", "label": "开始", "position": {"x": 0, "y": 0}, "config": {}},
    {"id": "d", "type": "delay", "label": "延时", "position": {"x": 0, "y": 80}, "config": {"duration_ms": "10"}},
    {"id": "l", "type": "loop", "label": "循环", "position": {"x": 0, "y": 160}, "config": {"count": "2", "output_key": "lr"}},
    {"id": "p", "type": "parallel", "label": "并行", "position": {"x": 0, "y": 240}, "config": {"branches": [{"instruction": "b1"}, {"instruction": "b2"}], "output_key": "pr"}},
    {"id": "o", "type": "output", "label": "输出", "position": {"x": 0, "y": 320}, "config": {"output_key": "result", "source_binding": {"from": "outputs", "field": "pr"}}}
  ],
  "edges": [
    {"id": "e1", "source": "s", "target": "d"},
    {"id": "e2", "source": "d", "target": "l"},
    {"id": "e3", "source": "l", "target": "p"},
    {"id": "e4", "source": "p", "target": "o"}
  ]
}`
	result, err := DryRunGraphJSON(context.Background(), graph, map[string]any{"message": "hello"})
	if err != nil {
		t.Fatalf("DryRunGraphJSON: %v", err)
	}
	if len(result.Trace) != 5 {
		t.Fatalf("trace len = %d, want 5", len(result.Trace))
	}
	statusByNode := map[string]string{}
	for _, step := range result.Trace {
		if id, ok := step["nodeId"].(string); ok {
			statusByNode[id] = fmt.Sprint(step["status"])
		}
	}
	for _, id := range []string{"s", "d", "l", "p", "o"} {
		if statusByNode[id] != "simulated" && statusByNode[id] != "completed" {
			t.Fatalf("node %s status = %q, want simulated/completed", id, statusByNode[id])
		}
	}
	// delay 节点不真 sleep，只标记 simulated
	if statusByNode["d"] != "simulated" {
		t.Fatalf("delay node status = %q, want simulated", statusByNode["d"])
	}
	if _, ok := result.Outputs["lr"]; !ok {
		t.Fatalf("dry-run outputs missing lr: %#v", result.Outputs)
	}
	if _, ok := result.Outputs["pr"]; !ok {
		t.Fatalf("dry-run outputs missing pr: %#v", result.Outputs)
	}
}

func TestValidateNodeConfigs_delayRequiresPositiveDuration(t *testing.T) {
	tests := []struct {
		name    string
		cfg     map[string]any
		wantErr string
	}{
		{"missing", map[string]any{}, "duration_ms"},
		{"zero", map[string]any{"duration_ms": "0"}, "duration_ms"},
		{"negative", map[string]any{"duration_ms": -5}, "duration_ms"},
		{"valid int", map[string]any{"duration_ms": 100}, ""},
		{"valid string", map[string]any{"duration_ms": "500"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &graphDef{
				Nodes: []graphNode{
					{ID: "s", Type: "start", Position: graphPosition{X: 0, Y: 0}, Config: map[string]any{}},
					{ID: "d", Type: "delay", Position: graphPosition{X: 0, Y: 80}, Config: tt.cfg},
					{ID: "o", Type: "output", Position: graphPosition{X: 0, Y: 160}, Config: map[string]any{"output_key": "r"}},
				},
				Edges: []graphEdge{
					{ID: "e1", Source: "s", Target: "d"},
					{ID: "e2", Source: "d", Target: "o"},
				},
			}
			idx := indexGraph(g)
			err := validateNodeConfigs(idx)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected no error, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected validation error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestValidateNodeConfigs_loopRequiresItemsOrCount(t *testing.T) {
	g := &graphDef{
		Nodes: []graphNode{
			{ID: "s", Type: "start", Position: graphPosition{X: 0, Y: 0}, Config: map[string]any{}},
			{ID: "l", Type: "loop", Position: graphPosition{X: 0, Y: 80}, Config: map[string]any{}},
			{ID: "o", Type: "output", Position: graphPosition{X: 0, Y: 160}, Config: map[string]any{"output_key": "r"}},
		},
		Edges: []graphEdge{
			{ID: "e1", Source: "s", Target: "l"},
			{ID: "e2", Source: "l", Target: "o"},
		},
	}
	idx := indexGraph(g)
	err := validateNodeConfigs(idx)
	if err == nil {
		t.Fatal("expected validation error for empty loop config")
	}
	if !strings.Contains(err.Error(), "items/count") {
		t.Fatalf("err = %q, want contains 'items/count'", err.Error())
	}
}

func TestValidateNodeConfigs_parallelRequiresBranches(t *testing.T) {
	g := &graphDef{
		Nodes: []graphNode{
			{ID: "s", Type: "start", Position: graphPosition{X: 0, Y: 0}, Config: map[string]any{}},
			{ID: "p", Type: "parallel", Position: graphPosition{X: 0, Y: 80}, Config: map[string]any{}},
			{ID: "o", Type: "output", Position: graphPosition{X: 0, Y: 160}, Config: map[string]any{"output_key": "r"}},
		},
		Edges: []graphEdge{
			{ID: "e1", Source: "s", Target: "p"},
			{ID: "e2", Source: "p", Target: "o"},
		},
	}
	idx := indexGraph(g)
	err := validateNodeConfigs(idx)
	if err == nil {
		t.Fatal("expected validation error for empty parallel config")
	}
	if !strings.Contains(err.Error(), "branch") {
		t.Fatalf("err = %q, want contains 'branch'", err.Error())
	}
}

func TestValidateGraphJSON_acceptsDelayLoopParallelGraphs(t *testing.T) {
	graphs := map[string]string{
		"delay": `{
  "nodes": [
    {"id": "s", "type": "start", "label": "开始", "position": {"x": 0, "y": 0}, "config": {}},
    {"id": "d", "type": "delay", "label": "延时", "position": {"x": 0, "y": 80}, "config": {"duration_ms": "10"}},
    {"id": "o", "type": "output", "label": "输出", "position": {"x": 0, "y": 160}, "config": {"output_key": "r"}}
  ],
  "edges": [{"id": "e1", "source": "s", "target": "d"}, {"id": "e2", "source": "d", "target": "o"}]
}`,
		"loop": `{
  "nodes": [
    {"id": "s", "type": "start", "label": "开始", "position": {"x": 0, "y": 0}, "config": {}},
    {"id": "l", "type": "loop", "label": "循环", "position": {"x": 0, "y": 80}, "config": {"count": "2", "output_key": "lr"}},
    {"id": "o", "type": "output", "label": "输出", "position": {"x": 0, "y": 160}, "config": {"output_key": "r"}}
  ],
  "edges": [{"id": "e1", "source": "s", "target": "l"}, {"id": "e2", "source": "l", "target": "o"}]
}`,
		"parallel": `{
  "nodes": [
    {"id": "s", "type": "start", "label": "开始", "position": {"x": 0, "y": 0}, "config": {}},
    {"id": "p", "type": "parallel", "label": "并行", "position": {"x": 0, "y": 80}, "config": {"branches": [{"instruction":"b1"}], "output_key": "pr"}},
    {"id": "o", "type": "output", "label": "输出", "position": {"x": 0, "y": 160}, "config": {"output_key": "r"}}
  ],
  "edges": [{"id": "e1", "source": "s", "target": "p"}, {"id": "e2", "source": "p", "target": "o"}]
}`,
	}
	for name, g := range graphs {
		t.Run(name, func(t *testing.T) {
			if err := ValidateGraphJSON(context.Background(), g); err != nil {
				t.Fatalf("ValidateGraphJSON(%s): %v", name, err)
			}
		})
	}
}
