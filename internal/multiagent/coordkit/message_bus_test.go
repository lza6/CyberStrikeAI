package coordkit

import (
	"strings"
	"sync"
	"testing"
)

func TestMessageBus_SendAndGetUnread(t *testing.T) {
	bus := NewMessageBus()
	bus.Send("coordinator", "worker1", "start A")
	bus.Send("coordinator", "worker2", "start B")

	unread1 := bus.GetUnread("worker1")
	if len(unread1) != 1 || unread1[0].Content != "start A" {
		t.Fatalf("worker1 unread = %+v", unread1)
	}
	unread2 := bus.GetUnread("worker2")
	if len(unread2) != 1 || unread2[0].Content != "start B" {
		t.Fatalf("worker2 unread = %+v", unread2)
	}
	// worker3 has nothing
	if u := bus.GetUnread("worker3"); len(u) != 0 {
		t.Fatalf("worker3 should have 0, got %d", len(u))
	}
}

func TestMessageBus_BroadcastExcludesSender(t *testing.T) {
	bus := NewMessageBus()
	bus.Broadcast("coordinator", "all hands")

	// sender does not receive its own broadcast
	if u := bus.GetUnread("coordinator"); len(u) != 0 {
		t.Fatalf("sender received own broadcast: %+v", u)
	}
	// everyone else does
	if u := bus.GetUnread("worker1"); len(u) != 1 {
		t.Fatalf("worker1 should receive broadcast, got %d", len(u))
	}
	if u := bus.GetUnread("worker2"); len(u) != 1 {
		t.Fatalf("worker2 should receive broadcast, got %d", len(u))
	}
}

func TestMessageBus_MarkRead(t *testing.T) {
	bus := NewMessageBus()
	m1 := bus.Send("a", "b", "msg1")
	bus.Send("a", "b", "msg2")
	if u := bus.GetUnread("b"); len(u) != 2 {
		t.Fatalf("b should have 2 unread, got %d", len(u))
	}
	bus.MarkRead("b", []string{m1.ID})
	if u := bus.GetUnread("b"); len(u) != 1 || u[0].Content != "msg2" {
		t.Fatalf("after markRead, b unread = %+v", u)
	}
	// GetAll still returns both
	if all := bus.GetAll("b"); len(all) != 2 {
		t.Fatalf("b should have 2 total, got %d", len(all))
	}
}

func TestMessageBus_GetConversationExcludesBroadcast(t *testing.T) {
	bus := NewMessageBus()
	bus.Send("a", "b", "p2p-1")
	bus.Broadcast("a", "bc-1")
	bus.Send("b", "a", "p2p-2")
	conv := bus.GetConversation("a", "b")
	if len(conv) != 2 {
		t.Fatalf("conversation should have 2 p2p msgs, got %d", len(conv))
	}
	for _, m := range conv {
		if m.To == "*" {
			t.Errorf("broadcast leaked into conversation: %+v", m)
		}
	}
}

func TestMessageBus_SubscribeSync(t *testing.T) {
	bus := NewMessageBus()
	var got []Message
	var mu sync.Mutex
	unsub := bus.Subscribe("worker", func(m Message) {
		mu.Lock()
		got = append(got, m)
		mu.Unlock()
	})
	bus.Send("coord", "worker", "hello")
	bus.Send("coord", "worker", "world")
	unsub()
	bus.Send("coord", "worker", "after-unsub")

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("expected 2 delivered, got %d (%+v)", len(got), got)
	}
	if got[0].Content != "hello" || got[1].Content != "world" {
		t.Errorf("order/content wrong: %+v", got)
	}
}

func TestMessageBus_SubscribeBroadcast(t *testing.T) {
	bus := NewMessageBus()
	var got []Message
	var mu sync.Mutex
	off := bus.Subscribe("worker", func(m Message) {
		mu.Lock()
		got = append(got, m)
		mu.Unlock()
	})
	bus.Broadcast("coord", "hi all")
	off()

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("worker should get broadcast, got %d", len(got))
	}
	if got[0].From != "coord" || got[0].To != "*" {
		t.Errorf("unexpected msg: %+v", got[0])
	}
}

func TestMessageBus_AllSnapshot(t *testing.T) {
	bus := NewMessageBus()
	bus.Send("a", "b", "1")
	bus.Broadcast("a", "2")
	bus.Send("b", "a", "3")
	all := bus.All()
	if len(all) != 3 {
		t.Fatalf("expected 3 total, got %d", len(all))
	}
}

func TestMessageBus_ConcurrentNoRace(t *testing.T) {
	bus := NewMessageBus()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(3)
		go func(n int) {
			defer wg.Done()
			bus.Send("w1", "w2", strings.Repeat("x", n))
		}(i)
		go func(n int) {
			defer wg.Done()
			bus.Broadcast("w3", strings.Repeat("y", n))
		}(i)
		go func() {
			defer wg.Done()
			bus.GetUnread("w2")
			bus.GetAll("w2")
		}()
	}
	wg.Wait()
	if len(bus.All()) != 100 {
		t.Errorf("expected 100 messages, got %d", len(bus.All()))
	}
}

// TestMessageBus_UnsubscribeIdempotent ensures calling the unsubscribe fn
// twice does not panic or corrupt state.
func TestMessageBus_UnsubscribeIdempotent(t *testing.T) {
	bus := NewMessageBus()
	off := bus.Subscribe("x", func(Message) {})
	off()
	off() // should not panic
	bus.Send("a", "x", "hi")
}
