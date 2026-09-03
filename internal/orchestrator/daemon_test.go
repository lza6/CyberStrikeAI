package orchestrator_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"cyberstrike-ai/internal/orchestrator"
	"cyberstrike-ai/internal/statusboard"
)

// fakeProvider 内存 StatusProvider，测试可动态改 facts。
type fakeProvider struct {
	mu    sync.Mutex
	facts map[string]orchestrator.WorkerFacts
	err   error
}

func (p *fakeProvider) ListWorkerFacts(ctx context.Context) (map[string]orchestrator.WorkerFacts, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return nil, p.err
	}
	out := make(map[string]orchestrator.WorkerFacts, len(p.facts))
	for k, v := range p.facts {
		out[k] = v
	}
	return out, nil
}

func (p *fakeProvider) set(sid string, wf orchestrator.WorkerFacts) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.facts[sid] = wf
}

func (p *fakeProvider) remove(sid string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.facts, sid)
}

// actionRecorder 收集 daemon 发出的 Action。
type actionRecorder struct {
	mu      sync.Mutex
	actions []orchestrator.Action
}

func (r *actionRecorder) handle(ctx context.Context, a orchestrator.Action) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.actions = append(r.actions, a)
}

func (r *actionRecorder) snapshot() []orchestrator.Action {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]orchestrator.Action, len(r.actions))
	copy(out, r.actions)
	return out
}

func (r *actionRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.actions)
}

func activeWorker(projectID string) orchestrator.WorkerFacts {
	return orchestrator.WorkerFacts{
		ProjectID: projectID,
		Session: statusboard.SessionFacts{
			Activity:       statusboard.ActivityActive,
			LastActivityAt: time.Now().UTC(),
			HasSignal:      true,
		},
	}
}

// TestDaemon_PollFirstObservedEmitsStatusChanged 首次观察 → status_changed。
func TestDaemon_PollFirstObservedEmitsStatusChanged(t *testing.T) {
	p := &fakeProvider{facts: make(map[string]orchestrator.WorkerFacts)}
	rec := &actionRecorder{}
	d := orchestrator.NewDaemon(p, rec.handle, orchestrator.OrchestratorConfig{Interval: 50 * time.Millisecond})
	defer d.Stop()

	p.set("s1", activeWorker("proj-1"))
	if err := d.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	actions := rec.snapshot()
	if len(actions) != 1 {
		t.Fatalf("first poll actions = %d, want 1", len(actions))
	}
	if actions[0].Kind != orchestrator.ActionStatusChanged || actions[0].SessionID != "s1" {
		t.Fatalf("action = %+v, want status_changed for s1", actions[0])
	}
	if actions[0].Payload["status"] != string(statusboard.StatusWorking) {
		t.Fatalf("payload status = %v, want working", actions[0].Payload["status"])
	}
}

// TestDaemon_PollNoChangeNoEmit 无变化不重发。
func TestDaemon_PollNoChangeNoEmit(t *testing.T) {
	p := &fakeProvider{facts: make(map[string]orchestrator.WorkerFacts)}
	rec := &actionRecorder{}
	d := orchestrator.NewDaemon(p, rec.handle, orchestrator.OrchestratorConfig{})
	defer d.Stop()

	p.set("s1", activeWorker("proj-1"))
	_ = d.Poll(context.Background())
	first := rec.count()
	// 第二轮无变化。
	_ = d.Poll(context.Background())
	if rec.count() != first {
		t.Fatalf("no-change poll emitted extra actions: %d -> %d", first, rec.count())
	}
}

// TestDaemon_PollTransitionEmitsNudge 状态转移 active→waiting_input → nudge。
func TestDaemon_PollTransitionEmitsNudge(t *testing.T) {
	p := &fakeProvider{facts: make(map[string]orchestrator.WorkerFacts)}
	rec := &actionRecorder{}
	d := orchestrator.NewDaemon(p, rec.handle, orchestrator.OrchestratorConfig{})
	defer d.Stop()

	p.set("s1", activeWorker("proj-1"))
	_ = d.Poll(context.Background())

	// 改为 waiting_input。
	p.set("s1", orchestrator.WorkerFacts{
		ProjectID: "proj-1",
		Session: statusboard.SessionFacts{
			Activity:       statusboard.ActivityWaitingInput,
			LastActivityAt: time.Now().UTC(),
			HasSignal:      true,
		},
	})
	_ = d.Poll(context.Background())
	actions := rec.snapshot()
	if len(actions) != 2 {
		t.Fatalf("actions = %d, want 2 (first + transition)", len(actions))
	}
	last := actions[len(actions)-1]
	if last.Kind != orchestrator.ActionNudge {
		t.Fatalf("transition kind = %q, want nudge", last.Kind)
	}
	if last.Payload["from"] != string(statusboard.StatusWorking) || last.Payload["to"] != string(statusboard.StatusNeedsInput) {
		t.Fatalf("transition payload = %v", last.Payload)
	}
}

// TestDaemon_PollWorkerDisappearsEmitsTerminated worker 消失 → terminated。
func TestDaemon_PollWorkerDisappearsEmitsTerminated(t *testing.T) {
	p := &fakeProvider{facts: make(map[string]orchestrator.WorkerFacts)}
	rec := &actionRecorder{}
	d := orchestrator.NewDaemon(p, rec.handle, orchestrator.OrchestratorConfig{})
	defer d.Stop()

	p.set("s1", activeWorker("proj-1"))
	_ = d.Poll(context.Background())
	p.remove("s1")
	_ = d.Poll(context.Background())
	actions := rec.snapshot()
	if len(actions) != 2 {
		t.Fatalf("actions = %d, want 2", len(actions))
	}
	last := actions[len(actions)-1]
	if last.Kind != orchestrator.ActionTerminated || last.SessionID != "s1" {
		t.Fatalf("last action = %+v, want terminated s1", last)
	}
}

// TestDaemon_NoSignalTimeout 静默超 grace → timeout action。
func TestDaemon_NoSignalTimeout(t *testing.T) {
	p := &fakeProvider{facts: make(map[string]orchestrator.WorkerFacts)}
	rec := &actionRecorder{}
	const grace = 50 * time.Millisecond
	d := orchestrator.NewDaemon(p, rec.handle, orchestrator.OrchestratorConfig{NoSignalGrace: grace})
	defer d.Stop()

	// SignalExpected=true HasSignal=false，LastActivityAt 在 grace 之前。
	p.set("s1", orchestrator.WorkerFacts{
		ProjectID: "proj-1",
		Session: statusboard.SessionFacts{
			Activity:       statusboard.ActivityIdle,
			LastActivityAt: time.Now().UTC().Add(-2 * grace),
			SignalExpected: true,
		},
	})
	_ = d.Poll(context.Background())
	time.Sleep(2 * grace) // 让静默期确定超限
	// 重新 Poll（首次已记录 idle？——不：首次 poll 时已超 grace，直接 no_signal）。
	// 改为先让首 poll 观察到 idle（在 grace 内），再等超时后第二轮转 no_signal。
	p2 := &fakeProvider{facts: make(map[string]orchestrator.WorkerFacts)}
	rec2 := &actionRecorder{}
	d2 := orchestrator.NewDaemon(p2, rec2.handle, orchestrator.OrchestratorConfig{NoSignalGrace: time.Hour})
	defer d2.Stop()
	p2.set("s1", orchestrator.WorkerFacts{
		ProjectID: "proj-1",
		Session: statusboard.SessionFacts{
			Activity:       statusboard.ActivityIdle,
			LastActivityAt: time.Now().UTC(),
			SignalExpected: true,
		},
	})
	_ = d2.Poll(context.Background()) // 首观察 idle
	// 改 LastActivityAt 到过去 + grace 很小的 d 替代——直接在 p2 上改时间不可行（time.Now 固定）。
	// 用 d（grace=50ms）验证首 poll 直接 no_signal：
	actions := rec.snapshot()
	if len(actions) != 1 {
		t.Fatalf("d actions = %d, want 1", len(actions))
	}
	if actions[0].Payload["status"] != string(statusboard.StatusNoSignal) {
		t.Fatalf("first-poll status = %v, want no_signal (already past grace)", actions[0].Payload["status"])
	}
}

// TestDaemon_StartStopLifecycle Start/Stop 幂等 + goroutine 退出。
func TestDaemon_StartStopLifecycle(t *testing.T) {
	p := &fakeProvider{facts: make(map[string]orchestrator.WorkerFacts)}
	rec := &actionRecorder{}
	d := orchestrator.NewDaemon(p, rec.handle, orchestrator.OrchestratorConfig{Interval: 30 * time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	d.Start(ctx)
	d.Start(ctx) // 幂等
	p.set("s1", activeWorker("proj-1"))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && rec.count() == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if rec.count() == 0 {
		t.Fatal("daemon did not poll after Start")
	}
	cancel()
	d.Stop()
	d.Stop() // 幂等
}

// TestDaemon_HandlerPanicIsolated handler panic 不杀 daemon。
func TestDaemon_HandlerPanicIsolated(t *testing.T) {
	p := &fakeProvider{facts: make(map[string]orchestrator.WorkerFacts)}
	good := &actionRecorder{}
	calls := 0
	handler := func(ctx context.Context, a orchestrator.Action) {
		calls++
		if calls == 1 {
			panic("boom")
		}
		good.handle(ctx, a)
	}
	d := orchestrator.NewDaemon(p, handler, orchestrator.OrchestratorConfig{})
	defer d.Stop()
	p.set("s1", activeWorker("proj-1"))
	if err := d.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 第二轮改状态（新事件仍投给 handler，不再 panic）。
	p.set("s1", orchestrator.WorkerFacts{
		ProjectID: "proj-1",
		Session: statusboard.SessionFacts{
			Activity:       statusboard.ActivityExited,
			LastActivityAt: time.Now().UTC(),
		},
	})
	if err := d.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if good.count() != 1 {
		t.Fatalf("good handler got %d, want 1 (panic not isolated)", good.count())
	}
}

// TestDaemon_ProviderError  provider 报错 → Poll 返回错误，不 emit。
func TestDaemon_ProviderError(t *testing.T) {
	p := &fakeProvider{facts: make(map[string]orchestrator.WorkerFacts), err: context.DeadlineExceeded}
	rec := &actionRecorder{}
	d := orchestrator.NewDaemon(p, rec.handle, orchestrator.OrchestratorConfig{})
	defer d.Stop()
	if err := d.Poll(context.Background()); err == nil {
		t.Fatal("provider error should propagate")
	}
	if rec.count() != 0 {
		t.Fatalf("error poll emitted %d actions, want 0", rec.count())
	}
}

// TestDaemon_Snapshot 验证 Snapshot 返回副本。
func TestDaemon_Snapshot(t *testing.T) {
	p := &fakeProvider{facts: make(map[string]orchestrator.WorkerFacts)}
	rec := &actionRecorder{}
	d := orchestrator.NewDaemon(p, rec.handle, orchestrator.OrchestratorConfig{})
	defer d.Stop()
	p.set("s1", activeWorker("proj-1"))
	_ = d.Poll(context.Background())
	snap := d.Snapshot()
	if len(snap) != 1 || snap["s1"].Status != string(statusboard.StatusWorking) {
		t.Fatalf("snapshot = %+v", snap)
	}
}
