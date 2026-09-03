package eventstream

import (
	"context"
	"sync"
)

// MemoryStore 进程内 Store 实现（测试/轻量场景）。
// 线程安全；事件按 ID 升序存储。不持久化（进程重启即失）。
type MemoryStore struct {
	mu     sync.Mutex
	events map[int64]Event
	maxID  int64
}

// NewMemoryStore 构造空 store。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{events: make(map[int64]Event)}
}

// Append 实现 Store。
func (m *MemoryStore) Append(ev Event) error {
	if ev == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	id := ev.ID()
	m.events[id] = ev
	if id > m.maxID {
		m.maxID = id
	}
	return nil
}

// GetEvent 实现 Store。
func (m *MemoryStore) GetEvent(id int64) (Event, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ev, ok := m.events[id]
	return ev, ok
}

// LatestEventID 实现 Store。
func (m *MemoryStore) LatestEventID() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.maxID
}

// SearchEvents 实现 Store。按 ID 升序遍历，匹配过滤器后投递到 channel。
//
// 注意：消费者必须排空返回的 channel（range 到 close），否则生产 goroutine
// 在缓冲填满（64 条）后永久阻塞导致 goroutine 泄漏。Store 接口不支持传 ctx
// 取消（对齐 OpenHands EventStoreABC），如需可取消检索请用 LatestEventID +
// GetEvent 手动遍历。
func (m *MemoryStore) SearchEvents(startID int64, f Filter) <-chan Event {
	out := make(chan Event, 64)
	go func() {
		defer close(out)
		m.mu.Lock()
		ids := make([]int64, 0, len(m.events))
		for id := range m.events {
			if id >= startID {
				ids = append(ids, id)
			}
		}
		m.mu.Unlock()
		// 升序排序（简单插入排序，避免依赖 sort 包；量小够用）。
		for i := 1; i < len(ids); i++ {
			for j := i; j > 0 && ids[j] < ids[j-1]; j-- {
				ids[j], ids[j-1] = ids[j-1], ids[j]
			}
		}
		ctx := context.Background()
		for _, id := range ids {
			m.mu.Lock()
			ev := m.events[id]
			m.mu.Unlock()
			if !f.match(ev) {
				continue
			}
			select {
			case <-ctx.Done():
				return
			case out <- ev:
			}
		}
	}()
	return out
}
