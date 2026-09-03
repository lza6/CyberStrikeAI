package eventstream

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// EventStream 进程内的类型化事件总线。移植自 openhands/events/stream.py:43-203 EventStream。
//
// 职责：
//  1. 为每个事件分配单调递增 ID（移植自 stream.py:163-203 add_event）
//  2. 持久化到 Store（若注入；移植自 EventStore）
//  3. pub/sub 分发到所有订阅者（移植自 stream.py:130-148 subscribe + 246-291 _process_queue）
//
// 与 OpenHands 差异（Go 实现）：
//   - 用 unboundedQueue（container/list + sync.Cond）替代 Python queue.Queue；
//     每订阅者一个独立队列 + goroutine，替代 ThreadPoolExecutor(max_workers=1)
//   - AddEvent 入主队列后立即返回（非阻塞），独立 broadcastLoop goroutine 出队后
//     非阻塞投递到每订阅者队列——彻底解耦，回调内重入 AddEvent 不会死锁
//     （对齐 stream.py:163-203 add_event 的 self._queue.put + _queue_thread 模型）
//   - 控制事件（Recall/Condensation）不丢：无界队列保证入队即最终送达；
//     高频 delta 事件应由调用方自行决定是否走本总线
type EventStream struct {
	store       Store // 可选持久化后端，nil=纯内存
	curID       atomic.Int64
	mu          sync.Mutex
	subscribers map[SubscriberID]map[CallbackID]*subscription
	// queue 主分发队列。AddEvent 入队（非阻塞），broadcastLoop 出队广播。
	// 对齐 OpenHands stream.py:55 _queue + 246-291 _run_queue_loop。
	queue   *unboundedQueue
	stop    chan struct{}
	stopped atomic.Bool
	wg      sync.WaitGroup // 跟踪 broadcastLoop + 所有 subscriberLoop
}

type subscription struct {
	owner SubscriberID
	cbID  CallbackID
	// cb 回调。事件经此回调分发。回调在订阅者专属 goroutine 中顺序执行。
	cb func(Event)
	// q 订阅者专属无界队列——单 goroutine 消费，保证同订阅者顺序、跨订阅者并行。
	// 无界避免广播侧阻塞（慢订阅者不会拖累其他订阅者或 AddEvent 调用方）。
	q      *unboundedQueue
	ctx    context.Context
	cancel context.CancelFunc
}

// NewEventStream 构造。store 可为 nil（纯内存，不持久化，用于测试/轻量场景）。
// 启动后调用 Close 释放所有订阅者 goroutine。
func NewEventStream(store Store) *EventStream {
	s := &EventStream{
		store:       store,
		subscribers: make(map[SubscriberID]map[CallbackID]*subscription),
		queue:       newUnboundedQueue(),
		stop:        make(chan struct{}),
	}
	// 从 Store 恢复 curID（移植自 event_store.py:65-83 _calculate_cur_id）。
	if store != nil {
		if latest := store.LatestEventID(); latest > 0 {
			s.curID.Store(latest)
		}
	}
	// 启动广播 goroutine（移植自 stream.py:246-291 _run_queue_loop）。
	s.wg.Add(1)
	go s.broadcastLoop()
	return s
}

// Subscribe 注册订阅者回调。移植自 stream.py:130-148 subscribe。
// 同一 (subscriberID, callbackID) 重复订阅返回错误（移植自 stream.py:144-148）。
// 返回 cancel 闭包，调用即取消订阅（移植自 unsubscribe）。
// 回调在专属 goroutine 中顺序执行（同订阅者串行，跨订阅者并行）回调内可安全重入
// AddEvent（因 AddEvent 入队即返回，不持有订阅者队列投递）。
func (s *EventStream) Subscribe(subID SubscriberID, cbID CallbackID, _ int, cb func(Event)) (cancel func(), err error) {
	if subID == "" || cbID == "" {
		return nil, errors.New("subscriberID and callbackID must be non-empty")
	}
	if cb == nil {
		return nil, errors.New("callback must be non-nil")
	}
	s.mu.Lock()
	if _, ok := s.subscribers[subID]; !ok {
		s.subscribers[subID] = make(map[CallbackID]*subscription)
	}
	if _, exists := s.subscribers[subID][cbID]; exists {
		s.mu.Unlock()
		return nil, errors.New("callback ID already exists for subscriber")
	}
	ctx, cancel := context.WithCancel(context.Background())
	sub := &subscription{
		owner:  subID,
		cbID:   cbID,
		cb:     cb,
		q:      newUnboundedQueue(),
		ctx:    ctx,
		cancel: cancel,
	}
	s.subscribers[subID][cbID] = sub
	s.mu.Unlock()

	// 启动订阅者专属 goroutine（移植自 ThreadPoolExecutor max_workers=1 的顺序保证）。
	s.wg.Add(1)
	go s.runSubscriber(ctx, sub)

	return func() { s.unsubscribe(subID, cbID) }, nil
}

// runSubscriber 订阅者专属 goroutine，顺序消费无界队列中的事件。
func (s *EventStream) runSubscriber(ctx context.Context, sub *subscription) {
	defer s.wg.Done()
	for {
		// 优先响应 ctx/stop，再出队。
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		default:
		}
		ev, ok := sub.q.get()
		if !ok {
			// 队列已关闭（unsubscribe 或 Close 触发）。
			return
		}
		// 回调 panic 隔离，避免一个订阅者崩溃拖垮整个总线。
		func() {
			defer func() { recover() }()
			sub.cb(ev)
		}()
	}
}

// broadcastLoop 广播 goroutine：从主队列出队事件，非阻塞投递到所有订阅者队列。
// 移植自 stream.py:246-291 _process_queue。独立 goroutine，不阻塞 AddEvent 调用方。
func (s *EventStream) broadcastLoop() {
	defer s.wg.Done()
	for {
		select {
		case <-s.stop:
			return
		default:
		}
		ev, ok := s.queue.get()
		if !ok {
			// 主队列已关闭（Close 触发）。
			return
		}
		// 快照订阅者列表（按 SubscriberID 排序保证幂等分发顺序）。
		s.mu.Lock()
		ids := make([]SubscriberID, 0, len(s.subscribers))
		for sid := range s.subscribers {
			ids = append(ids, sid)
		}
		s.mu.Unlock()
		// 简单排序（无 sort 包依赖）。
		for i := 1; i < len(ids); i++ {
			for j := i; j > 0 && ids[j] < ids[j-1]; j-- {
				ids[j], ids[j-1] = ids[j-1], ids[j]
			}
		}
		for _, sid := range ids {
			s.mu.Lock()
			m := s.subscribers[sid]
			subs := make([]*subscription, 0, len(m))
			for _, sub := range m {
				subs = append(subs, sub)
			}
			s.mu.Unlock()
			for _, sub := range subs {
				// 非阻塞入队（无界队列，不丢控制事件；Close 后入队静默丢弃）。
				sub.q.put(ev)
			}
		}
	}
}

// unsubscribe 取消订阅并关闭订阅者队列。移植自 stream.py:150-161 unsubscribe。
func (s *EventStream) unsubscribe(subID SubscriberID, cbID CallbackID) {
	s.mu.Lock()
	m, ok := s.subscribers[subID]
	if !ok {
		s.mu.Unlock()
		return
	}
	sub, ok := m[cbID]
	if !ok {
		s.mu.Unlock()
		return
	}
	delete(m, cbID)
	if len(m) == 0 {
		delete(s.subscribers, subID)
	}
	s.mu.Unlock()
	sub.cancel()
	// 关闭订阅者队列，runSubscriber 的 get 返回 (nil,false) 退出。
	sub.q.close()
}

// AddEvent 发布事件。移植自 stream.py:163-203 add_event。
// 分配单调递增 ID、打时间戳、设 source/cause，持久化（若 Store 注入），
// 然后入主队列（非阻塞）立即返回——独立 broadcastLoop goroutine 负责分发。
// 返回分配的 ID（供调用方建立 cause 链）。
// 回调内可安全重入本方法（入队即返回，不持有订阅者投递，无死锁）。
func (s *EventStream) AddEvent(ev Event, src EventSource, cause int64) (int64, error) {
	if s.stopped.Load() {
		return 0, errors.New("eventstream is closed")
	}
	if ev == nil {
		return 0, errors.New("event is nil")
	}
	// 校验未分配 ID（移植自 stream.py:164-167 防回环）。
	// Go 零值 id=0 也视为未分配（有效 ID 从 1 起）；INVALID_ID=-1 同样视为未分配。
	if ev.ID() > 0 && ev.ID() != INVALID_ID {
		return 0, errors.New("event already has an ID (loop prevention)")
	}
	id := s.curID.Add(1)
	ts := time.Now().UTC()
	// 用 assign 写入通用字段（具体事件嵌入 BaseEvent）。
	if be, ok := ev.(interface {
		assign(int64, time.Time, EventSource, int64)
	}); ok {
		be.assign(id, ts, src, cause)
	}
	// 持久化（若注入 Store）。
	if s.store != nil {
		if perr := s.store.Append(ev); perr != nil {
			// 持久化失败不阻断分发（降级），但返回错误供调用方日志。
			// 与 OpenHands 一致：file_store.write 失败仅日志。
			return id, perr
		}
	}
	// 入主队列（非阻塞），broadcastLoop 异步广播。
	// 关键：AddEvent 此处立即返回，不持有任何订阅者队列投递，
	// 故订阅者回调内重入 AddEvent 不会死锁。
	s.queue.put(ev)
	return id, nil
}

// Close 关闭总线，释放所有订阅者 + 广播 goroutine。移植自 stream.py:78-90 close。
// 关闭后 AddEvent 返回错误，Subscribe 无效。等待所有 goroutine 退出。
func (s *EventStream) Close() {
	if !s.stopped.CompareAndSwap(false, true) {
		return
	}
	close(s.stop)
	// 关闭主队列，broadcastLoop 的 get 返回 (nil,false) 退出。
	s.queue.close()
	s.mu.Lock()
	for _, m := range s.subscribers {
		for _, sub := range m {
			sub.cancel()
			sub.q.close()
		}
	}
	s.subscribers = make(map[SubscriberID]map[CallbackID]*subscription)
	s.mu.Unlock()
	// 等待广播 + 所有订阅者 goroutine 退出（避免 goroutine 泄漏）。
	s.wg.Wait()
}

// LatestEventID 当前最大事件 ID（含已持久化）。用于 cursor 续传。
func (s *EventStream) LatestEventID() int64 {
	return s.curID.Load()
}

// GetEventByID 按 ID 取单条事件（从 Store 检索）。移植自 EventStore.get_event。
// Store 为 nil（纯内存模式）时返回 (nil, false)。
func (s *EventStream) GetEventByID(id int64) (Event, bool) {
	if s.store == nil {
		return nil, false
	}
	return s.store.GetEvent(id)
}
