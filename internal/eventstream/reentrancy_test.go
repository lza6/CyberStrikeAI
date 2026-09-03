package eventstream

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestEventStream_ReentrantAddEventNoDeadlock 关键死锁回归测试：
// 订阅者回调内重入 AddEvent（生产场景：Memory 订阅者消费 RecallAction
// 后产出 RecallObservation 再次发布）。无界队列 + 独立广播 goroutine 模型
// 保证 AddEvent 入队即返回，不持有订阅者投递，故无死锁。
// 用 buf=0（旧实现会立即死锁）复现并验证新实现不卡。
func TestEventStream_ReentrantAddEventNoDeadlock(t *testing.T) {
	s := NewEventStream(NewMemoryStore())
	defer s.Close()

	var produced int32
	var observed int32
	// Memory 订阅者：收到 RecallAction 后重入 AddEvent 发布 RecallObservation。
	_, err := s.Subscribe(SubscriberMemory, "mem", 0, func(ev Event) {
		if ev.EventType() == "recall_action" {
			atomic.AddInt32(&observed, 1)
			// 重入：回调内再发布事件（OpenHands memory.py:110,130 的 add_event(obs) 模式）
			obs := &RecallObservation{RecallType: ev.(*RecallAction).RecallType}
			_, _ = s.AddEvent(obs, SourceEnvironment, ev.ID())
		}
	})
	if err != nil {
		t.Fatal(err)
	}

	// 连续发布 N 条 RecallAction，每条都会触发回调内重入 AddEvent。
	// 若死锁，此 for 循环的 AddEvent 会永久阻塞。
	const N = 50
	go func() {
		for i := 0; i < N; i++ {
			atomic.AddInt32(&produced, 1)
			id, err := s.AddEvent(&RecallAction{Query: "q"}, SourceUser, 0)
			if err != nil {
				t.Errorf("AddEvent[%d] err: %v (i=%d)", id, err, i)
				return
			}
		}
	}()

	// 等待所有 RecallAction 被消费 + 对应 RecallObservation 被发布。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&observed) >= N {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&observed); got < N {
		t.Fatalf("重入死锁：仅消费 %d/%d 条 RecallAction（produced=%d）", got, N, atomic.LoadInt32(&produced))
	}
	// 期望总事件数 = N(action) + N(observation) = 2N
	wantTotal := int64(2 * N)
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.LatestEventID() >= wantTotal {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := s.LatestEventID(); got != wantTotal {
		t.Fatalf("期望总事件 %d（action+observation），got %d", wantTotal, got)
	}
}

// TestEventStream_SlowSubscriberDoesNotBlockOthers 慢订阅者不拖累快订阅者。
// 无界队列模型：慢订阅者积压不影响其他订阅者接收事件。
func TestEventStream_SlowSubscriberDoesNotBlockOthers(t *testing.T) {
	s := NewEventStream(NewMemoryStore())
	defer s.Close()

	var fastMu sync.Mutex
	var fastCount int
	// 慢订阅者：每条 sleep 50ms
	slowDone := make(chan struct{})
	_, _ = s.Subscribe(SubscriberTest, "slow", 0, func(ev Event) {
		time.Sleep(50 * time.Millisecond)
		select {
		case <-slowDone:
		default:
		}
	})
	// 快订阅者：立即计数
	_, _ = s.Subscribe(SubscriberAgentController, "fast", 0, func(ev Event) {
		fastMu.Lock()
		fastCount++
		fastMu.Unlock()
	})

	// 发布 5 条事件
	for i := 0; i < 5; i++ {
		_, _ = s.AddEvent(&RecallAction{Query: "x"}, SourceAgent, 0)
	}

	// 快订阅者应很快收到全部 5 条（不等慢订阅者）
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		fastMu.Lock()
		c := fastCount
		fastMu.Unlock()
		if c == 5 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	fastMu.Lock()
	got := fastCount
	fastMu.Unlock()
	if got < 5 {
		t.Fatalf("慢订阅者拖累快订阅者：仅收到 %d/5 条", got)
	}
	close(slowDone)
}
