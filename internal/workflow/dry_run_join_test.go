package workflow

import (
	"context"
	"strings"
	"testing"
)

func cancelledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestDryRunGraphJSON_toolNodeSimulated(t *testing.T) {
	graph := `{
  "nodes": [
    {"id": "start-1", "type": "start", "label": "开始", "position": {"x": 0, "y": 0}, "config": {}},
    {"id": "tool-1", "type": "tool", "label": "工具", "position": {"x": 0, "y": 80}, "config": {"tool_name": "nmap", "arguments": "{\"host\":\"127.0.0.1\"}", "output_key": "tool_out"}},
    {"id": "out-1", "type": "output", "label": "输出", "position": {"x": 0, "y": 160}, "config": {"output_key": "result", "source_binding": {"from": "outputs", "field": "tool_out"}}}
  ],
  "edges": [
    {"id": "e1", "source": "start-1", "target": "tool-1"},
    {"id": "e2", "source": "tool-1", "target": "out-1"}
  ]
}`
	result, err := DryRunGraphJSON(nilContext(), graph, map[string]any{"message": "go"})
	if err != nil {
		t.Fatalf("DryRunGraphJSON: %v", err)
	}
	// dry-run tool node is simulated and does not write output_key — the output
	// node resolves source_binding from outputs.tool_out which stays unset.
	if got := result.Outputs["result"]; got != "" {
		t.Fatalf("result=%v, want empty (dry-run tool output not bound)", got)
	}
	if len(result.ReplayScript) != 3 {
		t.Fatalf("replay steps=%d", len(result.ReplayScript))
	}
	if result.ReplayScript[1]["type"] != "tool" {
		t.Fatalf("step2=%v", result.ReplayScript[1])
	}
}

func TestDryRunGraphJSON_hitlAndEndNode(t *testing.T) {
	graph := `{
  "nodes": [
    {"id": "start-1", "type": "start", "label": "开始", "position": {"x": 0, "y": 0}, "config": {}},
    {"id": "hitl-1", "type": "hitl", "label": "审批", "position": {"x": 0, "y": 80}, "config": {"prompt": "继续?", "reviewer": "human"}},
    {"id": "out-1", "type": "output", "label": "输出", "position": {"x": -80, "y": 160}, "config": {"output_key": "r1"}},
    {"id": "end-1", "type": "end", "label": "结束", "position": {"x": 80, "y": 160}, "config": {"result_binding": {"from": "previous", "field": "prompt"}}}
  ],
  "edges": [
    {"id": "e1", "source": "start-1", "target": "hitl-1"},
    {"id": "e2", "source": "hitl-1", "target": "out-1"},
    {"id": "e3", "source": "hitl-1", "target": "end-1"}
  ]
}`
	result, err := DryRunGraphJSON(nilContext(), graph, map[string]any{"message": "go"})
	if err != nil {
		t.Fatalf("DryRunGraphJSON: %v", err)
	}
	if len(result.Trace) != 4 {
		t.Fatalf("trace=%d", len(result.Trace))
	}
	last := result.Trace[3]
	if last["status"] != "completed" {
		t.Fatalf("end status=%v", last["status"])
	}
	// end node takes result_binding from previous.prompt; "previous" resolves to
	// the last executed node output — here the output node, so the prompt field
	// is empty. Assert the end envelope is well-formed with its binding applied.
	if out, ok := last["output"].(map[string]any); ok {
		if out["node_type"] != "end" || out["status"] != "completed" {
			t.Fatalf("end output=%v", out)
		}
		if out["output"] != "" {
			t.Fatalf("end output=%v, want empty (previous is the output node)", out["output"])
		}
	} else {
		t.Fatalf("end output type=%T", last["output"])
	}
	if result.ReplayScript == nil || len(result.ReplayScript) != 4 {
		t.Fatalf("replay=%v", result.ReplayScript)
	}
}

func TestDryRunGraphJSON_invalidInputs(t *testing.T) {
	t.Run("bad json", func(t *testing.T) {
		_, err := DryRunGraphJSON(nilContext(), `{nope`, nil)
		if err == nil || !strings.Contains(err.Error(), "解析工作流图失败") {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("no nodes", func(t *testing.T) {
		_, err := DryRunGraphJSON(nilContext(), `{"nodes":[],"edges":[]}`, nil)
		if err == nil {
			t.Fatal("expected error for empty graph")
		}
	})
	t.Run("validation failure", func(t *testing.T) {
		graph := `{
  "nodes": [
    {"id": "tool-1", "type": "tool", "label": "工具", "position": {"x": 0, "y": 0}, "config": {"tool_name": "nmap"}},
    {"id": "out-1", "type": "output", "label": "输出", "position": {"x": 0, "y": 80}, "config": {"output_key": "r"}}
  ],
  "edges": [{"id": "e1", "source": "tool-1", "target": "out-1"}]
}`
		_, err := DryRunGraphJSON(nilContext(), graph, nil)
		if err == nil || !strings.Contains(err.Error(), "开始节点") {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("ctx cancelled", func(t *testing.T) {
		ctx := cancelledContext()
		_, err := DryRunGraphJSON(ctx, linearStartOutputGraph(), nil)
		if err == nil {
			t.Fatal("expected ctx error")
		}
	})
}

func TestDryRunGraphJSON_unknownJoinStrategyFails(t *testing.T) {
	graph := `{
  "nodes": [
    {"id": "start-1", "type": "start", "label": "开始", "position": {"x": 0, "y": 0}, "config": {}},
    {"id": "agent-1", "type": "agent", "label": "A", "position": {"x": -60, "y": 80}, "config": {"instruction": "a", "output_key": "a"}},
    {"id": "agent-2", "type": "agent", "label": "B", "position": {"x": 60, "y": 80}, "config": {"instruction": "b", "output_key": "b"}},
    {"id": "out-1", "type": "output", "label": "输出", "position": {"x": 0, "y": 160}, "config": {"output_key": "r", "join_strategy": "bogus_strategy"}}
  ],
  "edges": [
    {"id": "e1", "source": "start-1", "target": "agent-1"},
    {"id": "e2", "source": "start-1", "target": "agent-2"},
    {"id": "e3", "source": "agent-1", "target": "out-1"},
    {"id": "e4", "source": "agent-2", "target": "out-1"}
  ]
}`
	_, err := DryRunGraphJSON(nilContext(), graph, map[string]any{"message": "m"})
	if err == nil || !strings.Contains(err.Error(), "未知汇聚策略") {
		t.Fatalf("err=%v", err)
	}
}

func TestPrepareNodeInputState_variants(t *testing.T) {
	t.Run("nil runtime", func(t *testing.T) {
		if err := prepareNodeInputState(nil, graphNode{ID: "n"}); err != nil {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("single upstream passthrough", func(t *testing.T) {
		idx := &graphIndex{
			nodes:    map[string]graphNode{"n": {ID: "n"}},
			incoming: map[string][]graphEdge{"n": {{Source: "u1", Target: "n"}}},
		}
		rt := &workflowRuntime{idx: idx, state: newWorkflowLocalState(nil, "r")}
		if err := prepareNodeInputState(rt, graphNode{ID: "n"}); err != nil {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("fail fast aborts on failed upstream", func(t *testing.T) {
		idx := &graphIndex{
			nodes: map[string]graphNode{"n": {ID: "n", Config: map[string]any{"join_strategy": JoinFailFast}}},
			incoming: map[string][]graphEdge{"n": {
				{Source: "u1", Target: "n"},
				{Source: "u2", Target: "n"},
			}},
		}
		state := newWorkflowLocalState(nil, "r")
		state.NodeOutputs["u1"] = map[string]any{"error": "上游失败"}
		state.NodeOutputs["u2"] = map[string]any{"output": "ok"}
		rt := &workflowRuntime{idx: idx, state: state}
		err := prepareNodeInputState(rt, graphNode{ID: "n", Config: map[string]any{"join_strategy": JoinFailFast}})
		if err == nil || !strings.Contains(err.Error(), "fail_fast") {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("unknown strategy aborts", func(t *testing.T) {
		idx := &graphIndex{
			nodes: map[string]graphNode{"n": {ID: "n", Config: map[string]any{"join_strategy": "bogus"}}},
			incoming: map[string][]graphEdge{"n": {
				{Source: "u1", Target: "n"},
				{Source: "u2", Target: "n"},
			}},
		}
		rt := &workflowRuntime{idx: idx, state: newWorkflowLocalState(nil, "r")}
		err := prepareNodeInputState(rt, graphNode{ID: "n", Config: map[string]any{"join_strategy": "bogus"}})
		if err == nil || !strings.Contains(err.Error(), "未知汇聚策略") {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("all merge combines upstreams", func(t *testing.T) {
		idx := &graphIndex{
			nodes: map[string]graphNode{"n": {ID: "n"}},
			incoming: map[string][]graphEdge{"n": {
				{Source: "u1", Target: "n"},
				{Source: "u2", Target: "n"},
			}},
		}
		state := newWorkflowLocalState(nil, "r")
		state.NodeOutputs["u1"] = map[string]any{"output": "a", "kind": "tool"}
		state.NodeOutputs["u2"] = map[string]any{"output": "b"}
		rt := &workflowRuntime{idx: idx, state: state}
		if err := prepareNodeInputState(rt, graphNode{ID: "n"}); err != nil {
			t.Fatalf("err=%v", err)
		}
		merged := rt.state.LastOutput
		if merged["strategy"] != JoinAllMerge {
			t.Fatalf("merged=%v", merged)
		}
	})
	t.Run("no upstream outputs", func(t *testing.T) {
		idx := &graphIndex{
			nodes: map[string]graphNode{"n": {ID: "n"}},
			incoming: map[string][]graphEdge{"n": {
				{Source: "u1", Target: "n"},
				{Source: "u2", Target: "n"},
			}},
		}
		state := newWorkflowLocalState(nil, "r")
		rt := &workflowRuntime{idx: idx, state: state}
		if err := prepareNodeInputState(rt, graphNode{ID: "n"}); err != nil {
			t.Fatalf("err=%v", err)
		}
		if rt.state.LastOutput != nil {
			t.Fatalf("LastOutput=%v", rt.state.LastOutput)
		}
	})
}

func TestIsFailedNodeOutput(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want bool
	}{
		{name: "nil", in: nil, want: false},
		{name: "error field", in: map[string]any{"error": "boom"}, want: true},
		{name: "empty error field", in: map[string]any{"error": "  "}, want: false},
		{name: "is_error true", in: map[string]any{"is_error": true}, want: true},
		{name: "is_error string true", in: map[string]any{"is_error": "TRUE"}, want: true},
		{name: "is_error false", in: map[string]any{"is_error": false}, want: false},
		{name: "clean", in: map[string]any{"output": "ok"}, want: false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := isFailedNodeOutput(tt.in); got != tt.want {
				t.Fatalf("got=%v want=%v", got, tt.want)
			}
		})
	}
}

func TestMergeUpstreamOutputs_lastByCanvasAndEmptyFirst(t *testing.T) {
	last := mergeUpstreamOutputs(JoinLastByCanvas, []map[string]any{
		{"output": "first"},
		{"output": "second"},
	})
	if last["output"] != "second" {
		t.Fatalf("last=%v", last)
	}
	allEmpty := mergeUpstreamOutputs(JoinFirstNonEmpty, []map[string]any{
		{"output": ""},
		{"output": "   "},
	})
	if allEmpty["output"] != "" {
		t.Fatalf("allEmpty=%v", allEmpty)
	}
}

func TestIsEmptyOutputValue(t *testing.T) {
	if !isEmptyOutputValue(nil) {
		t.Fatal("nil is empty")
	}
	if !isEmptyOutputValue("") {
		t.Fatal("empty string is empty")
	}
	if isEmptyOutputValue("x") {
		t.Fatal("x is not empty")
	}
}
