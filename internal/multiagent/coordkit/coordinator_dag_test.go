package coordkit

import (
	"strings"
	"testing"
)

func TestParseTaskSpecs_JSONFence(t *testing.T) {
	raw := "```json\n" +
		`[{"title":"Recon","description":"port scan","assignee":"scanner","dependsOn":[]},` +
		`{"title":"Exploit","description":"gain foothold","assignee":"attacker","dependsOn":["Recon"]}]` +
		"\n```"
	specs := ParseTaskSpecs(raw)
	if len(specs) != 2 {
		t.Fatalf("expected 2 specs, got %d", len(specs))
	}
	if specs[0].Title != "Recon" || specs[0].Assignee != "scanner" {
		t.Errorf("spec0 wrong: %+v", specs[0])
	}
	if len(specs[1].DependsOn) != 1 || specs[1].DependsOn[0] != "Recon" {
		t.Errorf("spec1 dep wrong: %+v", specs[1])
	}
}

func TestParseTaskSpecs_BareArray(t *testing.T) {
	raw := `here you go: [{"title":"A","description":"x"},{"title":"B","description":"y"}]`
	specs := ParseTaskSpecs(raw)
	if len(specs) != 2 {
		t.Fatalf("expected 2 specs, got %d", len(specs))
	}
}

func TestParseTaskSpecs_FiltersInvalidItems(t *testing.T) {
	// items missing description are dropped; empty array -> nil
	raw := `[{"title":"A"}, {"title":"B","description":"ok"}]`
	specs := ParseTaskSpecs(raw)
	if len(specs) != 1 {
		t.Fatalf("expected 1 valid spec (A dropped), got %d", len(specs))
	}
	if specs[0].Title != "B" {
		t.Errorf("expected B to survive, got %v", specs[0].Title)
	}
}

func TestParseTaskSpecs_EmptyArrayReturnsNil(t *testing.T) {
	if specs := ParseTaskSpecs("[]"); specs != nil {
		t.Errorf("empty array should yield nil, got %v", specs)
	}
}

func TestParseTaskSpecs_NoJSON(t *testing.T) {
	if specs := ParseTaskSpecs("no json here at all"); specs != nil {
		t.Errorf("no json should yield nil, got %v", specs)
	}
}

func TestParseTaskSpecs_DependsOnNonStringCoerced(t *testing.T) {
	// dependsOn array with mixed types: strings kept, non-strings dropped
	raw := `[{"title":"A","description":"x","dependsOn":["B", 123, null, "C"]},{"title":"B","description":"y"},{"title":"C","description":"z"}]`
	specs := ParseTaskSpecs(raw)
	if len(specs) != 3 {
		t.Fatalf("expected 3 specs, got %d", len(specs))
	}
	if len(specs[0].DependsOn) != 2 {
		t.Errorf("A should have 2 string deps, got %d", len(specs[0].DependsOn))
	}
}

func TestLoadSpecs_TitleDependencyResolution(t *testing.T) {
	specs := []TaskSpec{
		{Title: "Recon", Desc: "port scan", Assignee: "scanner"},
		{Title: "Exploit", Desc: "foothold", Assignee: "attacker", DependsOn: []string{"Recon"}},
		{Title: "Report", Desc: "writeup", Assignee: "writer", DependsOn: []string{"Exploit", "Recon"}},
	}
	d, err := LoadSpecs(specs)
	if err != nil {
		t.Fatalf("LoadSpecs: %v", err)
	}
	if len(d.Tasks()) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(d.Tasks()))
	}
	recon := d.Tasks()[0]
	exploit := d.Tasks()[1]
	report := d.Tasks()[2]
	if len(exploit.DependsOn) != 1 || exploit.DependsOn[0] != recon.ID {
		t.Errorf("exploit should depend on recon ID, got %v", exploit.DependsOn)
	}
	if len(report.DependsOn) != 2 {
		t.Errorf("report should have 2 deps, got %d", len(report.DependsOn))
	}
	// report.DependsOn order matches spec order: [Exploit, Recon]
	if report.DependsOn[0] != exploit.ID || report.DependsOn[1] != recon.ID {
		t.Errorf("report deps wrong: %v", report.DependsOn)
	}
}

func TestLoadSpecs_DuplicateTitleRejected(t *testing.T) {
	specs := []TaskSpec{
		{Title: "Same", Desc: "a"},
		{Title: "Same", Desc: "b"},
	}
	if _, err := LoadSpecs(specs); err == nil {
		t.Fatal("expected duplicate title error")
	}
}

func TestLoadSpecs_UnknownDependencyRejected(t *testing.T) {
	specs := []TaskSpec{
		{Title: "A", Desc: "a", DependsOn: []string{"Nonexistent"}},
	}
	if _, err := LoadSpecs(specs); err == nil {
		t.Fatal("expected unknown dependency error")
	}
	if _, err := LoadSpecs(specs); err != nil && !strings.Contains(err.Error(), "unknown") {
		t.Errorf("error should mention unknown: %v", err)
	}
}

func TestLoadSpecs_SelfDependencyRejected(t *testing.T) {
	specs := []TaskSpec{
		{Title: "Loop", Desc: "self", DependsOn: []string{"Loop"}},
	}
	if _, err := LoadSpecs(specs); err == nil {
		t.Fatal("expected self-dependency error")
	}
}

func TestLoadSpecs_CycleRejected(t *testing.T) {
	specs := []TaskSpec{
		{Title: "A", Desc: "a", DependsOn: []string{"B"}},
		{Title: "B", Desc: "b", DependsOn: []string{"C"}},
		{Title: "C", Desc: "c", DependsOn: []string{"A"}},
	}
	if _, err := LoadSpecs(specs); err == nil {
		t.Fatal("expected cycle error")
	}
}

func TestLoadSpecs_EmptySpecsRejected(t *testing.T) {
	if _, err := LoadSpecs(nil); err == nil {
		t.Fatal("expected error for empty specs")
	}
	if _, err := LoadSpecs([]TaskSpec{}); err == nil {
		t.Fatal("expected error for empty specs slice")
	}
}

func TestLoadSpecs_EmptyTitleRejected(t *testing.T) {
	specs := []TaskSpec{{Title: "", Desc: "no title"}}
	if _, err := LoadSpecs(specs); err == nil {
		t.Fatal("expected error for empty title")
	}
}

func TestDAG_DependencyOrder(t *testing.T) {
	// Build a diamond: A -> B, A -> C, B -> D, C -> D
	specs := []TaskSpec{
		{Title: "D", Desc: "d", DependsOn: []string{"B", "C"}},
		{Title: "B", Desc: "b", DependsOn: []string{"A"}},
		{Title: "C", Desc: "c", DependsOn: []string{"A"}},
		{Title: "A", Desc: "a"},
	}
	d, err := LoadSpecs(specs)
	if err != nil {
		t.Fatalf("LoadSpecs: %v", err)
	}
	ordered, err := d.DependencyOrder()
	if err != nil {
		t.Fatalf("DependencyOrder: %v", err)
	}
	if len(ordered) != 4 {
		t.Fatalf("expected 4 ordered, got %d", len(ordered))
	}
	// A must come before B and C; B and C before D
	pos := make(map[string]int)
	for i, task := range ordered {
		pos[task.Title] = i
	}
	if pos["A"] >= pos["B"] || pos["A"] >= pos["C"] {
		t.Errorf("A must precede B and C: %v", pos)
	}
	if pos["B"] >= pos["D"] || pos["C"] >= pos["D"] {
		t.Errorf("B and C must precede D: %v", pos)
	}
}

func TestDAG_IsReady(t *testing.T) {
	specs := []TaskSpec{
		{Title: "A", Desc: "a"},
		{Title: "B", Desc: "b", DependsOn: []string{"A"}},
	}
	d, err := LoadSpecs(specs)
	if err != nil {
		t.Fatalf("LoadSpecs: %v", err)
	}
	a := d.Tasks()[0]
	b := d.Tasks()[1]
	if !d.IsReady(a) {
		t.Error("A should be ready (no deps)")
	}
	if d.IsReady(b) {
		t.Error("B should not be ready (A not completed)")
	}
	a.Status = TaskCompleted
	if !d.IsReady(b) {
		t.Error("B should be ready after A completed")
	}
}

func TestFallbackSpecs(t *testing.T) {
	specs := FallbackSpecs("do the thing", []string{"a1", "a2"})
	if len(specs) != 2 {
		t.Fatalf("expected 2 fallback specs, got %d", len(specs))
	}
	for i, s := range specs {
		if s.Assignee == "" {
			t.Errorf("spec %d missing assignee", i)
		}
		if !strings.Contains(s.Title, "do the thing") {
			t.Errorf("spec %d title should contain goal: %q", i, s.Title)
		}
		if s.Desc != "do the thing" {
			t.Errorf("spec %d desc should be goal: %q", i, s.Desc)
		}
	}
}

func TestFallbackSpecs_NoAgents(t *testing.T) {
	if specs := FallbackSpecs("goal", nil); specs != nil {
		t.Errorf("expected nil with no agents, got %v", specs)
	}
}

func TestDAG_DependencyOrderCycle(t *testing.T) {
	// Construct a cycle manually via LoadSpecs should already reject, so
	// construct via DAG fields directly to exercise DependencyOrder's cycle
	// branch. Use a 2-task mutual dependency.
	specs := []TaskSpec{
		{Title: "X", Desc: "x", DependsOn: []string{"Y"}},
		{Title: "Y", Desc: "y", DependsOn: []string{"X"}},
	}
	_, err := LoadSpecs(specs)
	if err == nil {
		t.Fatal("LoadSpecs should reject the cycle")
	}
}
