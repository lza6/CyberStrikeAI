// Package eventstream 提供进程内的类型化事件总线 + Recall 一等公民——
// 移植自 OpenHands openhands/events/stream.py 的 EventStream pub/sub 体系。
//
// 与 internal/blackboard 的区别：
//   - blackboard = 仅承载 Finding 结构体的进程内 pub/sub（无持久化、无 Event 基类）
//   - eventstream = 通用 Event 类型总线（含 id/timestamp/source/cause）+ Store 持久化 +
//     RecallAction/RecallObservation 一等公民（AgentController 等可订阅消费）
//
// Go 实现差异：用无界队列（container/list + sync.Cond）+ 每订阅者一个独立 goroutine
// 替代 Python 的 queue.Queue + ThreadPoolExecutor(max_workers=1)。AddEvent 入主队列
// 立即返回（非阻塞），独立 broadcastLoop goroutine 出队分发到各订阅者无界队列——彻底
// 解耦，回调内重入 AddEvent 不会死锁（对齐 stream.py 的 _queue.put + _queue_thread）。
// 控制事件（Recall）不丢（无界队列）；高频 delta 不走本总线。
//
// 设计为 leaf 包：只依赖标准库，不反向导入 agent/handler/project/microagent
// （eventstream 自定义 MicroagentKnowledge，与 microagent.Knowledge 同名独立——
// 字段集对齐，改动需三方同步）。避免循环依赖。
package eventstream

import "time"

// EventSource 事件来源。移植自 openhands/events/event.py:9-12 EventSource。
type EventSource string

const (
	SourceAgent       EventSource = "agent"
	SourceUser        EventSource = "user"
	SourceEnvironment EventSource = "environment"
)

// Event 事件接口。移植自 openhands/events/event.py:25-122 Event 基类。
// 实现（RecallAction/RecallObservation 等）嵌入 BaseEvent 获得通用字段。
type Event interface {
	// ID 事件 ID（单调递增，由 EventStream 分配）。INVALID_ID=-1 表示未分配。
	ID() int64
	// Timestamp 分配时的时间戳（UTC RFC3339）。
	Timestamp() time.Time
	// Source 来源。
	Source() EventSource
	// Cause 触发本事件的上游事件 ID（cause 链）。0=无上游。
	Cause() int64
	// EventType 类型标识（如 "recall_action"/"recall_observation"），用于持久化与过滤。
	EventType() string
}

// INVALID_ID 未分配的事件 ID。移植自 event.py:27 INVALID_ID = -1。
const INVALID_ID int64 = -1

// BaseEvent 通用事件基类。实现 Event 接口的通用部分。
// 具体事件类型嵌入本 struct 并实现 EventType()。
type BaseEvent struct {
	id        int64
	timestamp time.Time
	source    EventSource
	cause     int64
}

func (b *BaseEvent) ID() int64            { return b.id }
func (b *BaseEvent) Timestamp() time.Time { return b.timestamp }
func (b *BaseEvent) Source() EventSource  { return b.source }
func (b *BaseEvent) Cause() int64         { return b.cause }

// assign 由 EventStream 在 AddEvent 时调用，分配 id/timestamp/source/cause。
func (b *BaseEvent) assign(id int64, ts time.Time, src EventSource, cause int64) {
	b.id = id
	b.timestamp = ts
	b.source = src
	b.cause = cause
}

// reset 重置为未分配状态（便于复用）。
func (b *BaseEvent) reset() {
	b.id = 0
	b.timestamp = time.Time{}
	b.source = ""
	b.cause = 0
}

// SubscriberID 订阅者标识。移植自 openhands/events/stream.py:23-30 EventStreamSubscriber。
type SubscriberID string

const (
	SubscriberAgentController SubscriberID = "agent_controller"
	SubscriberMemory          SubscriberID = "memory"
	SubscriberServer          SubscriberID = "server"
	SubscriberRuntime         SubscriberID = "runtime"
	SubscriberMain            SubscriberID = "main"
	SubscriberTest            SubscriberID = "test"
)

// CallbackID 同一订阅者内的多回调区分标识。移植自 stream.py subscribe 的 callback_id 参数。
type CallbackID string
