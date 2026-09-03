// Package coordkit
package coordkit

import (
	"sync"
)

// Message is a single inter-agent message exchanged on the bus.
//
// Migrated from open-multi-agent-main src/team/messaging.ts Message. The bus
// retains all messages in insertion order for replay/audit and tracks per-
// recipient read state. Broadcasts (To == "*") are delivered to every agent
// except the sender.
type Message struct {
	ID        string
	From      string
	To        string // agent name, or "*" for broadcast
	Content   string
	Timestamp int64 // unix nanoseconds; set by the bus on send
}

// isAddressedTo reports whether message is intended for agentName. Broadcasts
// are addressed to everyone except the sender (mirrors messaging.ts
// isAddressedTo).
func isAddressedTo(m Message, agentName string) bool {
	if m.To == "*" {
		return m.From != agentName
	}
	return m.To == agentName
}

// MessageBus is an in-memory pub/sub for inter-agent communication. It is safe
// for concurrent use.
//
// Migrated from open-multi-agent-main src/team/messaging.ts MessageBus. All
// messages are retained for replay and audit; read state is tracked per
// recipient. Subscribers are notified synchronously after a message is
// persisted.
type MessageBus struct {
	mu          mutex
	messages    []Message
	readState   map[string]map[string]struct{}
	subscribers map[string]map[int]func(Message)
	nextSubID   int
}

// NewMessageBus constructs an empty bus.
func NewMessageBus() *MessageBus {
	return &MessageBus{
		readState:   make(map[string]map[string]struct{}),
		subscribers: make(map[string]map[int]func(Message)),
	}
}

// Send persists a point-to-point message from -> to and notifies subscribers of
// `to`. Returns the persisted message (with ID and timestamp assigned).
func (b *MessageBus) Send(from, to, content string) Message {
	m := Message{
		ID:        newMessageID(),
		From:      from,
		To:        to,
		Content:   content,
		Timestamp: nowNano(),
	}
	b.persist(m)
	return m
}

// Broadcast is equivalent to Send(from, "*", content).
func (b *MessageBus) Broadcast(from, content string) Message {
	return b.Send(from, "*", content)
}

// persist stores the message and fires subscribers synchronously.
func (b *MessageBus) persist(m Message) {
	b.mu.Lock()
	b.messages = append(b.messages, m)
	// Snapshot subscriber callbacks to invoke outside the write lock, so a
	// subscriber that calls back into the bus cannot self-deadlock.
	var cbs []func(Message)
	if m.To == "*" {
		for agent, subs := range b.subscribers {
			if agent == m.From {
				continue
			}
			for _, cb := range subs {
				cbs = append(cbs, cb)
			}
		}
	} else {
		if subs, ok := b.subscribers[m.To]; ok {
			for _, cb := range subs {
				cbs = append(cbs, cb)
			}
		}
	}
	b.mu.Unlock()

	for _, cb := range cbs {
		cb(m)
	}
}

// GetUnread returns messages addressed to agentName that have not been marked
// read, in insertion order.
func (b *MessageBus) GetUnread(agentName string) []Message {
	b.mu.Lock()
	defer b.mu.Unlock()
	read := b.readState[agentName]
	var out []Message
	for _, m := range b.messages {
		if !isAddressedTo(m, agentName) {
			continue
		}
		if read != nil {
			if _, ok := read[m.ID]; ok {
				continue
			}
		}
		out = append(out, m)
	}
	return out
}

// GetAll returns every message addressed to agentName (read or unread) in
// insertion order. Broadcasts addressed to the agent are included.
func (b *MessageBus) GetAll(agentName string) []Message {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []Message
	for _, m := range b.messages {
		if isAddressedTo(m, agentName) {
			out = append(out, m)
		}
	}
	return out
}

// MarkRead marks messageIds as read for agentName. Unknown or already-read IDs
// are a no-op.
func (b *MessageBus) MarkRead(agentName string, messageIDs []string) {
	if len(messageIDs) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	read, ok := b.readState[agentName]
	if !ok {
		read = make(map[string]struct{}, len(messageIDs))
		b.readState[agentName] = read
	}
	for _, id := range messageIDs {
		read[id] = struct{}{}
	}
}

// GetConversation returns all messages exchanged between agent1 and agent2 in
// either direction, in insertion order. Broadcasts are excluded (a broadcast
// has To == "*" and is therefore not addressed to either agent specifically).
func (b *MessageBus) GetConversation(agent1, agent2 string) []Message {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []Message
	for _, m := range b.messages {
		if (m.From == agent1 && m.To == agent2) || (m.From == agent2 && m.To == agent1) {
			out = append(out, m)
		}
	}
	return out
}

// Subscribe registers a callback for messages addressed to agentName. The
// callback is invoked synchronously after each matching message is persisted.
// Returns an unsubscribe function; calling it is idempotent.
func (b *MessageBus) Subscribe(agentName string, callback func(Message)) func() {
	b.mu.Lock()
	subs, ok := b.subscribers[agentName]
	if !ok {
		subs = make(map[int]func(Message))
		b.subscribers[agentName] = subs
	}
	b.nextSubID++
	id := b.nextSubID
	subs[id] = callback
	b.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			if subs, ok := b.subscribers[agentName]; ok {
				delete(subs, id)
				if len(subs) == 0 {
					delete(b.subscribers, agentName)
				}
			}
			b.mu.Unlock()
		})
	}
}

// All returns a snapshot of every message ever sent, in insertion order.
// Intended for audit/replay in tests.
func (b *MessageBus) All() []Message {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Message, len(b.messages))
	copy(out, b.messages)
	return out
}
