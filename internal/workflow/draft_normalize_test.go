package workflow

import (
	"strings"
	"testing"
)

func TestNormalizeLLMNodeConfig_toolWithUnknownNameBecomesAgent(t *testing.T) {
	used := map[string]bool{}
	node := graphNode{
		ID:     "tool-1",
		Type:   "tool",
		Label:  "未知工具",
		Config: map[string]any{"tool_name": "ghost-tool"},
	}
	normalizeLLMNodeConfig("扫描目标", &node, map[string]bool{"nmap": true}, used)
	if node.Type != "agent" {
		t.Fatalf("type=%q, want agent", node.Type)
	}
	if cfgString(node.Config, "missing_tool_name") != "ghost-tool" {
		t.Fatalf("missing_tool_name=%v", node.Config["missing_tool_name"])
	}
	if cfgString(node.Config, "agent_mode") != "eino_single" {
		t.Fatalf("agent_mode=%v", node.Config["agent_mode"])
	}
	if cfgString(node.Config, "output_key") == "" {
		t.Fatal("output_key missing")
	}
}

func TestNormalizeLLMNodeConfig_validToolGetsDefaults(t *testing.T) {
	used := map[string]bool{}
	node := graphNode{
		ID:     "tool-1",
		Type:   "tool",
		Label:  "端口扫描",
		Config: map[string]any{"tool_name": "NMAP"},
	}
	normalizeLLMNodeConfig("扫描", &node, map[string]bool{"nmap": true}, used)
	if node.Type != "tool" {
		t.Fatalf("type=%q, want tool", node.Type)
	}
	if cfgString(node.Config, "arguments") == "" {
		t.Fatal("arguments default missing")
	}
	if cfgString(node.Config, "timeout_seconds") != "120" {
		t.Fatalf("timeout=%v", node.Config["timeout_seconds"])
	}
	if cfgString(node.Config, "output_key") != "nmap_result" {
		t.Fatalf("output_key=%v", node.Config["output_key"])
	}
	if cfgString(node.Config, "join_strategy") != JoinAllMerge {
		t.Fatalf("join_strategy=%v", node.Config["join_strategy"])
	}
}

func TestNormalizeLLMNodeConfig_startHitlConditionEnd(t *testing.T) {
	used := map[string]bool{}
	enabled := map[string]bool{}

	start := graphNode{ID: "start-1", Type: "start", Config: map[string]any{}}
	normalizeLLMNodeConfig("p", &start, enabled, used)
	if !strings.Contains(cfgString(start.Config, "input_keys"), "message") {
		t.Fatalf("input_keys=%v", start.Config["input_keys"])
	}

	hitl := graphNode{ID: "hitl-1", Type: "hitl", Config: map[string]any{}}
	normalizeLLMNodeConfig("审核阶段", &hitl, enabled, used)
	if !strings.Contains(cfgString(hitl.Config, "prompt"), "审核阶段") {
		t.Fatalf("prompt=%v", hitl.Config["prompt"])
	}
	if cfgString(hitl.Config, "reviewer") != "human" {
		t.Fatalf("reviewer=%v", hitl.Config["reviewer"])
	}

	cond := graphNode{ID: "cond-1", Type: "condition", Config: map[string]any{}}
	normalizeLLMNodeConfig("p", &cond, enabled, used)
	if cfgString(cond.Config, "expression") == "" {
		t.Fatal("expression default missing")
	}

	end := graphNode{ID: "end-1", Type: "end", Config: map[string]any{}}
	normalizeLLMNodeConfig("p", &end, enabled, used)
	if cfgString(end.Config, "join_strategy") != JoinAllMerge {
		t.Fatalf("join_strategy=%v", end.Config["join_strategy"])
	}
}

func TestEnsureNodeOutputKey_dedupesAndSanitizes(t *testing.T) {
	used := map[string]bool{}
	n1 := graphNode{ID: "a", Config: map[string]any{"output_key": "共享 键!"}}
	ensureNodeOutputKey(&n1, used, "result")
	n2 := graphNode{ID: "b", Config: map[string]any{"output_key": "共享 键!"}}
	ensureNodeOutputKey(&n2, used, "result")

	k1 := cfgString(n1.Config, "output_key")
	k2 := cfgString(n2.Config, "output_key")
	// sanitizeOutputKey("共享 键!") strips non-ascii/non-alnum chars leaving
	// "result", so the duplicate receives the "_2" suffix.
	if k1 != "result" || k2 != "result_2" {
		t.Fatalf("keys=%q %q, expect result/result_2", k1, k2)
	}
	if strings.ContainsAny(k1, " !") {
		t.Fatalf("key not sanitized: %q", k1)
	}

	empty := graphNode{ID: "", Config: map[string]any{}}
	ensureNodeOutputKey(&empty, used, "")
	// "result"/"result_2" already used, so the fallback also dedupes.
	if got := cfgString(empty.Config, "output_key"); got != "result_3" {
		t.Fatalf("fallback key=%q", got)
	}
}

func TestDraftSlug_variants(t *testing.T) {
	long := draftSlug("Port Scan And Report With Very Long Description Text Here 12345")
	if len(long) > 48 {
		t.Fatalf("slug too long: %q", long)
	}
	if strings.Contains(long, " ") {
		t.Fatalf("slug contains space: %q", long)
	}
	if got := draftSlug("端口扫描"); got == "" {
		t.Fatal("non-ascii prompt should still produce a slug (hash fallback)")
	}
	if got := draftSlug("  ABC-123  "); got != "abc-123" {
		t.Fatalf("slug=%q", got)
	}
}

func TestDraftName_truncatesLongPrompt(t *testing.T) {
	if got := draftName("短名称"); got != "短名称" {
		t.Fatalf("name=%q", got)
	}
	long := strings.Repeat("长", 30)
	got := draftName(long)
	if len([]rune(got)) != 25 { // 22 runes + "..."
		t.Fatalf("name runes=%d", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("name=%q", got)
	}
}

func TestDraftOutputLabel_andBranchHelpers(t *testing.T) {
	if draftOutputLabel(true) != "输出报告" || draftOutputLabel(false) != "输出" {
		t.Fatal("draftOutputLabel mismatch")
	}
	if branchLabel("cond-1", "cond-1") != "是" {
		t.Fatal("branchLabel yes case")
	}
	if branchLabel("start-1", "cond-1") != "" {
		t.Fatal("branchLabel non-condition case")
	}
	if branchConfig("start-1", "cond-1", true) != nil {
		t.Fatal("branchConfig non-condition returns nil")
	}
	yes := branchConfig("cond-1", "cond-1", true)
	if yes == nil || yes["branch"] != "true" {
		t.Fatalf("branchConfig yes=%v", yes)
	}
	no := branchConfig("cond-1", "cond-1", false)
	if no == nil || no["branch"] != "false" {
		t.Fatalf("branchConfig no=%v", no)
	}
}

func TestNormalizeLLMDraft_toolWithMissingNameRepairsGraph(t *testing.T) {
	result := normalizeLLMDraft("运行一个未指定工具的扫描", DraftRequest{
		AvailableTools: []DraftTool{{Key: "nmap", Name: "nmap", Enabled: true}},
	}, llmDraftEnvelope{
		Graph: graphDef{
			Nodes: []graphNode{
				{ID: "start-1", Type: "start", Label: "开始", Config: map[string]any{}},
				{ID: "tool-1", Type: "tool", Label: "扫描", Config: map[string]any{}},
				{ID: "out-1", Type: "output", Label: "输出", Config: map[string]any{}},
			},
			Edges: []graphEdge{
				{ID: "e1", Source: "start-1", Target: "tool-1"},
				{ID: "e2", Source: "tool-1", Target: "out-1"},
			},
		},
	})
	var sawAgent bool
	for _, node := range result.Graph.Nodes {
		if node.ID == "tool-1" && node.Type == "agent" {
			sawAgent = true
		}
	}
	if !sawAgent {
		t.Fatal("tool node without valid tool should be normalized to agent")
	}
}
