package blackboard

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"go.uber.org/zap"
)

// waitGoroutineStable 等待 goroutine 数量相对稳定（给 ctx.Done goroutine 退出时间）。
func waitGoroutineStable(t *testing.T, settle time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	prev := runtime.NumGoroutine()
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		cur := runtime.NumGoroutine()
		if cur == prev {
			return cur
		}
		prev = cur
	}
	return prev
}

// assertGoroutinesReclaimed 断言 goroutine 数回落到基线附近（±2 容忍测试运行器的噪声）。
func assertGoroutinesReclaimed(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		runtime.Gosched()
		cur := runtime.NumGoroutine()
		if cur <= baseline+2 {
			return
		}
	}
	t.Fatalf("goroutine 数未回落：baseline=%d current=%d（订阅 ctx goroutine 泄漏）",
		baseline, runtime.NumGoroutine())
}

// TestMemoryBoardSubscriberCtxCancelReclaims P1-5：MemoryBoard 无 Close 方法
// （app_lifecycle 对 blackboard 的 Close 走 type assertion，MemoryBoard 被跳过），
// 但 Subscribe 的 ctx.Done goroutine 同样需要可回收路径：ctx 带 cancel 时由调用方
// 取消（既有语义），这里验证 ctx cancel 后 goroutine 退出、订阅者从 map 移除。
// SQLiteBoard.Close 的级联 cancel 见 TestSQLiteBoardCloseCancelsSubscriberCtx。
func TestMemoryBoardSubscriberCtxCancelReclaims(t *testing.T) {
	b := NewMemoryBoard(zap.NewNop())
	subCtx, cancel := context.WithCancel(context.Background())
	ch := b.Subscribe(subCtx, 0)
	if ch == nil {
		t.Fatal("Subscribe 返回 nil channel")
	}
	waitGoroutineStable(t, 100*time.Millisecond)
	baseline := waitGoroutineStable(t, 100*time.Millisecond)

	b.mu.Lock()
	subsBefore := len(b.subscribers)
	b.mu.Unlock()
	if subsBefore != 1 {
		t.Fatalf("订阅前 subscribers=%d，want 1", subsBefore)
	}
	cancel()
	assertGoroutinesReclaimed(t, baseline)

	b.mu.Lock()
	subsAfter := len(b.subscribers)
	b.mu.Unlock()
	if subsAfter != 0 {
		t.Fatalf("ctx 取消后 subscribers 应清空，got %d", subsAfter)
	}
}

// TestSQLiteBoardCloseCancelsSubscriberCtx P1-5：SQLiteBoard.Close 后，Subscribe
// 注册的 ctx.Done goroutine（阻塞在 <-ctx.Done()）应被 cancel 唤醒并退出。
func TestSQLiteBoardCloseCancelsSubscriberCtx(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "p1-5.db")
	b, err := NewSQLiteBoard(dbPath, zap.NewNop())
	if err != nil {
		t.Fatalf("NewSQLiteBoard: %v", err)
	}
	ctx := context.Background()
	ch := b.Subscribe(ctx, 0)
	if ch == nil {
		t.Fatal("Subscribe 返回 nil channel")
	}
	waitGoroutineStable(t, 100*time.Millisecond)
	baseline := waitGoroutineStable(t, 100*time.Millisecond)

	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	assertGoroutinesReclaimed(t, baseline)
}

// TestSQLiteBoardCloseCancelsExplicitCtx P1-5 补充：Subscribe 传入带 cancel 的
// ctx 且从不手动 cancel 时，board.Close 同样级联取消，goroutine 退出。
func TestSQLiteBoardCloseCancelsExplicitCtx(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "p1-5b.db")
	b, err := NewSQLiteBoard(dbPath, zap.NewNop())
	if err != nil {
		t.Fatalf("NewSQLiteBoard: %v", err)
	}
	subCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = b.Subscribe(subCtx, 0)

	waitGoroutineStable(t, 100*time.Millisecond)
	baseline := waitGoroutineStable(t, 100*time.Millisecond)

	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	assertGoroutinesReclaimed(t, baseline)
}
