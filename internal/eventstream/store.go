package eventstream

// Store 事件持久化后端接口。移植自 openhands/events/event_store_abc.py:11-112 EventStoreABC。
// 实现可选用 SQLite（推荐，与 CyberStrikeAI 现有 database 一致）或内存。
// nil-safe：EventStream.store=nil 时纯内存不持久化。
type Store interface {
	// Append 追加一条事件。实现负责序列化（JSON）+ 落库。
	// 调用时事件已分配 ID/Timestamp/Source/Cause。
	Append(ev Event) error
	// GetEvent 按 ID 取单条事件。不存在返回 nil。
	GetEvent(id int64) (Event, bool)
	// LatestEventID 最大已持久化事件 ID（用于 EventStream 启动时恢复 curID）。
	LatestEventID() int64
	// SearchEvents 从 startID（含）按过滤器检索事件，返回 channel 供订阅者流式消费。
	// startID=0 表示从头。实现应按 ID 升序投递。
	SearchEvents(startID int64, f Filter) <-chan Event
}

// Filter 事件过滤器。移植自 openhands/events/event_filter.py:9-32 EventFilter。
// 所有字段为"包含"语义（IncludeTypes 取并集，ExcludeTypes 取差集）；
// 字段为零值时不限制该维度。
type Filter struct {
	// IncludeTypes 仅包含这些 EventType（空=不限）。
	IncludeTypes []string
	// ExcludeTypes 排除这些 EventType。
	ExcludeTypes []string
	// Source 仅此来源（空=不限）。
	Source EventSource
}

// match 判断事件是否匹配过滤器。
func (f Filter) match(ev Event) bool {
	if ev == nil {
		return false
	}
	if f.Source != "" && ev.Source() != f.Source {
		return false
	}
	et := ev.EventType()
	if len(f.IncludeTypes) > 0 {
		found := false
		for _, t := range f.IncludeTypes {
			if t == et {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(f.ExcludeTypes) > 0 {
		for _, t := range f.ExcludeTypes {
			if t == et {
				return false
			}
		}
	}
	return true
}
