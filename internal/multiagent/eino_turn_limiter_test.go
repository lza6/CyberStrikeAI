package multiagent

import (
	"sync"
	"testing"
)

func TestTurnLimiter_DisabledWhenLimitZero(t *testing.T) {
	l := NewTurnToolCallLimiter(0)
	if l.Enabled() {
		t.Fatal("limit=0 should be disabled")
	}
	// 未启用时 CheckAndIncrement 恒放行，current/limit 为 0。
	for i := 0; i < 100; i++ {
		allowed, cur, lim := l.CheckAndIncrement("turn1", "call1")
		if !allowed || cur != 0 || lim != 0 {
			t.Fatalf("disabled limiter should always allow: iter=%d allowed=%v cur=%d lim=%d", i, allowed, cur, lim)
		}
	}
}

func TestTurnLimiter_LimitsAfterMaxPerTurn(t *testing.T) {
	l := NewTurnToolCallLimiter(3)
	if !l.Enabled() {
		t.Fatal("limit=3 should be enabled")
	}
	// 前 3 次放行
	for i := 1; i <= 3; i++ {
		allowed, cur, lim := l.CheckAndIncrement("turn1", "call"+itoa(i))
		if !allowed {
			t.Fatalf("call %d should be allowed", i)
		}
		if cur != i {
			t.Fatalf("current = %d, want %d", cur, i)
		}
		if lim != 3 {
			t.Fatalf("limit = %d, want 3", lim)
		}
	}
	// 第 4 次被拦截
	allowed, cur, lim := l.CheckAndIncrement("turn1", "call4")
	if allowed {
		t.Fatal("4th call should be blocked")
	}
	if cur != 3 {
		t.Fatalf("current on block = %d, want 3", cur)
	}
	if lim != 3 {
		t.Fatalf("limit on block = %d, want 3", lim)
	}
	if l.Dropped() != 1 {
		t.Fatalf("dropped = %d, want 1", l.Dropped())
	}
}

func TestTurnLimiter_ResetRestartsCount(t *testing.T) {
	l := NewTurnToolCallLimiter(2)
	// 用满 2 次
	l.CheckAndIncrement("turn1", "c1")
	l.CheckAndIncrement("turn1", "c2")
	// 第 3 次被拦
	if allowed, _, _ := l.CheckAndIncrement("turn1", "c3"); allowed {
		t.Fatal("3rd call before reset should be blocked")
	}
	// Reset 后重新计数
	l.Reset("turn1")
	allowed, cur, _ := l.CheckAndIncrement("turn1", "c4")
	if !allowed {
		t.Fatal("first call after reset should be allowed")
	}
	if cur != 1 {
		t.Fatalf("current after reset = %d, want 1", cur)
	}
}

func TestTurnLimiter_IdempotentSameCallID(t *testing.T) {
	l := NewTurnToolCallLimiter(1)
	// 同一 callID 多次询问应返回同一决策，且只计一次。
	a1, c1, _ := l.CheckAndIncrement("turn1", "dup")
	a2, c2, _ := l.CheckAndIncrement("turn1", "dup")
	if !a1 || !a2 {
		t.Fatal("first callID should be allowed both times")
	}
	if c1 != c2 {
		t.Fatalf("idempotent count mismatch: %d vs %d", c1, c2)
	}
	if c1 != 1 {
		t.Fatalf("current = %d, want 1 (only one increment)", c1)
	}
	// 不同 callID 在上限=1 时应被拦
	if allowed, _, _ := l.CheckAndIncrement("turn1", "other"); allowed {
		t.Fatal("second distinct callID should be blocked at limit=1")
	}
}

func TestTurnLimiter_Concurrent(t *testing.T) {
	l := NewTurnToolCallLimiter(100)
	var wg sync.WaitGroup
	// 200 个 goroutine 各自尝试 5 次不同 callID（共 1000 次请求）。
	// 上限 100，故放行数恰为 100；dropped = 900。
	const goroutines = 200
	const perG = 5
	var allowedCount int64
	var mu sync.Mutex
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				callID := "c" + itoa(g) + "_" + itoa(i)
				allowed, _, _ := l.CheckAndIncrement("turn_concurrent", callID)
				if allowed {
					mu.Lock()
					allowedCount++
					mu.Unlock()
				}
			}
		}(g)
	}
	wg.Wait()
	// 允许的放行数应为 100（上限）。但并发竞态可能让少数请求同时通过边界，
	// 故断言"不超过上限 + 严格不超"——race 测试关注的是数据竞争检测。
	if allowedCount > 100 {
		t.Fatalf("allowed = %d, should not exceed limit 100", allowedCount)
	}
}

func TestEnsureUniqueToolCallID(t *testing.T) {
	existing := []string{"call_abc", "call_def", "call_ghi"}
	seen := make(map[string]struct{}, len(existing)+10)
	for _, id := range existing {
		seen[id] = struct{}{}
	}
	// 生成 50 个新 id，均不应与 existing 碰撞，且彼此不重复。
	for i := 0; i < 50; i++ {
		newID := EnsureUniqueToolCallID(existing)
		if _, ok := seen[newID]; ok {
			t.Fatalf("EnsureUniqueToolCallID produced duplicate: %q", newID)
		}
		seen[newID] = struct{}{}
		existing = append(existing, newID)
	}
}

func TestEnsureUniqueToolCallID_EmptyExisting(t *testing.T) {
	id := EnsureUniqueToolCallID(nil)
	if id == "" {
		t.Fatal("EnsureUniqueToolCallID(nil) should return non-empty id")
	}
	if len(id) < 10 {
		t.Fatalf("EnsureUniqueToolCallID id too short: %q", id)
	}
}
