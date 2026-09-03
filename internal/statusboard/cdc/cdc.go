// Package cdc 提供 CDC（变更数据捕获）的 poller + broadcaster live push 层。
//
// 迁移自参考项目 agent-orchestrator/backend 的 internal/cdc 包。
// 核心设计（与参考项目一致）：
//   - Poller 每 DefaultPollInterval(100ms) 轮询 Store 的 EventsAfter(lastSeq, batch)，
//     按 seq 升序投递给 Broadcaster，推进内存 cursor lastSeq（live push）。
//   - Broadcaster 维护 subs map[int]func(Event)，Publish 时同步调所有订阅者；
//     订阅者 panic 被 recover 隔离，一个坏 callback 不杀 poller goroutine。
//   - durable catch-up 是客户端责任（读 Store 从自己的 offset），poller 只推 live。
//   - SeekToHead 让 daemon 重启时跳过历史（只推新增）。
//
// 与参考项目的差异（Go 重写，适配 CyberStrikeAI）：
//   - 参考项目 Source 接口含 EventsAfter/LatestSeq；本实现直接复用 internal/eventstream.Store
//     （Store.SearchEvents(startID, Filter) 提供 durable catch-up，LatestEventID 提供 head seq）。
//   - 参考项目 Poller 读 change_log；本实现 Poller 读 eventstream.SQLiteStore
//     （EventStream.AddEvent→SQLiteStore.Append 已持久化，Poller 轮询 live）。
//   - EventStream 的 broadcastLoop 已是进程内 fan-out；但 CDC Poller 的场景是
//     "跨进程/daemon 边界的 live push"（如 SSE 端点订阅），故单独保留 Broadcaster
//     seam，供 transport 层（SSE/WS）按需订阅。
//
// 设计为 leaf 包：只依赖标准库 + internal/eventstream（Event/Store 类型），不反向导入。
package cdc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"cyberstrike-ai/internal/eventstream"
)

// DefaultPollInterval 是 poller 检查 change_log 新行的频率。
// 轮询（而非 fs-notify 或 DB hook）保持零依赖；此 cadence 下 live 更新远低于人感延迟。
// 移植自参考项目 cdc/poller.go:13。
const DefaultPollInterval = 100 * time.Millisecond

// DefaultBatch 限制一次 poll 排空多少事件。移植自 cdc/poller.go:16。
const DefaultBatch = 512

// Source 是 poller 对持久化日志的视图：读 after 之后的事件 + 当前 head seq。
// 移植自参考项目 cdc.Source（EventsAfter/LatestSeq）。
// 本实现直接复用 eventstream.Store（SearchEvents 提供 startID 流式读，
// LatestEventID 提供 head）。
type Source interface {
	// EventsAfter 返回 after（不含）之后的事件，按 ID 升序。
	// limit<=0 时实现用默认上限。ctx 取消时尽早返回。
	EventsAfter(ctx context.Context, after int64, limit int) (<-chan eventstream.Event, error)
	// LatestSeq 返回当前最大事件 ID（head）。空表返回 0。
	LatestSeq(ctx context.Context) (int64, error)
}

// StoreSource 把 eventstream.Store 适配为 CDC Source。
// eventstream.Store 的 SearchEvents(startID, Filter) 从 startID（含）升序流式读，
// EventsAfter 需要"after（不含）"，故 Source 包装层把 after+1 传给 SearchEvents。
type StoreSource struct {
	Store eventstream.Store
}

// EventsAfter 实现 Source：after+1 起，无类型过滤，流式读。
func (s *StoreSource) EventsAfter(ctx context.Context, after int64, limit int) (<-chan eventstream.Event, error) {
	if s == nil || s.Store == nil {
		ch := make(chan eventstream.Event)
		close(ch)
		return ch, nil
	}
	// SearchEvents 从 startID（含）升序读；after 不含 → startID = after+1。
	ch := s.Store.SearchEvents(after+1, eventstream.Filter{})
	// 包装一层：在 limit 达到时截断，并响应 ctx。
	out := make(chan eventstream.Event, 64)
	go func() {
		defer close(out)
		count := 0
		for ev := range ch {
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

// LatestSeq 实现 Source：返回 Store.LatestEventID()。
func (s *StoreSource) LatestSeq(ctx context.Context) (int64, error) {
	if s == nil || s.Store == nil {
		return 0, nil
	}
	return s.Store.LatestEventID(), nil
}

// Broadcaster 是 poller 喂的进程内 fan-out。移植自参考项目 cdc/broadcast.go。
//
// 订阅者如 SSE 端点 / terminal session-state 注册 callback；每条 polled Event
// 投递给所有当前订阅者。它是 CDC poller 与 live 投递之间的唯一 seam，
// 故 transport 可构建/替换而不动 poller。
type Broadcaster struct {
	mu     sync.RWMutex
	nextID int
	subs   map[int]func(eventstream.Event)
	logger *slog.Logger
}

// NewBroadcaster 返回空 Broadcaster。移植自 cdc/broadcast.go:21-23。
func NewBroadcaster() *Broadcaster {
	return &Broadcaster{subs: map[int]func(eventstream.Event){}, logger: slog.Default()}
}

// WithLogger 注入 logger（测试/生产可选）。返回 b 供链式调用。
func (b *Broadcaster) WithLogger(l *slog.Logger) *Broadcaster {
	if b != nil && l != nil {
		b.logger = l
	}
	return b
}

// Subscribe 注册 fn 并返回 unsubscribe 函数。移植自 cdc/broadcast.go:28-39。
// fn 从 poller loop 同步调用，故不得阻塞；需缓冲的 transport 应在自己的 fn 内入队。
func (b *Broadcaster) Subscribe(fn func(eventstream.Event)) (unsubscribe func()) {
	b.mu.Lock()
	id := b.nextID
	b.nextID++
	b.subs[id] = fn
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		delete(b.subs, id)
		b.mu.Unlock()
	}
}

// SubscriberCount 报告当前订阅者数。移植自 cdc/broadcast.go:42-46。
func (b *Broadcaster) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}

// Publish 投递 e 给所有当前订阅者。panic 的订阅者被 recover 并记日志，
// 故一个坏 callback 不杀 poller goroutine 或饿死其他订阅者。移植自 cdc/broadcast.go:51-57。
func (b *Broadcaster) Publish(e eventstream.Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, fn := range b.subs {
		b.deliver(fn, e)
	}
}

func (b *Broadcaster) deliver(fn func(eventstream.Event), e eventstream.Event) {
	defer func() {
		if r := recover(); r != nil {
			if b.logger != nil {
				b.logger.Error("cdc broadcaster: subscriber panicked", "seq", e.ID(), "panic", r)
			}
		}
	}()
	fn(e)
}

// Poller tails change_log 并按 seq 序把每条新事件经 Broadcaster fan-out。
// 只持有内存 cursor lastSeq：它是 LIVE push 路径，durable catch-up 是客户端责任。
// 重启时 re-seek 到 head，故 poller 永不向全新启动的 broadcaster 重播历史。
// 移植自参考项目 cdc/poller.go:30-37。
type Poller struct {
	src      Source
	bcast    *Broadcaster
	interval time.Duration
	batch    int
	logger   *slog.Logger
	lastSeq  int64
}

// PollerConfig 持有可选旋钮；零值回退默认。移植自 cdc/poller.go:42-47。
// StartSeq 是起始 cursor；生产 wiring 留 0 并调 SeekToHead，测试设为从开头读。
type PollerConfig struct {
	Interval time.Duration
	Batch    int
	Logger   *slog.Logger
	StartSeq int64
}

// NewPoller 构造 over src 的 Poller，经 bcast fan-out。移植自 cdc/poller.go:50-69。
func NewPoller(src Source, bcast *Broadcaster, cfg PollerConfig) *Poller {
	p := &Poller{
		src:      src,
		bcast:    bcast,
		interval: cfg.Interval,
		batch:    cfg.Batch,
		logger:   cfg.Logger,
		lastSeq:  cfg.StartSeq,
	}
	if p.interval <= 0 {
		p.interval = DefaultPollInterval
	}
	if p.batch <= 0 {
		p.batch = DefaultBatch
	}
	if p.logger == nil {
		p.logger = slog.Default()
	}
	return p
}

// SeekToHead 把 cursor 移到当前 head，故 poller 只广播从现在起创建的事件
// （客户端靠读 Store 补更老的事件）。移植自 cdc/poller.go:73-80。
func (p *Poller) SeekToHead(ctx context.Context) error {
	seq, err := p.src.LatestSeq(ctx)
	if err != nil {
		return fmt.Errorf("cdc poller seek: %w", err)
	}
	p.lastSeq = seq
	return nil
}

// Start 运行 poll loop 直到 ctx 取消；返回的 channel 在 loop 退出时关闭。
// 移植自 cdc/poller.go:84-102。
func (p *Poller) Start(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(p.interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := p.Poll(ctx); err != nil {
					if p.logger != nil {
						p.logger.Error("cdc poller: poll failed", "err", err)
					}
				}
			}
		}
	}()
	return done
}

// Poll 排空一批新事件并按 seq 序广播，推进 cursor。导出以便测试（和 daemon）
// 同步驱动一轮。移植自 cdc/poller.go:107-120。
func (p *Poller) Poll(ctx context.Context) error {
	if p.src == nil {
		return errors.New("cdc poller: source is nil")
	}
	ch, err := p.src.EventsAfter(ctx, p.lastSeq, p.batch)
	if err != nil {
		return fmt.Errorf("cdc poller: read after %d: %w", p.lastSeq, err)
	}
	for ev := range ch {
		if ev == nil {
			continue
		}
		if ev.ID() <= p.lastSeq {
			continue // 幂等守卫
		}
		if p.bcast != nil {
			p.bcast.Publish(ev)
		}
		p.lastSeq = ev.ID()
	}
	return nil
}

// LastSeq 返回 poller 当前 cursor（已广播的最大 seq）。移植自 cdc/poller.go:123。
func (p *Poller) LastSeq() int64 { return p.lastSeq }
