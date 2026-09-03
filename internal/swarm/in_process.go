// Package swarm — 进程内后端：spawn 一个 goroutine 跑 teammate 逻辑，用 channel + 文件 mailbox 通信。
//
// 移植自 OpenHarness swarm/in_process.py InProcessBackend（693 行），简化：
//   - 去掉 ContextVar / TeammateContext 自动传播（Go 用 context.Context 显式传递）
//   - 去掉 system_prompt / plan_mode 解析（由编排层负责）
//   - query loop stub：用户传入 RunFunc，由编排层注入真实 agent loop
//   - mailbox 同时落文件（持久化，跨进程可见）+ 推内存 channel（低延迟旁路）
package swarm

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// RunFunc 是 teammate 的 agent loop 入口。编排层注入真实实现。
//
// ctx 在优雅/强制 shutdown 时取消。messages 是 user_message 投递 channel。
// 返回 idle summary（写入 idle_notification）。
type RunFunc func(ctx context.Context, cfg SpawnConfig, messages <-chan Message) (summary string, err error)

// InProcessBackend 在进程内用 goroutine 执行 teammate。
//
// 移植自 OpenHarness swarm/in_process.py:413 InProcessBackend。
type InProcessBackend struct {
	homeDir  string
	run      RunFunc
	mu       sync.Mutex
	active   map[string]*inProcessEntry
	shutdownTimeout time.Duration // 默认 10s
}

type inProcessEntry struct {
	agentID   string
	taskID    string
	cancel    context.CancelFunc // 优雅取消
	forceCancel context.CancelFunc // 强制取消
	done      chan struct{}        // goroutine 完成信号
	mailbox   *Mailbox
	msgCh     chan Message
	startedAt time.Time
}

// NewInProcessBackend 创建进程内后端。homeDir/run 为空时返回错误。
func NewInProcessBackend(homeDir string, run RunFunc) (*InProcessBackend, error) {
	if homeDir == "" {
		return nil, errors.New("swarm: in_process homeDir is empty")
	}
	if run == nil {
		return nil, errors.New("swarm: in_process run func is nil")
	}
	return &InProcessBackend{
		homeDir: homeDir, run: run, active: make(map[string]*inProcessEntry),
		shutdownTimeout: 10 * time.Second,
	}, nil
}

// Type 返回后端类型。移植自 in_process.py:422。
func (b *InProcessBackend) Type() BackendType { return BackendInProcess }

// IsAvailable 进程内后端始终可用。移植自 in_process.py:432。
func (b *InProcessBackend) IsAvailable(_ context.Context) bool { return true }

// Spawn 启动一个 goroutine 跑 teammate。移植自 in_process.py:436。
func (b *InProcessBackend) Spawn(ctx context.Context, cfg SpawnConfig) (SpawnResult, error) {
	agentID := fmt.Sprintf("%s@%s", cfg.Name, cfg.Team)
	b.mu.Lock()
	if _, exists := b.active[agentID]; exists {
		b.mu.Unlock()
		return SpawnResult{
			TaskID: "", AgentID: agentID, BackendType: BackendInProcess,
			Success: false, Error: "agent already spawned",
		}, nil
	}
	taskID := "in_process_" + uuid.NewString()[:12]
	mb, err := NewMailbox(b.homeDir, cfg.Team, agentID)
	if err != nil {
		b.mu.Unlock()
		return SpawnResult{}, fmt.Errorf("swarm: in_process spawn mailbox: %w", err)
	}
	// 双 context：优雅取消 + 强制取消
	runCtx, cancel := context.WithCancel(ctx)
	forceCtx, forceCancel := context.WithCancel(context.Background())
	msgCh := make(chan Message, 16)
	entry := &inProcessEntry{
		agentID: agentID, taskID: taskID,
		cancel: cancel, forceCancel: forceCancel,
		done: make(chan struct{}), mailbox: mb, msgCh: msgCh,
		startedAt: time.Now(),
	}
	b.active[agentID] = entry
	b.mu.Unlock()

	// spawn goroutine（自动 copy context）
	go func() {
		defer close(entry.done)
		// 编排层注入的 RunFunc；它负责从 msgCh 取消息、跑 query loop、返回 summary
		summary, _ := b.run(runCtx, cfg, msgCh)
		// 完成后写 idle_notification 到 leader mailbox（recipient 用 team leader 名）
		_ = mb.Write(forceCtx, NewIdleNotification(agentID, "team-lead", summary))
	}()

	return SpawnResult{
		TaskID: taskID, AgentID: agentID, BackendType: BackendInProcess,
		Success: true,
	}, nil
}

// SendMessage 投递消息：推内存 channel + 落文件 mailbox（持久化）。
//
// 移植自 OpenHarness in_process.py:494。OpenHarness 原本想加内存旁路但没做完
// （in_process.py:502-505 注释），这里补齐：channel 非阻塞投递，满则只落文件。
//
// M2 修复（重复投递）：channel 旁路命中时消息即视为已投递，直接把文件消息标记
// read；channel 满时保持 unread，由 DrainMailbox 兜底注入。二选一，不重复。
func (b *InProcessBackend) SendMessage(ctx context.Context, agentID string, msg Message) error {
	b.mu.Lock()
	entry, ok := b.active[agentID]
	b.mu.Unlock()
	if !ok {
		return ErrAgentNotFound
	}
	// 落文件（持久化，跨进程可见）
	mbMsg := MailboxMessage{
		ID: uuid.NewString(), Type: MsgUserMessage,
		Sender: msg.FromAgent, Recipient: agentID,
		Payload: map[string]interface{}{"content": msg.Text, "color": msg.Color},
		Timestamp: float64(time.Now().UnixNano()) / 1e9,
	}
	if err := entry.mailbox.Write(ctx, mbMsg); err != nil {
		return fmt.Errorf("swarm: in_process send mailbox: %w", err)
	}
	// 内存旁路（低延迟）。命中即标记 read（已投递），避免 DrainMailbox 重复注入；
	// 满则跳过，保持 unread，teammate 会从 mailbox drain。
	select {
	case entry.msgCh <- msg:
		_ = entry.mailbox.MarkRead(ctx, mbMsg.ID)
	default:
	}
	return nil
}

// Shutdown 终止 teammate。移植自 in_process.py:529。
//
// 优雅：cancel() + 等 shutdownTimeout；超时则 forceCancel()。
// force=true：直接 forceCancel()。
func (b *InProcessBackend) Shutdown(ctx context.Context, agentID string, force bool) (bool, error) {
	b.mu.Lock()
	entry, ok := b.active[agentID]
	b.mu.Unlock()
	if !ok {
		return false, ErrAgentNotFound
	}
	if force {
		entry.forceCancel()
		entry.cancel()
	} else {
		entry.cancel()
		select {
		case <-entry.done:
		case <-time.After(b.shutdownTimeout):
			entry.forceCancel()
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	<-entry.done
	b.mu.Lock()
	delete(b.active, agentID)
	b.mu.Unlock()
	return true, nil
}

// DrainMailbox 供 teammate goroutine 在 query loop 间隙调用，把文件 mailbox 的
// user_message 注入 msgCh。移植自 OpenHarness in_process.py:295 _drain_mailbox。
func (b *InProcessBackend) DrainMailbox(ctx context.Context, agentID string) error {
	b.mu.Lock()
	entry, ok := b.active[agentID]
	b.mu.Unlock()
	if !ok {
		return ErrAgentNotFound
	}
	msgs, err := entry.mailbox.ReadAll(ctx, true)
	if err != nil {
		return err
	}
	for _, m := range msgs {
		if m.Type == MsgShutdown {
			entry.forceCancel()
			return nil
		}
		if m.Type == MsgUserMessage {
			content, _ := m.Payload["content"].(string)
			// L6 修复：保留小数秒精度（Timestamp 是浮点秒）
			ts := time.Unix(int64(m.Timestamp), int64((m.Timestamp-float64(int64(m.Timestamp)))*1e9))
			select {
			case entry.msgCh <- Message{Text: content, FromAgent: m.Sender, Timestamp: ts}:
				_ = entry.mailbox.MarkRead(ctx, m.ID)
			default:
			}
		}
	}
	return nil
}
