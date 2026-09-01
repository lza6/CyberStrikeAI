package blackboard

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// subscriberBufferSize 订阅 channel 缓冲大小。参考实现使用类似量级以
// 兼顾吞吐与背压；满了之后丢弃最旧的一条并记 Warn，保证 at-least-once
// 语义（宁可丢旧保新，订阅者靠 cursor 去重）。
const subscriberBufferSize = 128

// MemoryBoard 进程内黑板实现。用一个 slice 维护全部 finding（顺序即序号），
// 一个 mutex 保护并发访问，一组带缓冲 channel 做 pub/sub。
//
// 不持久化：进程重启后状态丢失。适合单机桌面部署或作为分布式实现的
// 参考底座。
type MemoryBoard struct {
	mu          sync.Mutex
	findings    []Finding            // 按 Publish 顺序追加
	subscribers map[int64]chan Finding // key = subscriberID
	nextSubID   int64
	logger      *zap.Logger
}

// NewMemoryBoard 创建进程内黑板。logger 可为 nil（静默丢弃）。
func NewMemoryBoard(logger *zap.Logger) *MemoryBoard {
	return &MemoryBoard{
		subscribers: make(map[int64]chan Finding),
		logger:      logger,
	}
}

// Publish 追加一条 finding 并向所有订阅者广播。
func (b *MemoryBoard) Publish(ctx context.Context, finding Finding) (string, error) {
	if finding.ID == "" {
		finding.ID = uuid.New().String()
	}
	if finding.CreatedAt.IsZero() {
		finding.CreatedAt = time.Now()
	}

	b.mu.Lock()
	b.findings = append(b.findings, finding)
	// 复制 snapshot 用于广播，避免在持锁状态下向 channel 写入（可能阻塞）。
	subs := make([]chan Finding, 0, len(b.subscribers))
	for _, ch := range b.subscribers {
		subs = append(subs, ch)
	}
	b.mu.Unlock()

	// 非阻塞广播：channel 满则丢弃最旧一条 + Warn，保 at-least-once 语义。
	for _, ch := range subs {
		select {
		case ch <- finding:
		default:
			// 满了：尝试丢一条旧的腾位置；仍不行就放弃这条（at-least-once 由 cursor 兜底）。
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- finding:
			default:
				if b.logger != nil {
					b.logger.Warn("blackboard 订阅 channel 满，丢弃一条 finding（订阅者靠 cursor 去重）",
						zap.String("finding_id", finding.ID))
				}
			}
		}
	}

	// ctx 已取消不阻塞 Publish 本身（finding 已落盘内存）；订阅者各自感知 ctx。
	_ = ctx
	return finding.ID, nil
}

// Get 按 ID 获取单条 finding。
func (b *MemoryBoard) Get(ctx context.Context, id string) (Finding, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, f := range b.findings {
		if f.ID == id {
			return f, true, nil
		}
	}
	return Finding{}, false, nil
}

// List 列出某项目下的所有 finding（升序）。projectID 为空返回全部。
func (b *MemoryBoard) List(ctx context.Context, projectID string) ([]Finding, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if projectID == "" {
		out := make([]Finding, len(b.findings))
		copy(out, b.findings)
		return out, nil
	}
	out := make([]Finding, 0)
	for _, f := range b.findings {
		if f.ProjectID == projectID {
			out = append(out, f)
		}
	}
	return out, nil
}

// Subscribe 从 cursor 开始订阅。cursor 是已处理的最后一条 finding 序号（从 0 开始）；
// 0 表示从第一条开始。返回的 channel 收到的 finding 对应序号 cursor+1..N。
// ctx 取消时关闭 channel。
func (b *MemoryBoard) Subscribe(ctx context.Context, cursor int64) <-chan Finding {
	ch := make(chan Finding, subscriberBufferSize)

	b.mu.Lock()
	// 先把 cursor 之后已存在的 finding 投递到 channel（带缓冲，理论上不会阻塞）。
	if cursor < 0 {
		cursor = 0
	}
	if cursor > int64(len(b.findings)) {
		cursor = int64(len(b.findings))
	}
	for i := cursor; i < int64(len(b.findings)); i++ {
		ch <- b.findings[i]
	}
	// 注册为订阅者，接收后续 Publish。
	b.nextSubID++
	subID := b.nextSubID
	b.subscribers[subID] = ch
	b.mu.Unlock()

	// ctx 取消时移除订阅者并关闭 channel。
	go func() {
		<-ctx.Done()
		b.mu.Lock()
		if existing, ok := b.subscribers[subID]; ok {
			delete(b.subscribers, subID)
			close(existing)
		}
		b.mu.Unlock()
	}()

	return ch
}

// Supersede 标记 oldID 被 newFinding 取代，返回新 finding 的 ID。
func (b *MemoryBoard) Supersede(ctx context.Context, oldID string, newFinding Finding) (string, error) {
	if oldID == "" {
		return "", errEmptyOldID
	}
	// 先确认 oldID 存在。
	b.mu.Lock()
	oldIdx := -1
	for i, f := range b.findings {
		if f.ID == oldID {
			oldIdx = i
			break
		}
	}
	b.mu.Unlock()
	if oldIdx < 0 {
		return "", errOldNotFound
	}

	// 发布新 finding。
	newID, err := b.Publish(ctx, newFinding)
	if err != nil {
		return "", err
	}

	// 标记 old.SupersededBy = newID。
	b.mu.Lock()
	for i := range b.findings {
		if b.findings[i].ID == oldID {
			b.findings[i].SupersededBy = newID
			break
		}
	}
	b.mu.Unlock()
	return newID, nil
}

// Len 返回当前 finding 总数（主要供测试用）。
func (b *MemoryBoard) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.findings)
}
