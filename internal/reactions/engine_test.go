package reactions

import (
	"context"
	"sync"
	"testing"
	"time"

	"cyberstrike-ai/internal/blackboard"
	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/pluginslot"

	"go.uber.org/zap"
)

// fakeNotifier 测试用 Notifier，记录收到的 NotifyEvent。
type fakeNotifier struct {
	mu       sync.Mutex
	events   []pluginslot.NotifyEvent
	failWith error // 非 nil 时 Notify 返回该 error（测试容错）
	delay    time.Duration
	panicky  bool // true 时 Notify panic（测试 recover 兜底）
}

func (f *fakeNotifier) Notify(ev pluginslot.NotifyEvent) error {
	if f.panicky {
		panic("fakeNotifier panic")
	}
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
	return f.failWith
}

func (f *fakeNotifier) Events() []pluginslot.NotifyEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]pluginslot.NotifyEvent, len(f.events))
	copy(out, f.events)
	return out
}

// newTestEngine 构造测试用引擎：memory board + 默认规则 + 1 fake notifier。
func newTestEngine(t *testing.T, rules map[string]config.Reaction) (*Engine, *blackboard.MemoryBoard, *fakeNotifier) {
	t.Helper()
	logger := zap.NewNop()
	board := blackboard.NewMemoryBoard(logger)
	cfg := config.ReactionsConfig{Rules: rules}
	if cfg.Rules == nil {
		cfg.Rules = make(map[string]config.Reaction)
	}
	fn := &fakeNotifier{}
	eng := New(board, cfg, []pluginslot.Notifier{fn}, logger)
	if eng == nil {
		t.Fatal("New returned nil")
	}
	return eng, board, fn
}

// TestEngineNotifyOnMatchingFinding 核心链路：Publish 匹配规则的 finding → notifier 收到通知。
func TestEngineNotifyOnMatchingFinding(t *testing.T) {
	r2 := 3
	rules := map[string]config.Reaction{
		"high-impact-tool": {Auto: true, Action: "notify", Priority: "urgent", Message: "hi"},
		"_unused":          {Auto: true, Action: "notify", Retries: &r2},
	}
	eng, board, fn := newTestEngine(t, rules)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	defer eng.Stop()

	board.Publish(ctx, blackboard.Finding{Type: "high-impact-tool", Title: "nmap ran", ProjectID: "p1"})

	waitFor(t, func() bool { return len(fn.Events()) >= 1 }, 2*time.Second)
	evs := fn.Events()
	if len(evs) != 1 {
		t.Fatalf("got %d events, want 1", len(evs))
	}
	if evs[0].Type != "high-impact-tool" || evs[0].Priority != "urgent" || evs[0].Message != "hi" {
		t.Fatalf("event mismatch: %+v", evs[0])
	}
}

// TestEngineNoRuleLogOnly 未配置规则 → 不触发动作（仅 Debug 日志）。
func TestEngineNoRuleLogOnly(t *testing.T) {
	eng, board, fn := newTestEngine(t, map[string]config.Reaction{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	defer eng.Stop()

	board.Publish(ctx, blackboard.Finding{Type: "unknown-event", Title: "x"})
	time.Sleep(150 * time.Millisecond)
	if len(fn.Events()) != 0 {
		t.Fatalf("unknown event should not notify, got %d", len(fn.Events()))
	}
}

// TestEngineHistoricalFindingIgnored CreatedAt 早于 startedAt 的历史 finding 被忽略。
func TestEngineHistoricalFindingIgnored(t *testing.T) {
	eng, board, fn := newTestEngine(t, map[string]config.Reaction{
		"high-impact-tool": {Auto: true, Action: "notify", Priority: "urgent"},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	defer eng.Stop()

	// 历史 finding：CreatedAt 早于引擎启动。
	old := blackboard.Finding{Type: "high-impact-tool", Title: "old", CreatedAt: time.Now().Add(-1 * time.Hour)}
	board.Publish(ctx, old)
	time.Sleep(200 * time.Millisecond)
	if len(fn.Events()) != 0 {
		t.Fatalf("historical finding should be ignored, got %d", len(fn.Events()))
	}
}

// TestEngineEscalateAfterRetries 重试次数超限 → 升级（priority 改 urgent）。
// 语义对齐参考项目：attempts++ 后判 attempts > retries。retries=1 → 第 1 次正常，第 2 次升级。
func TestEngineEscalateAfterRetries(t *testing.T) {
	r1 := 1
	rules := map[string]config.Reaction{
		"hitl-pending": {Auto: true, Action: "notify", Priority: "warning", Retries: &r1, Message: "pending"},
	}
	eng, board, fn := newTestEngine(t, rules)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	defer eng.Stop()

	// 第 1 次：attempts=1，1 > 1=false → 正常 notify(warning)。
	board.Publish(ctx, blackboard.Finding{Type: "hitl-pending", Title: "pending", ProjectID: "p1"})
	waitFor(t, func() bool { return len(fn.Events()) >= 1 }, 1*time.Second)
	// 第 2 次：attempts=2，2 > 1=true → 升级 notify(urgent)。
	board.Publish(ctx, blackboard.Finding{Type: "hitl-pending", Title: "pending", ProjectID: "p1"})
	waitFor(t, func() bool { return len(fn.Events()) >= 2 }, 1*time.Second)

	evs := fn.Events()
	if len(evs) < 2 {
		t.Fatalf("got %d events, want >=2", len(evs))
	}
	last := evs[len(evs)-1]
	if last.Priority != "urgent" {
		t.Fatalf("2nd event should be escalated to urgent (attempts=2 > retries=1), got %+v", last)
	}
	// 第 1 次应为 warning（未升级）。
	if evs[0].Priority != "warning" {
		t.Fatalf("1st event should be warning, got %+v", evs[0])
	}
}

// TestEngineEscalateAfterDuration 时长超限 → 升级。
func TestEngineEscalateAfterDuration(t *testing.T) {
	rules := map[string]config.Reaction{
		"agent-idle": {Auto: true, Action: "notify", Priority: "warning", EscalateAfter: "100ms", Message: "idle"},
	}
	eng, board, fn := newTestEngine(t, rules)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	defer eng.Stop()

	// 第 1 次：firstTriggered 设为 now，未超 100ms → 不升级（priority=warning）。
	board.Publish(ctx, blackboard.Finding{Type: "agent-idle", Title: "idle1", ProjectID: "p1"})
	waitFor(t, func() bool { return len(fn.Events()) >= 1 }, 1*time.Second)

	// 等 150ms 后再发：since(firstTriggered) > 100ms → 升级 urgent。
	time.Sleep(150 * time.Millisecond)
	board.Publish(ctx, blackboard.Finding{Type: "agent-idle", Title: "idle2", ProjectID: "p1"})
	waitFor(t, func() bool { return len(fn.Events()) >= 2 }, 1*time.Second)

	evs := fn.Events()
	if len(evs) < 2 {
		t.Fatalf("got %d events, want >=2", len(evs))
	}
	last := evs[len(evs)-1]
	if last.Priority != "urgent" {
		t.Fatalf("2nd event after duration threshold should escalate, got %+v", last)
	}
}

// TestEngineAutoFalseNotifyOnly auto=false → 只 notify 不处置（不升级），即使多次也不 escalate。
func TestEngineAutoFalseNotifyOnly(t *testing.T) {
	r2 := 1
	rules := map[string]config.Reaction{
		"run-complete": {Auto: false, Action: "notify", Priority: "info", Retries: &r2, Message: "done"},
	}
	eng, board, fn := newTestEngine(t, rules)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	defer eng.Stop()

	for i := 0; i < 5; i++ {
		board.Publish(ctx, blackboard.Finding{Type: "run-complete", Title: "done", ProjectID: "p1"})
		time.Sleep(50 * time.Millisecond)
	}
	waitFor(t, func() bool { return len(fn.Events()) >= 5 }, 2*time.Second)
	evs := fn.Events()
	for _, ev := range evs {
		if ev.Priority != "info" {
			t.Fatalf("auto=false should never escalate, got priority=%s", ev.Priority)
		}
	}
}

// TestEngineSendToAgentDegradesToNotify send-to-agent 在 CyberStrikeAI 降级为 notify。
func TestEngineSendToAgentDegradesToNotify(t *testing.T) {
	rules := map[string]config.Reaction{
		"hitl-pending": {Auto: true, Action: "send-to-agent", Message: "pending"},
	}
	eng, board, fn := newTestEngine(t, rules)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	defer eng.Stop()

	board.Publish(ctx, blackboard.Finding{Type: "hitl-pending", Title: "p", ProjectID: "p1"})
	waitFor(t, func() bool { return len(fn.Events()) >= 1 }, 1*time.Second)
	if len(fn.Events()) == 0 {
		t.Fatal("send-to-agent should degrade to notify and deliver event")
	}
}

// TestEngineLogOnlyAction log-only action → 不触发 notifier。
func TestEngineLogOnlyAction(t *testing.T) {
	rules := map[string]config.Reaction{
		"tool-not-found": {Auto: true, Action: "log-only"},
	}
	eng, board, fn := newTestEngine(t, rules)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	defer eng.Stop()

	board.Publish(ctx, blackboard.Finding{Type: "tool-not-found", Title: "x", ProjectID: "p1"})
	time.Sleep(150 * time.Millisecond)
	if len(fn.Events()) != 0 {
		t.Fatalf("log-only should not notify, got %d", len(fn.Events()))
	}
}

// TestEngineNoNotifierDegradesToLog 无 notifier → 不 panic，日志兜底。
func TestEngineNoNotifierDegradesToLog(t *testing.T) {
	logger := zap.NewNop()
	board := blackboard.NewMemoryBoard(logger)
	cfg := config.ReactionsConfig{Rules: map[string]config.Reaction{
		"high-impact-tool": {Auto: true, Action: "notify", Priority: "urgent"},
	}}
	eng := New(board, cfg, nil, logger) // 无 notifier
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	defer eng.Stop()

	// 不应 panic，事件被吞（日志兜底）。
	board.Publish(ctx, blackboard.Finding{Type: "high-impact-tool", Title: "x"})
	time.Sleep(100 * time.Millisecond)
}

// TestEngineDisabledConfigNotStart EnabledEffective=false → Start 不起消费 goroutine。
func TestEngineDisabledConfigNotStart(t *testing.T) {
	logger := zap.NewNop()
	board := blackboard.NewMemoryBoard(logger)
	f := false
	cfg := config.ReactionsConfig{Enabled: &f, Rules: map[string]config.Reaction{
		"high-impact-tool": {Auto: true, Action: "notify", Priority: "urgent"},
	}}
	eng := New(board, cfg, []pluginslot.Notifier{&fakeNotifier{}}, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	defer eng.Stop()

	board.Publish(ctx, blackboard.Finding{Type: "high-impact-tool", Title: "x"})
	time.Sleep(100 * time.Millisecond)
	// 引擎未启动，不应消费。
}

// TestEngineNotifierFailureTolerant 单个 notifier 失败不影响（容错，但此处只有 1 个 notifier 时事件丢失可接受）。
func TestEngineNotifierFailureTolerant(t *testing.T) {
	rules := map[string]config.Reaction{
		"high-impact-tool": {Auto: true, Action: "notify", Priority: "urgent"},
	}
	logger := zap.NewNop()
	board := blackboard.NewMemoryBoard(logger)
	cfg := config.ReactionsConfig{Rules: rules}
	fnFail := &fakeNotifier{failWith: errFake{}}
	eng := New(board, cfg, []pluginslot.Notifier{fnFail}, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	defer eng.Stop()

	board.Publish(ctx, blackboard.Finding{Type: "high-impact-tool", Title: "x"})
	time.Sleep(200 * time.Millisecond)
	// 不 panic 即可（notifier 失败被吞）。
}

// TestTrackerKey trackerKey 生成。
func TestTrackerKey(t *testing.T) {
	if got := trackerKey("p1", "x"); got != "p1:x" {
		t.Fatalf("got %q", got)
	}
	if got := trackerKey("", "x"); got != "global:x" {
		t.Fatalf("got %q", got)
	}
}

// TestDeriveSessionStatusToolPendingByType P2-3：tool_pending 用真实 finding Type
// （tool-pending）判定，不再依赖 Detail 含 "pending" 的字符串启发式。
// hitl-pending 等 finding 的 Detail 是 conversationId=xxx 不含 pending，
// 启发式永不命中——本测试同时覆盖：hitl-pending finding 不误派生 tool_pending。
func TestDeriveSessionStatusToolPendingByType(t *testing.T) {
	eng, board, _ := newTestEngine(t, map[string]config.Reaction{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 真实 pending tool_call finding（securityevents 发布的 tool-pending Type）。
	board.Publish(ctx, blackboard.Finding{Type: "tool-pending", Title: "exec running", Detail: "conversationId=c1 tool=exec", ProjectID: "p1"})
	if got := eng.deriveSessionStatus(ctx); got != SessionStatusToolPending {
		t.Fatalf("tool-pending finding should derive tool_pending, got %q", got)
	}
}

// TestDeriveSessionStatusHitlPendingDetailHeuristicRemoved P2-3：Detail 含 "pending"
// 的 finding 不再误派生 tool_pending（启发式已删除，状态由 Type 决定）。
func TestDeriveSessionStatusHitlPendingDetailHeuristicRemoved(t *testing.T) {
	eng, board, _ := newTestEngine(t, map[string]config.Reaction{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Detail 故意含 "pending" 字样，但 Type 不是 tool-pending → 不派生 tool_pending。
	board.Publish(ctx, blackboard.Finding{Type: "hitl-pending", Title: "await approval", Detail: "conversationId=c2 status=pending", ProjectID: "p1"})
	if got := eng.deriveSessionStatus(ctx); got != SessionStatusHITLPending {
		t.Fatalf("hitl-pending should derive hitl_pending (not tool_pending), got %q", got)
	}
}

// TestDeriveSessionStatusPriorityChain P2-3 回归：Type 判定改造后优先级链
// failed > done > hitl_pending > tool_pending > running > idle 不回归。
func TestDeriveSessionStatusPriorityChain(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	t.Run("plain finding derives running", func(t *testing.T) {
		eng, board, _ := newTestEngine(t, map[string]config.Reaction{})
		// 普通 vuln finding（无状态语义，Detail 无 pending 关键字）→ running。
		// 注意：deriveSessionStatus 不做 startedAt 历史过滤（那是 handleFinding 的语义），
		// board 里不能有状态型 finding。
		board.Publish(ctx, blackboard.Finding{Type: "vuln", Title: "plain vuln finding", Detail: "no pending keyword"})
		if got := eng.deriveSessionStatus(ctx); got != SessionStatusRunning {
			t.Fatalf("plain vuln finding should derive running, got %q", got)
		}
	})
	t.Run("stale hitl finding still derives hitl", func(t *testing.T) {
		eng, board, _ := newTestEngine(t, map[string]config.Reaction{})
		// deriveSessionStatus 无时间过滤：board 内存在 hitl-pending 即派生 hitl_pending
		// （与改造前行为一致，验证 Type 判定不引入回归）。
		board.Publish(ctx, blackboard.Finding{Type: "hitl-pending", Title: "old", CreatedAt: time.Now().Add(-1 * time.Hour)})
		if got := eng.deriveSessionStatus(ctx); got != SessionStatusHITLPending {
			t.Fatalf("hitl-pending finding should derive hitl_pending regardless of age, got %q", got)
		}
	})
	t.Run("failed wins over done", func(t *testing.T) {
		eng, board, _ := newTestEngine(t, map[string]config.Reaction{})
		board.Publish(ctx, blackboard.Finding{Type: "run-complete", Title: "done"})
		board.Publish(ctx, blackboard.Finding{Type: "agent-stuck", Title: "stuck"})
		if got := eng.deriveSessionStatus(ctx); got != SessionStatusFailed {
			t.Fatalf("failed should win over done, got %q", got)
		}
	})
	t.Run("hitl wins over tool pending", func(t *testing.T) {
		eng, board, _ := newTestEngine(t, map[string]config.Reaction{})
		board.Publish(ctx, blackboard.Finding{Type: "tool-pending", Title: "exec"})
		board.Publish(ctx, blackboard.Finding{Type: "hitl-pending", Title: "await"})
		if got := eng.deriveSessionStatus(ctx); got != SessionStatusHITLPending {
			t.Fatalf("hitl_pending should win over tool_pending, got %q", got)
		}
	})
	t.Run("done wins over hitl", func(t *testing.T) {
		eng, board, _ := newTestEngine(t, map[string]config.Reaction{})
		board.Publish(ctx, blackboard.Finding{Type: "hitl-pending", Title: "await"})
		board.Publish(ctx, blackboard.Finding{Type: "run-complete", Title: "done"})
		if got := eng.deriveSessionStatus(ctx); got != SessionStatusDone {
			t.Fatalf("done should win over hitl, got %q", got)
		}
	})
}

// TestEngineNilSafe New 返回 nil（board/logger 为空）。
func TestEngineNilSafe(t *testing.T) {
	if got := New(nil, config.ReactionsConfig{}, nil, zap.NewNop()); got != nil {
		t.Fatal("nil board should return nil engine")
	}
	if got := New(blackboard.NewMemoryBoard(zap.NewNop()), config.ReactionsConfig{}, nil, nil); got != nil {
		t.Fatal("nil logger should return nil engine")
	}
}

// waitFor 轮询条件直至超时。
func waitFor(t *testing.T, cond func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("condition not met within %v", timeout)
	}
}

type errFake struct{}

func (errFake) Error() string { return "fake notifier error" }

// TestEngineNotifierPanicRecovered notifier panic 不会崩进程（RC9 修复）。
// 原 notify 对每个 notifier 起 goroutine 无 recover，notifier panic 会崩进程；
// 修复后 callNotifierSafely 用 defer recover 兜底，记 Warn 不崩。
func TestEngineNotifierPanicRecovered(t *testing.T) {
	rules := map[string]config.Reaction{
		"high-impact-tool": {Auto: true, Action: "notify", Priority: "urgent"},
	}
	logger := zap.NewNop()
	board := blackboard.NewMemoryBoard(logger)
	cfg := config.ReactionsConfig{Rules: rules}
	fnPanicky := &fakeNotifier{panicky: true}
	fnOK := &fakeNotifier{}
	eng := New(board, cfg, []pluginslot.Notifier{fnPanicky, fnOK}, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	defer eng.Stop()

	board.Publish(ctx, blackboard.Finding{Type: "high-impact-tool", Title: "panic-test", ProjectID: "p1"})
	// 不应 panic；fnOK 应正常收到通知。
	waitFor(t, func() bool { return len(fnOK.Events()) >= 1 }, 2*time.Second)
	if len(fnOK.Events()) != 1 {
		t.Fatalf("panic notifier 不应影响其他 notifier，fnOK got %d events, want 1", len(fnOK.Events()))
	}
}

// TestEngineNotifierTimeoutNotBlocking notifier 阻塞 5s 以上被超时放弃（RC9 修复）。
// 原 notify 起无超时 goroutine，阻塞 Notify 无限堆积 goroutine；修复后 5s 超时放弃。
func TestEngineNotifierTimeoutNotBlocking(t *testing.T) {
	rules := map[string]config.Reaction{
		"high-impact-tool": {Auto: true, Action: "notify", Priority: "urgent"},
	}
	logger := zap.NewNop()
	board := blackboard.NewMemoryBoard(logger)
	cfg := config.ReactionsConfig{Rules: rules}
	// delay=30s 远超 notifierTimeout=5s，模拟阻塞 notifier。
	fnBlock := &fakeNotifier{delay: 30 * time.Second}
	fnOK := &fakeNotifier{}
	eng := New(board, cfg, []pluginslot.Notifier{fnBlock, fnOK}, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	defer eng.Stop()

	board.Publish(ctx, blackboard.Finding{Type: "high-impact-tool", Title: "timeout-test", ProjectID: "p1"})
	// fnOK 应在 5s 超时窗口内收到通知（fnBlock 被超时放弃不阻塞 fnOK）。
	waitFor(t, func() bool { return len(fnOK.Events()) >= 1 }, 7*time.Second)
	if len(fnOK.Events()) != 1 {
		t.Fatalf("阻塞 notifier 不应阻塞其他 notifier，fnOK got %d events, want 1", len(fnOK.Events()))
	}
}
