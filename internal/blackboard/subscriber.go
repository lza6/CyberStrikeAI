package blackboard

import (
	"context"
	"sync"

	"go.uber.org/zap"
)

// subscriber 是 MemoryBoard 与 SQLiteBoard 共用的订阅者并发安全封装。
//
// 解决 Blocking 1（send-on-closed-channel panic + race）：
//   - Publish/Supersede 在 b.mu.Unlock() 后遍历快照 subs 发送，但 Subscribe 的
//     ctx.Done goroutine 与 Close 在锁内 sub.close() → close(ch)。快照释放锁后
//     对已关闭 channel 发送会 panic，且 close+send 是 race。
//   - 本结构体用 mu 互斥锁序列化 close 与 send：close 在 mu 内置 closed=true
//     并 close(ch)；trySend 在 mu 内检查 closed，closed=true 则不发直接返回。
//     因 close 与 send 互斥，close(ch) 时无并发 sender，send-on-closed 不会
//     发生，race detector 也不报（访问已同步）。
//
// 生命周期：
//   - Subscribe 锁内 newSubscriber() 注册到 b.subscribers。
//   - ctx 取消 goroutine 调用 sub.close()（closeOnce 保证幂等）。
//   - Close 遍历 b.subscribers 调用 sub.close() + sub.cancel()。
//   - trySend 在 Publish/Supersede 广播路径调用，closed 快速返回。
//
// 返回给调用方的 channel 是 sub.ch（只读），sub.close() 关闭后接收方感知 ok=false。
type subscriber struct {
	ch        chan Finding
	mu        sync.Mutex
	closed    bool
	closeOnce sync.Once
	// cancel 持有 Subscribe 传入 ctx 的 cancel 句柄（P1-5）。board.Close 时调用，
	// 使 Subscribe 注册的 ctx.Done goroutine（阻塞在 <-ctx.Done()）得以退出，
	// 否则每个订阅者泄漏一个常驻 goroutine。Subscribe 的 ctx 无 cancel（如
	// context.Background()）时为 nil，Close 跳过。
	cancel context.CancelFunc
}

// newSubscriber 构造一个未关闭的订阅者，缓冲大小由调用方传入（与各 board 的
// buffer 常量对齐：MemoryBoard/SQLiteBoard 均为 128）。
func newSubscriber(bufferSize int) *subscriber {
	return &subscriber{
		ch: make(chan Finding, bufferSize),
	}
}

// bindCancel 绑定 ctx cancel 句柄（Subscribe 在锁内注册前调用）。
func (s *subscriber) bindCancel(cancel context.CancelFunc) {
	s.cancel = cancel
}

// cancelCtx 触发订阅 ctx 的取消（Close 时调用），唤醒等待 ctx.Done 的 goroutine。
// 幂等：context.CancelFunc 本身幂等；cancel 为 nil（ctx 无 cancel）时 no-op。
func (s *subscriber) cancelCtx() {
	if s.cancel != nil {
		s.cancel()
	}
}

// close 关闭订阅者。幂等（closeOnce）。在 mu 内置 closed=true 并 close(ch)，
// 保证与 trySend 的 send 互斥（close 时无并发 sender）。
// 调用方：Subscribe 的 ctx.Done goroutine、Close。两者可能并发调用，closeOnce
// 保证只关闭一次。
func (s *subscriber) close() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		close(s.ch)
		s.mu.Unlock()
	})
}

// trySend 向订阅者投递一条 finding。非阻塞：channel 满则丢旧一条保新；订阅者
// 已关闭则直接返回。close 与 send 在 mu 内互斥，close(ch) 时无并发 sender，
// 故 send-on-closed 不会发生。recover 作为终极兜底（逻辑正确时不触发）。
func (s *subscriber) trySend(f Finding, logger *zap.Logger) {
	// recover 终极兜底：逻辑上 close 与 send 互斥不会 panic，但保留兜底以防
	// 未来改动引入的 send-on-closed（不崩进程）。
	defer func() { _ = recover() }()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	select {
	case s.ch <- f:
	default:
		// channel 满：尝试丢一条旧的腾位置；仍不行放弃这条（at-least-once 由 cursor 兜底）。
		select {
		case <-s.ch:
		default:
		}
		select {
		case s.ch <- f:
		default:
			if logger != nil {
				logger.Warn("blackboard 订阅 channel 满，丢弃一条 finding（订阅者靠 cursor 去重）",
					zap.String("finding_id", f.ID))
			}
		}
	}
}
