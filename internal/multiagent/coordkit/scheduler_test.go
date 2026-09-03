// Package coordkit
package coordkit

import (
	"testing"
	"time"
)

func mustLoadSpecs(t *testing.T, specs []TaskSpec) *DAG {
	t.Helper()
	d, err := LoadSpecs(specs)
	if err != nil {
		t.Fatalf("LoadSpecs: %v", err)
	}
	return d
}

// TestSelectRoundRobin round-robin 跨轮游标轮转。
func TestSelectRoundRobin(t *testing.T) {
	dag := mustLoadSpecs(t, []TaskSpec{
		{Title: "A", Desc: "a", Assignee: "scanner"},
		{Title: "B", Desc: "b", Assignee: "enumerator"},
		{Title: "C", Desc: "c", Assignee: "scanner"},
	})
	ready := readyTasksFrom(dag)
	s := NewScheduler(StrategyRoundRobin)
	// 第 1 轮：rrIdx=0 → [A,B,C]，rrIdx→1
	first := s.Select(dag, ready, nil)
	if len(first) != 3 || first[0].Title != "A" {
		t.Fatalf("round 1: got %v", titles(first))
	}
	// 第 2 轮：rrIdx=1 → [B,C,A]，rrIdx→2
	second := s.Select(dag, ready, nil)
	if len(second) != 3 || second[0].Title != "B" {
		t.Fatalf("round 2: got %v", titles(second))
	}
	// 第 3 轮：rrIdx=2 → [C,A,B]，rrIdx→0
	third := s.Select(dag, ready, nil)
	if len(third) != 3 || third[0].Title != "C" {
		t.Fatalf("round 3: got %v", titles(third))
	}
	// 第 4 轮：rrIdx=0 → [A,B,C]
	fourth := s.Select(dag, ready, nil)
	if fourth[0].Title != "A" {
		t.Fatalf("round 4: got %v", titles(fourth))
	}
}

// TestSelectLeastBusy 最少运行数优先。
func TestSelectLeastBusy(t *testing.T) {
	dag := mustLoadSpecs(t, []TaskSpec{
		{Title: "A", Desc: "a", Assignee: "scanner"},
		{Title: "B", Desc: "b", Assignee: "enumerator"},
		{Title: "C", Desc: "c", Assignee: "scanner"},
	})
	ready := readyTasksFrom(dag)
	running := map[string]int{"scanner": 3, "enumerator": 1}
	s := NewScheduler(StrategyLeastBusy)
	out := s.Select(dag, ready, running)
	// enumerator 运行数 1 < scanner 运行数 3，B 应排前。
	if out[0].Title != "B" {
		t.Fatalf("least-busy should pick enumerator first: got %v", titles(out))
	}
}

// TestSelectCapabilityMatch 已在 running 的 assignee 优先。
func TestSelectCapabilityMatch(t *testing.T) {
	dag := mustLoadSpecs(t, []TaskSpec{
		{Title: "A", Desc: "a", Assignee: "scanner"},   // scanner 已 running
		{Title: "B", Desc: "b", Assignee: "enumerator"}, // enumerator 未 running
	})
	ready := readyTasksFrom(dag)
	running := map[string]int{"scanner": 1, "enumerator": 0}
	s := NewScheduler(StrategyCapabilityMatch)
	out := s.Select(dag, ready, running)
	// scanner 已 running，A 应排前。
	if out[0].Title != "A" {
		t.Fatalf("capability-match should prefer already-running scanner: got %v", titles(out))
	}
}

// TestSelectDependencyFirst 阻塞最多后继的优先。
//
// 构造 DAG：
//   Recon -> {Scan, Enum} -> Report
// Recon 阻塞 3 个后继（Scan, Enum, Report），最优先派发。
func TestSelectDependencyFirst(t *testing.T) {
	dag := mustLoadSpecs(t, []TaskSpec{
		{Title: "Recon", Desc: "r", Assignee: "scanner"},
		{Title: "Scan", Desc: "s", Assignee: "scanner", DependsOn: []string{"Recon"}},
		{Title: "Enum", Desc: "e", Assignee: "enumerator", DependsOn: []string{"Recon"}},
		{Title: "Report", Desc: "rep", Assignee: "writer", DependsOn: []string{"Scan", "Enum"}},
	})
	// 全部 pending：IsReady 只对 Recon 为 true（无依赖）。
	var ready []*Task
	for _, t := range dag.Tasks() {
		if dag.IsReady(t) {
			ready = append(ready, t)
		}
	}
	if len(ready) != 1 || ready[0].Title != "Recon" {
		t.Fatalf("expected only Recon ready, got %v", titles(ready))
	}
	s := NewScheduler(StrategyDependencyFirst)
	out := s.Select(dag, ready, nil)
	if len(out) != 1 || out[0].Title != "Recon" {
		t.Fatalf("dependency-first should pick Recon: got %v", titles(out))
	}

	// 辅助断言：countBlockedDependents(Recon)=3（Scan+Enum+Report）。
	if got := countBlockedDependents(dag, ready[0]); got != 3 {
		t.Fatalf("Recon should block 3 successors, got %d", got)
	}
}

// TestSelectDefaultStrategy 空 strategy 走 round-robin（向后兼容）。
func TestSelectDefaultStrategy(t *testing.T) {
	dag := mustLoadSpecs(t, []TaskSpec{
		{Title: "A", Desc: "a", Assignee: "scanner"},
		{Title: "B", Desc: "b", Assignee: "enumerator"},
	})
	ready := readyTasksFrom(dag)
	s := NewScheduler("")
	if s.Strategy() != DefaultSchedulerStrategy {
		t.Fatalf("empty strategy should default to %s, got %s", DefaultSchedulerStrategy, s.Strategy())
	}
	out := s.Select(dag, ready, nil)
	if len(out) != 2 {
		t.Fatalf("default strategy should return all ready, got %d", len(out))
	}
}

// TestSelectEmptyReady 空 ready 不 panic。
func TestSelectEmptyReady(t *testing.T) {
	dag := mustLoadSpecs(t, []TaskSpec{
		{Title: "A", Desc: "a", Assignee: "scanner"},
	})
	s := NewScheduler(StrategyRoundRobin)
	out := s.Select(dag, nil, nil)
	if len(out) != 0 {
		t.Fatalf("empty ready should return empty, got %d", len(out))
	}
}

// TestSelectNilDag nil dag 不 panic（dependency-first 退化为原序）。
func TestSelectNilDag(t *testing.T) {
	tasks := []*Task{
		{ID: "1", Title: "A", Assignee: "scanner"},
		{ID: "2", Title: "B", Assignee: "enumerator"},
	}
	s := NewScheduler(StrategyDependencyFirst)
	out := s.Select(nil, tasks, nil)
	if len(out) != 2 {
		t.Fatalf("nil dag should still return all ready, got %d", len(out))
	}
}

// TestComputeRetryDelayDefaults baseDelay/backoff<=0 时用默认值。
func TestComputeRetryDelayDefaults(t *testing.T) {
	got := ComputeRetryDelay(0, 0, 0, 0)
	if got != time.Second {
		t.Fatalf("attempt=0 with defaults should return 1s, got %v", got)
	}
}

// TestComputeRetryDelayExponential 指数退避随 attempt 增长。
func TestComputeRetryDelayExponential(t *testing.T) {
	base := 100 * time.Millisecond
	backoff := 2.0
	// attempt=0: 100ms * 2^0 = 100ms
	// attempt=1: 100ms * 2^1 = 200ms
	// attempt=2: 100ms * 2^2 = 400ms
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 100 * time.Millisecond},
		{1, 200 * time.Millisecond},
		{2, 400 * time.Millisecond},
		{3, 800 * time.Millisecond},
	}
	for _, c := range cases {
		got := ComputeRetryDelay(base, backoff, c.attempt, RetryBackoffCap)
		if got != c.want {
			t.Fatalf("attempt=%d: want %v, got %v", c.attempt, c.want, got)
		}
	}
}

// TestComputeRetryDelayCap 退避被 cap 截断。
func TestComputeRetryDelayCap(t *testing.T) {
	base := 10 * time.Second
	backoff := 10.0
	cap := 30 * time.Second
	// attempt=0: 10s * 10^0 = 10s
	// attempt=1: 10s * 10^1 = 100s → cap 30s
	got0 := ComputeRetryDelay(base, backoff, 0, cap)
	if got0 != 10*time.Second {
		t.Fatalf("attempt=0: want 10s, got %v", got0)
	}
	got1 := ComputeRetryDelay(base, backoff, 1, cap)
	if got1 != 30*time.Second {
		t.Fatalf("attempt=1 should be capped at 30s, got %v", got1)
	}
	// 后续 attempt 不超过 cap。
	got5 := ComputeRetryDelay(base, backoff, 5, cap)
	if got5 != 30*time.Second {
		t.Fatalf("attempt=5 should stay at cap 30s, got %v", got5)
	}
}

// TestComputeRetryDelayDefaultCap cap<=0 用 RetryBackoffCap（30s）。
func TestComputeRetryDelayDefaultCap(t *testing.T) {
	base := 5 * time.Second
	backoff := 100.0
	// 5s * 100^1 = 500s → 默认 cap 30s。
	got := ComputeRetryDelay(base, backoff, 1, 0)
	if got != RetryBackoffCap {
		t.Fatalf("cap<=0 should use default 30s cap, got %v", got)
	}
}

// TestComputeRetryDelayFloor 延迟不小于 baseDelay（防 backoff<1 时下探）。
func TestComputeRetryDelayFloor(t *testing.T) {
	base := 2 * time.Second
	backoff := 0.5 // 衰减（非典型，但防误配）
	got := ComputeRetryDelay(base, backoff, 3, RetryBackoffCap)
	if got < base {
		t.Fatalf("delay should not fall below baseDelay, got %v < %v", got, base)
	}
}

// readyTasksFrom 返回 dag 中所有 pending 任务（用于测试 Select，跳过 IsReady 依赖检查）。
func readyTasksFrom(dag *DAG) []*Task {
	var ready []*Task
	for _, t := range dag.Tasks() {
		ready = append(ready, t)
	}
	return ready
}

func titles(tasks []*Task) []string {
	out := make([]string, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, t.Title)
	}
	return out
}
