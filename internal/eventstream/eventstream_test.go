package eventstream

import (
	"sync"
	"testing"
	"time"
)

// TestEventStream_AddEventAssignsID 发布事件后分配单调递增 ID + 时间戳 + source。
func TestEventStream_AddEventAssignsID(t *testing.T) {
	s := NewEventStream(NewMemoryStore())
	defer s.Close()

	var got Event
	cancel, err := s.Subscribe(SubscriberTest, "cb1", 8, func(ev Event) {
		got = ev
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	a := &RecallAction{RecallType: RecallTypeKnowledge, Query: "sqli"}
	id, err := s.AddEvent(a, SourceUser, 0)
	if err != nil {
		t.Fatal(err)
	}
	if id != 1 {
		t.Fatalf("首个事件 ID 应为 1，got %d", id)
	}
	if a.ID() != 1 {
		t.Fatalf("事件 ID 字段应为 1，got %d", a.ID())
	}
	if a.Source() != SourceUser {
		t.Fatalf("Source 应为 user，got %v", a.Source())
	}
	if a.Timestamp().IsZero() {
		t.Fatal("Timestamp 应已分配")
	}
	// 等待订阅者消费
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && got == nil {
		time.Sleep(5 * time.Millisecond)
	}
	if got == nil {
		t.Fatal("订阅者未收到事件")
	}
	if got.ID() != 1 || got.EventType() != "recall_action" {
		t.Fatalf("收到的事件不匹配，got ID=%d type=%q", got.ID(), got.EventType())
	}
}

// TestEventStream_MonotonicID 多事件 ID 单调递增。
func TestEventStream_MonotonicID(t *testing.T) {
	s := NewEventStream(NewMemoryStore())
	defer s.Close()
	for i := 0; i < 5; i++ {
		_, _ = s.AddEvent(&RecallAction{Query: "x"}, SourceAgent, 0)
	}
	if got := s.LatestEventID(); got != 5 {
		t.Fatalf("5 事件后 LatestEventID 应为 5，got %d", got)
	}
}

// TestEventStream_CauseChain cause 链：RecallObservation.Cause=RecallAction.ID。
func TestEventStream_CauseChain(t *testing.T) {
	s := NewEventStream(NewMemoryStore())
	defer s.Close()

	var observed Event
	_, _ = s.Subscribe(SubscriberTest, "obs", 8, func(ev Event) {
		if ev.EventType() == "recall_observation" {
			observed = ev
		}
	})

	// 发布 RecallAction，拿到 id。
	a := &RecallAction{RecallType: RecallTypeKnowledge, Query: "xss"}
	actionID, _ := s.AddEvent(a, SourceUser, 0)
	// 发布 RecallObservation，cause=actionID。
	o := &RecallObservation{RecallType: RecallTypeKnowledge, MicroagentKnowledge: []MicroagentKnowledge{{Name: "xss", Trigger: "xss", Content: "c"}}}
	_, _ = s.AddEvent(o, SourceEnvironment, actionID)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && observed == nil {
		time.Sleep(5 * time.Millisecond)
	}
	if observed == nil {
		t.Fatal("未收到 RecallObservation")
	}
	if observed.Cause() != actionID {
		t.Fatalf("RecallObservation.Cause 应为 %d，got %d", actionID, observed.Cause())
	}
	if observed.EventType() != "recall_observation" {
		t.Fatalf("EventType 应为 recall_observation，got %q", observed.EventType())
	}
}

// TestEventStream_SubscriberOrdering 同订阅者顺序保证。
func TestEventStream_SubscriberOrdering(t *testing.T) {
	s := NewEventStream(NewMemoryStore())
	defer s.Close()

	var mu sync.Mutex
	var seq []int
	_, _ = s.Subscribe(SubscriberTest, "ord", 32, func(ev Event) {
		mu.Lock()
		defer mu.Unlock()
		seq = append(seq, int(ev.ID()))
	})

	for i := 0; i < 10; i++ {
		_, _ = s.AddEvent(&RecallAction{Query: "x"}, SourceAgent, 0)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(seq)
		mu.Unlock()
		if n == 10 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seq) != 10 {
		t.Fatalf("应收到 10 条，got %d", len(seq))
	}
	for i := 0; i < 10; i++ {
		if seq[i] != i+1 {
			t.Fatalf("顺序错误 idx %d: got %d want %d", i, seq[i], i+1)
		}
	}
}

// TestEventStore_Persistence 持久化：新 EventStream 从 Store 恢复 curID。
func TestEventStore_Persistence(t *testing.T) {
	store := NewMemoryStore()
	s1 := NewEventStream(store)
	_, _ = s1.AddEvent(&RecallAction{Query: "a"}, SourceAgent, 0)
	_, _ = s1.AddEvent(&RecallAction{Query: "b"}, SourceAgent, 0)
	s1.Close()

	// 新 stream 从同一 store 恢复，curID 应为 2。
	s2 := NewEventStream(store)
	defer s2.Close()
	if got := s2.LatestEventID(); got != 2 {
		t.Fatalf("恢复后 LatestEventID 应为 2，got %d", got)
	}
	id, _ := s2.AddEvent(&RecallAction{Query: "c"}, SourceAgent, 0)
	if id != 3 {
		t.Fatalf("恢复后新事件 ID 应为 3，got %d", id)
	}
}

// TestStore_SearchEvents 按过滤器检索事件。
func TestStore_SearchEvents(t *testing.T) {
	store := NewMemoryStore()
	s := NewEventStream(store)
	defer s.Close()
	// 发布 3 条 recall_action + 1 条 recall_observation
	_, _ = s.AddEvent(&RecallAction{Query: "a"}, SourceUser, 0)
	_, _ = s.AddEvent(&RecallAction{Query: "b"}, SourceUser, 0)
	_, _ = s.AddEvent(&RecallObservation{RecallType: RecallTypeKnowledge}, SourceEnvironment, 1)
	_, _ = s.AddEvent(&RecallAction{Query: "c"}, SourceUser, 0)

	// 检索 recall_action，startID=2
	out := store.SearchEvents(2, Filter{IncludeTypes: []string{"recall_action"}})
	var got []int64
	for ev := range out {
		got = append(got, ev.ID())
	}
	if len(got) != 2 {
		t.Fatalf("应检索到 2 条 recall_action（id=2,4），got %d", len(got))
	}
	if got[0] != 2 || got[1] != 4 {
		t.Fatalf("检索结果 ID 应为 [2,4]，got %v", got)
	}
}

// TestAddEvent_LoopPrevention 已分配 ID 的事件拒绝发布（防回环）。
func TestAddEvent_LoopPrevention(t *testing.T) {
	s := NewEventStream(NewMemoryStore())
	defer s.Close()
	a := &RecallAction{Query: "x"}
	_, _ = s.AddEvent(a, SourceUser, 0) // 分配 ID=1
	if _, err := s.AddEvent(a, SourceUser, 0); err == nil {
		t.Fatal("已分配 ID 的事件再次发布应返回错误（防回环）")
	}
}

// TestSubscribe_DuplicateCallbackID 重复 callbackID 报错。
func TestSubscribe_DuplicateCallbackID(t *testing.T) {
	s := NewEventStream(NewMemoryStore())
	defer s.Close()
	_, _ = s.Subscribe(SubscriberTest, "dup", 4, func(Event) {})
	if _, err := s.Subscribe(SubscriberTest, "dup", 4, func(Event) {}); err == nil {
		t.Fatal("重复 callbackID 应报错")
	}
}
