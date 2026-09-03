package cdc_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cyberstrike-ai/internal/eventstream"
	"cyberstrike-ai/internal/statusboard/cdc"
)

// === Source fakes ===

// fakeSource 内存 Source 实现，记录所有 EventsAfter 调用，供 Poller 测试。
type fakeSource struct {
	mu     sync.Mutex
	events map[int64]eventstream.Event
	maxID  int64
	calls  int // EventsAfter 调用计数
}

func newFakeSource(events ...eventstream.Event) *fakeSource {
	m := make(map[int64]eventstream.Event, len(events))
	max := int64(0)
	for _, ev := range events {
		m[ev.ID()] = ev
		if ev.ID() > max {
			max = ev.ID()
		}
	}
	return &fakeSource{events: m, maxID: max}
}

func (s *fakeSource) add(ev eventstream.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events[ev.ID()] = ev
	if ev.ID() > s.maxID {
		s.maxID = ev.ID()
	}
}

func (s *fakeSource) EventsAfter(ctx context.Context, after int64, limit int) (<-chan eventstream.Event, error) {
	s.mu.Lock()
	s.calls++
	var ids []int64
	for id := range s.events {
		if id > after {
			ids = append(ids, id)
		}
	}
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j] < ids[j-1]; j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
	snap := make([]eventstream.Event, 0, len(ids))
	for _, id := range ids {
		snap = append(snap, s.events[id])
	}
	s.mu.Unlock()

	out := make(chan eventstream.Event, 64)
	go func() {
		defer close(out)
		count := 0
		for _, ev := range snap {
			if ctx.Err() != nil {
				return
			}
			if limit > 0 && count >= limit {
				return
			}
			select {
			case <-ctx.Done():
				return
			case out <- ev:
				count++
			}
		}
	}()
	return out, nil
}

func (s *fakeSource) LatestSeq(ctx context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxID, nil
}

// === Event factory ===

// stubEvent 是自包含的测试 Event，直接持有 id/timestamp/source/cause/etype，
// 不依赖 eventstream.BaseEvent 的 unexported assign（跨包可见性问题）。
type stubEvent struct {
	id    int64
	ts    time.Time
	src   eventstream.EventSource
	cause int64
	etype string
	data  string
}

func (e *stubEvent) ID() int64                       { return e.id }
func (e *stubEvent) Timestamp() time.Time            { return e.ts }
func (e *stubEvent) Source() eventstream.EventSource { return e.src }
func (e *stubEvent) Cause() int64                    { return e.cause }
func (e *stubEvent) EventType() string               { return e.etype }

// evSeq 包级递增计数器，保证每个 makeEv 的 ID 唯一且单调。
var evSeq atomic.Int64

// makeEv 构造一条带指定 ID 的 stubEvent（ID 由调用方指定，保证测试可预测）。
func makeEv(id int64, t string) eventstream.Event {
	return &stubEvent{
		id:    id,
		ts:    time.Date(2026, 6, 10, 12, 0, int(id), 0, time.UTC),
		src:   eventstream.SourceAgent,
		cause: 0,
		etype: "recall_action",
		data:  t,
	}
}

// 静态断言 stubEvent 实现 eventstream.Event。
var _ eventstream.Event = (*stubEvent)(nil)

// === Poller tests ===

func TestPoller_PollDrainsAndAdvancesCursor(t *testing.T) {
	src := newFakeSource(makeEv(1, "a"), makeEv(2, "b"), makeEv(3, "c"))
	bc := cdc.NewBroadcaster()
	var mu sync.Mutex
	var got []int64
	bc.Subscribe(func(e eventstream.Event) {
		mu.Lock()
		got = append(got, e.ID())
		mu.Unlock()
	})
	p := cdc.NewPoller(src, bc, cdc.PollerConfig{StartSeq: 0})

	if err := p.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("got %v, want [1 2 3]", got)
	}
	mu.Unlock()
	if got := p.LastSeq(); got != 3 {
		t.Fatalf("LastSeq = %d, want 3", got)
	}

	// 幂等：第二次无新事件，不投递。
	if err := p.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(got) != 3 {
		t.Fatalf("re-poll delivered extra: %d", len(got))
	}
	mu.Unlock()
}

func TestPoller_SeekToHead(t *testing.T) {
	src := newFakeSource(makeEv(1, "a"), makeEv(2, "b"))
	bc := cdc.NewBroadcaster()
	var mu sync.Mutex
	var got []int64
	bc.Subscribe(func(e eventstream.Event) {
		mu.Lock()
		got = append(got, e.ID())
		mu.Unlock()
	})
	p := cdc.NewPoller(src, bc, cdc.PollerConfig{})

	if err := p.SeekToHead(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := p.LastSeq(); got != 2 {
		t.Fatalf("after seek LastSeq = %d, want 2", got)
	}
	if err := p.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(got) != 0 {
		t.Fatalf("post-seek poll delivered history: %v", got)
	}
	mu.Unlock()
	src.add(makeEv(3, "c"))
	if err := p.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(got) != 1 || got[0] != 3 {
		t.Fatalf("post-seek new event poll = %v, want [3]", got)
	}
	mu.Unlock()
}

func TestPoller_StartLiveDelivery(t *testing.T) {
	src := newFakeSource(makeEv(1, "a"))
	bc := cdc.NewBroadcaster()
	var mu sync.Mutex
	var got []int64
	bc.Subscribe(func(e eventstream.Event) {
		mu.Lock()
		got = append(got, e.ID())
		mu.Unlock()
	})
	ctx, cancel := context.WithCancel(context.Background())
	p := cdc.NewPoller(src, bc, cdc.PollerConfig{Interval: 20 * time.Millisecond})
	done := p.Start(ctx)

	src.add(makeEv(2, "b"))
	src.add(makeEv(3, "c"))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		c := len(got)
		mu.Unlock()
		if c >= 3 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 3 {
		t.Fatalf("delivered %d, want 3", len(got))
	}
	for i, id := range got {
		if id != int64(i+1) {
			t.Fatalf("got[%d] = %d, want %d (out-of-order)", i, id, i+1)
		}
	}
}

// === Broadcaster tests ===

func TestBroadcaster_RecoversPanickingSubscriber(t *testing.T) {
	bc := cdc.NewBroadcaster()
	good := 0
	bc.Subscribe(func(eventstream.Event) { panic("boom") })
	bc.Subscribe(func(eventstream.Event) { good++ })

	bc.Publish(makeEv(1, "x"))
	bc.Publish(makeEv(2, "y"))

	if good != 2 {
		t.Fatalf("good subscriber got %d, want 2 (panic was not isolated)", good)
	}
}

func TestBroadcaster_Unsubscribe(t *testing.T) {
	bc := cdc.NewBroadcaster()
	got := 0
	unsub := bc.Subscribe(func(eventstream.Event) { got++ })
	bc.Publish(makeEv(1, "a"))
	unsub()
	bc.Publish(makeEv(2, "b"))
	if got != 1 {
		t.Fatalf("got %d, want 1 (unsubscribe not honored)", got)
	}
}

// === StoreSource adapter tests ===

// fakeMemoryStore 最小 eventstream.Store 内存实现（测试用）。
// SearchEvents 不做类型过滤（IncludeTypes 空=不限），直接投递所有事件。
type fakeMemoryStore struct {
	mu     sync.Mutex
	events map[int64]eventstream.Event
	maxID  int64
}

func (s *fakeMemoryStore) add(ev eventstream.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.events == nil {
		s.events = make(map[int64]eventstream.Event)
	}
	s.events[ev.ID()] = ev
	if ev.ID() > s.maxID {
		s.maxID = ev.ID()
	}
}

func (s *fakeMemoryStore) Append(ev eventstream.Event) error { return nil }
func (s *fakeMemoryStore) GetEvent(id int64) (eventstream.Event, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ev, ok := s.events[id]
	return ev, ok
}
func (s *fakeMemoryStore) LatestEventID() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxID
}
func (s *fakeMemoryStore) SearchEvents(startID int64, f eventstream.Filter) <-chan eventstream.Event {
	s.mu.Lock()
	var ids []int64
	for id := range s.events {
		if id >= startID {
			ids = append(ids, id)
		}
	}
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j] < ids[j-1]; j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
	snap := make([]eventstream.Event, 0, len(ids))
	for _, id := range ids {
		snap = append(snap, s.events[id])
	}
	s.mu.Unlock()
	out := make(chan eventstream.Event, 64)
	go func() {
		defer close(out)
		for _, ev := range snap {
			out <- ev
		}
	}()
	return out
}

// 静态断言 fakeMemoryStore 实现 eventstream.Store。
var _ eventstream.Store = (*fakeMemoryStore)(nil)

func TestStoreSource_AdaptsEventstreamStore(t *testing.T) {
	store := &fakeMemoryStore{}
	store.add(makeEv(1, "a"))
	store.add(makeEv(2, "b"))
	store.add(makeEv(3, "c"))

	src := &cdc.StoreSource{Store: store}
	// EventsAfter(1) 应得 2,3（after=1 不含，从 2 起）。
	ch, err := src.EventsAfter(context.Background(), 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []int64
	for ev := range ch {
		got = append(got, ev.ID())
	}
	if len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("EventsAfter(1) = %v, want [2 3]", got)
	}
	// limit 截断。
	ch2, _ := src.EventsAfter(context.Background(), 0, 1)
	var got2 []int64
	for ev := range ch2 {
		got2 = append(got2, ev.ID())
	}
	if len(got2) != 1 || got2[0] != 1 {
		t.Fatalf("EventsAfter(0, limit=1) = %v, want [1]", got2)
	}
	// LatestSeq。
	seq, err := src.LatestSeq(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if seq != 3 {
		t.Fatalf("LatestSeq = %d, want 3", seq)
	}
}
