package eventstream

import (
	"container/list"
	"sync"
)

// unboundedQueue 无界事件队列。移植自 Python queue.Queue（默认无界）。
// 用于 AddEvent → 广播 goroutine 的解耦：AddEvent 入队立即返回（非阻塞），
// 广播 goroutine 出队分发。这样订阅者回调内重入 AddEvent 不会死锁
// （OpenHands stream.py 的 self._queue.put + _queue_thread 模型）。
type unboundedQueue struct {
	mu     sync.Mutex
	l      *list.List
	cond   *sync.Cond
	closed bool
}

func newUnboundedQueue() *unboundedQueue {
	q := &unboundedQueue{l: list.New()}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// put 入队（非阻塞）。Close 后入队静默丢弃（返回 false）。
func (q *unboundedQueue) put(ev Event) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return false
	}
	q.l.PushBack(ev)
	q.cond.Signal()
	return true
}

// get 阻塞出队。队列空且未关闭时阻塞等待；关闭且空时返回 (nil, false)。
func (q *unboundedQueue) get() (Event, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for q.l.Len() == 0 && !q.closed {
		q.cond.Wait()
	}
	if q.l.Len() == 0 {
		return nil, false
	}
	e := q.l.Front()
	q.l.Remove(e)
	return e.Value.(Event), true
}

// close 关闭队列，get 不再阻塞（排空后返回 false）。
func (q *unboundedQueue) close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	q.cond.Broadcast()
}
