package reactions

import (
	"context"
	"testing"
	"time"

	"cyberstrike-ai/internal/blackboard"
	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/pluginslot"

	"go.uber.org/zap"
)

// TestE2EReactionsFullChain K-E2E：完整链路 E2E。
// 场景：应用启动 reactions 引擎 → 安全事件 Publish 到 blackboard → 引擎订阅匹配规则
// → notify 通道 → fakeNotifier 收到通知。验证 reactions 引擎端到端可用。
//
// 链路：blackboard.Publish → Board.Subscribe → Engine.handleFinding → executeReaction → notify → Notifier
func TestE2EReactionsFullChain(t *testing.T) {
	logger := zap.NewNop()
	board := blackboard.NewMemoryBoard(logger)

	// 用默认 reactions 规则（模拟 config.Load 后 applyDefaultReactions 的结果）。
	cfg := config.ReactionsConfig{Rules: config.DefaultReactionsForTest()}

	// 注册一个 fake notifier（模拟 app.go 从 pluginslot.DetectAvailable 取出 Notifier）。
	fn := &fakeNotifier{}
	eng := New(board, cfg, []pluginslot.Notifier{fn}, logger)
	if eng == nil {
		t.Fatal("engine should not be nil")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	defer eng.Stop()

	// 给引擎 goroutine 启动时间。
	time.Sleep(50 * time.Millisecond)

	// 模拟安全事件：HIGH_IMPACT 工具执行 → app 适配器 Publish 到 blackboard。
	_, err := board.Publish(ctx, blackboard.Finding{
		Type:      "high-impact-tool",
		Title:     "nmap -sV 执行",
		Severity:  "high",
		Source:    "executor",
		ProjectID: "proj-1",
	})
	if err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	// 验证 fakeNotifier 在 2s 内收到通知。
	waitFor(t, func() bool { return len(fn.Events()) >= 1 }, 2*time.Second)
	evs := fn.Events()
	if len(evs) != 1 {
		t.Fatalf("E2E: got %d events, want 1", len(evs))
	}
	ev := evs[0]
	if ev.Type != "high-impact-tool" {
		t.Errorf("E2E: event type = %q, want high-impact-tool", ev.Type)
	}
	if ev.Priority != "urgent" {
		t.Errorf("E2E: priority = %q, want urgent", ev.Priority)
	}
	if ev.ProjectID != "proj-1" {
		t.Errorf("E2E: projectID = %q, want proj-1", ev.ProjectID)
	}
	if _, ok := ev.Data["finding_id"]; !ok {
		t.Errorf("E2E: Data should contain finding_id, got %+v", ev.Data)
	}
}

// TestE2EReactionsScopeViolationEvent scope 拦截事件触发 reaction。
func TestE2EReactionsScopeViolationEvent(t *testing.T) {
	logger := zap.NewNop()
	board := blackboard.NewMemoryBoard(logger)
	cfg := config.ReactionsConfig{Rules: config.DefaultReactionsForTest()}
	fn := &fakeNotifier{}
	eng := New(board, cfg, []pluginslot.Notifier{fn}, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	defer eng.Stop()
	time.Sleep(50 * time.Millisecond)

	board.Publish(ctx, blackboard.Finding{
		Type: "scope-violation", Title: "目标越界", Severity: "warn", Source: "scope_block", ProjectID: "p2",
	})
	waitFor(t, func() bool { return len(fn.Events()) >= 1 }, 2*time.Second)
	if len(fn.Events()) != 1 {
		t.Fatalf("got %d events, want 1", len(fn.Events()))
	}
	if ev := fn.Events()[0]; ev.Type != "scope-violation" || ev.Priority != "urgent" {
		t.Fatalf("E2E scope-violation mismatch: %+v", ev)
	}
}

// TestE2EReactionsCapabilityRollbackEvent capability 回滚事件触发 reaction。
func TestE2EReactionsCapabilityRollbackEvent(t *testing.T) {
	logger := zap.NewNop()
	board := blackboard.NewMemoryBoard(logger)
	cfg := config.ReactionsConfig{Rules: config.DefaultReactionsForTest()}
	fn := &fakeNotifier{}
	eng := New(board, cfg, []pluginslot.Notifier{fn}, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	defer eng.Stop()
	time.Sleep(50 * time.Millisecond)

	board.Publish(ctx, blackboard.Finding{
		Type: "capability-rollback", Title: "modify-file 回滚", Severity: "warning", Source: "capability", ProjectID: "p3",
	})
	waitFor(t, func() bool { return len(fn.Events()) >= 1 }, 2*time.Second)
	evs := fn.Events()
	if len(evs) != 1 {
		t.Fatalf("got %d events, want 1", len(evs))
	}
	if ev := evs[0]; ev.Type != "capability-rollback" || ev.Priority != "warning" {
		t.Fatalf("E2E capability-rollback mismatch: %+v", ev)
	}
}

// TestE2EReactionsMultipleEventsMultipleNotifiers 多事件多 notifier 容错。
func TestE2EReactionsMultipleEventsMultipleNotifiers(t *testing.T) {
	logger := zap.NewNop()
	board := blackboard.NewMemoryBoard(logger)
	cfg := config.ReactionsConfig{Rules: config.DefaultReactionsForTest()}
	fn1 := &fakeNotifier{}
	fn2 := &fakeNotifier{}
	eng := New(board, cfg, []pluginslot.Notifier{fn1, fn2}, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	defer eng.Stop()
	time.Sleep(50 * time.Millisecond)

	// 发 3 个不同事件。
	board.Publish(ctx, blackboard.Finding{Type: "high-impact-tool", Title: "e1", ProjectID: "p"})
	board.Publish(ctx, blackboard.Finding{Type: "scope-violation", Title: "e2", ProjectID: "p"})
	board.Publish(ctx, blackboard.Finding{Type: "capability-rollback", Title: "e3", ProjectID: "p"})

	waitFor(t, func() bool {
		return len(fn1.Events()) >= 3 && len(fn2.Events()) >= 3
	}, 3*time.Second)

	if len(fn1.Events()) != 3 || len(fn2.Events()) != 3 {
		t.Fatalf("each notifier should get 3 events: fn1=%d fn2=%d", len(fn1.Events()), len(fn2.Events()))
	}
}

// TestE2EReactionsStopCancelsSubscription Stop 后不再消费新事件。
func TestE2EReactionsStopCancelsSubscription(t *testing.T) {
	logger := zap.NewNop()
	board := blackboard.NewMemoryBoard(logger)
	cfg := config.ReactionsConfig{Rules: config.DefaultReactionsForTest()}
	fn := &fakeNotifier{}
	eng := New(board, cfg, []pluginslot.Notifier{fn}, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	time.Sleep(50 * time.Millisecond)

	eng.Stop() // 停止消费 goroutine。
	time.Sleep(50 * time.Millisecond)

	board.Publish(ctx, blackboard.Finding{Type: "high-impact-tool", Title: "after-stop"})
	time.Sleep(200 * time.Millisecond)

	if len(fn.Events()) != 0 {
		t.Fatalf("after Stop, no events should be delivered, got %d", len(fn.Events()))
	}
}

// TestE2EReactionsDisabledByConfig enabled=false → 引擎不消费。
func TestE2EReactionsDisabledByConfig(t *testing.T) {
	logger := zap.NewNop()
	board := blackboard.NewMemoryBoard(logger)
	f := false
	cfg := config.ReactionsConfig{Enabled: &f, Rules: config.DefaultReactionsForTest()}
	fn := &fakeNotifier{}
	eng := New(board, cfg, []pluginslot.Notifier{fn}, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx) // enabled=false → 不 Start goroutine
	defer eng.Stop()
	time.Sleep(50 * time.Millisecond)

	board.Publish(ctx, blackboard.Finding{Type: "high-impact-tool", Title: "disabled"})
	time.Sleep(200 * time.Millisecond)
	if len(fn.Events()) != 0 {
		t.Fatalf("disabled engine should not consume, got %d", len(fn.Events()))
	}
}
