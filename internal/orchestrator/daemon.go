package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"cyberstrike-ai/internal/statusboard"
)

// Action 是 daemon 从状态事实派生出的编排动作。移植自参考项目
// lifecycle.ApplySCMObservation 的 action 派生（CI 失败 → nudge 等），
// CyberStrikeAI 场景适配为：worker 超时/无信号/状态转移 → Action 交给 Handler。
type Action struct {
	// Kind 动作类型。
	Kind ActionKind
	// SessionID 目标 worker session。
	SessionID string
	// ProjectID 所属项目。
	ProjectID string
	// Reason 派生原因（人可读，供日志/通知）。
	Reason string
	// At 派生时间。
	At time.Time
	// Payload 附加数据（新状态等）。
	Payload map[string]interface{}
}

// ActionKind 动作类型枚举。
type ActionKind string

const (
	// ActionNudge 需要用户/agent 介入（needs_input / ci_failed 等）。
	ActionNudge ActionKind = "nudge"
	// ActionTimeout worker 无信号超时（no_signal）。
	ActionTimeout ActionKind = "timeout"
	// ActionStatusChanged 状态转移（idle→working 等，供看板刷新）。
	ActionStatusChanged ActionKind = "status_changed"
	// ActionTerminated worker 终止。
	ActionTerminated ActionKind = "terminated"
)

// ActionHandler 消费 daemon 派生的 Action。移植自参考项目 sessionguard.Guard.Nudge
// 的消费端（sendOnce 去重 + 持久化签名）——本实现把去重留给 Handler（更灵活）。
type ActionHandler func(ctx context.Context, action Action)

// Daemon 是编排守护：周期性从 StatusProvider 拉取 worker 状态事实，
// 用 statusboard.DeriveStatus 派生状态，与上次快照比对后发出 Action。
//
// 迁移自参考项目 daemon.go 的「观察者 → state → action」模式（SCM observer
// 30s tick + lifecycle.ApplySCMObservation），简化为：
//
//	goroutine ticker(interval) → Poll() → for each worker: DeriveStatus → diff → emit Action
//
// 设计约束：
//   - 零 app.go/config.go 依赖（用户决策：纯新增包暂不接 app）。
//   - Start(ctx) 自起 goroutine，Stop/ctx 取消退出（与 c2.SessionWatchdog 范式一致）。
//   - 状态源抽象为 StatusProvider 接口，测试可注入 fake。
//   - 上次快照仅存内存（lastStatus map）——daemon 重启后首个 tick 全量重发一次
//     （ActionStatusChanged），供消费端重建看板（at-least-once 语义）。
type Daemon struct {
	provider StatusProvider
	handler  ActionHandler
	interval time.Duration
	grace    time.Duration
	logger   *slog.Logger

	mu      sync.Mutex
	running bool
	stop    chan struct{}
	wg      sync.WaitGroup

	// lastStatus 上次派生状态快照（sessionID → SessionStatus）。
	lastStatus map[string]SessionStatusEntry
}

// SessionStatusEntry 快照条目。
type SessionStatusEntry struct {
	Status    string
	At        time.Time
	Detail    string // 人可读详情（如 "ci_failed: pr1"）
	ProjectID string
}

// StatusProvider 是 daemon 的状态事实源。移植自参考项目 observe/scm/observer.go
// 的 ListAllSessions + FetchPullRequests，抽象为一次拉取全部 worker 事实。
type StatusProvider interface {
	// ListWorkerFacts 返回当前所有 worker 的状态事实 + PR 事实。
	// 按 sessionID 索引。空 map 合法（无 worker）。
	ListWorkerFacts(ctx context.Context) (map[string]WorkerFacts, error)
}

// WorkerFacts 一个 worker 的完整事实（session + PR）。
type WorkerFacts struct {
	ProjectID string
	Session   statusboard.SessionFacts
	PRs       []statusboard.PRFacts
}

// OrchestratorConfig daemon 配置；零值走默认。
type OrchestratorConfig struct {
	// Interval 轮询间隔（默认 5s；参考项目 SCM observer 30s，本实现 worker 状态
	// 无外部 API 调用，可更短）。
	Interval time.Duration
	// NoSignalGrace no_signal 判定静默期（默认 90s，对齐参考项目测试值）。
	NoSignalGrace time.Duration
	// Logger 可选。
	Logger *slog.Logger
}

// NewDaemon 构造。
func NewDaemon(provider StatusProvider, handler ActionHandler, cfg OrchestratorConfig) *Daemon {
	d := &Daemon{
		provider:   provider,
		handler:    handler,
		interval:   cfg.Interval,
		grace:      cfg.NoSignalGrace,
		logger:     cfg.Logger,
		lastStatus: make(map[string]SessionStatusEntry),
		stop:       make(chan struct{}),
	}
	if d.interval <= 0 {
		d.interval = 5 * time.Second
	}
	if d.grace <= 0 {
		d.grace = 90 * time.Second
	}
	if d.logger == nil {
		d.logger = slog.Default()
	}
	return d
}

// Start 启动守护 goroutine（幂等：重复 Start 无副作用）。ctx 取消或 Stop() 退出。
func (d *Daemon) Start(ctx context.Context) {
	if d == nil || d.provider == nil {
		return
	}
	d.mu.Lock()
	if d.running {
		d.mu.Unlock()
		return
	}
	d.running = true
	d.stop = make(chan struct{})
	stop := d.stop
	d.mu.Unlock()

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		ticker := time.NewTicker(d.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-ticker.C:
				pollCtx, cancel := context.WithTimeout(context.Background(), d.interval)
				if err := d.Poll(pollCtx); err != nil {
					d.logger.Error("orchestrator daemon: poll failed", "err", err)
				}
				cancel()
			}
		}
	}()
}

// Stop 停止守护 goroutine 并等待退出（幂等）。
func (d *Daemon) Stop() {
	if d == nil {
		return
	}
	d.mu.Lock()
	if !d.running {
		d.mu.Unlock()
		return
	}
	d.running = false
	close(d.stop)
	d.mu.Unlock()
	d.wg.Wait()
}

// Poll 执行一轮：拉事实 → 派生 → diff → emit Action。导出供测试同步驱动。
func (d *Daemon) Poll(ctx context.Context) error {
	if d == nil {
		return errors.New("orchestrator daemon: nil receiver")
	}
	if d.provider == nil {
		return errors.New("orchestrator daemon: provider is nil")
	}
	facts, err := d.provider.ListWorkerFacts(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator daemon: list worker facts: %w", err)
	}
	now := time.Now().UTC()
	d.mu.Lock()
	prev := d.lastStatus
	cur := make(map[string]SessionStatusEntry, len(facts))
	d.mu.Unlock()

	for sid, wf := range facts {
		status := statusboard.DeriveStatus(wf.Session, wf.PRs, now, d.grace)
		entry := SessionStatusEntry{
			Status:    string(status),
			At:        now,
			Detail:    deriveDetail(status, wf.PRs),
			ProjectID: wf.ProjectID,
		}
		cur[sid] = entry

		if old, existed := prev[sid]; !existed {
			// 新 worker 首次观察 → status_changed（消费端据此入看板）。
			d.emit(ctx, Action{Kind: ActionStatusChanged, SessionID: sid, ProjectID: wf.ProjectID,
				Reason: "worker first observed: " + entry.Status, At: now,
				Payload: map[string]interface{}{"status": entry.Status}})
		} else if old.Status != entry.Status {
			// 状态转移。
			kind := ActionStatusChanged
			reason := fmt.Sprintf("status %s -> %s", old.Status, entry.Status)
			switch status {
			case statusboard.StatusNeedsInput, statusboard.StatusCIFailed, statusboard.StatusChangesRequested:
				kind = ActionNudge
			case statusboard.StatusNoSignal:
				kind = ActionTimeout
			case statusboard.StatusTerminated, statusboard.StatusMerged:
				kind = ActionTerminated
			}
			d.emit(ctx, Action{Kind: kind, SessionID: sid, ProjectID: wf.ProjectID,
				Reason: reason, At: now,
				Payload: map[string]interface{}{"from": old.Status, "to": entry.Status, "detail": entry.Detail}})
		}
	}

	// 消失的 worker（provider 不再返回）→ terminated。
	for sid, old := range prev {
		if _, still := cur[sid]; !still {
			d.emit(ctx, Action{Kind: ActionTerminated, SessionID: sid, ProjectID: old.ProjectID,
				Reason: "worker disappeared from provider", At: now,
				Payload: map[string]interface{}{"from": old.Status}})
		}
	}

	d.mu.Lock()
	d.lastStatus = cur
	d.mu.Unlock()
	return nil
}

// emit 安全调用 handler（nil-safe + panic 隔离）。
func (d *Daemon) emit(ctx context.Context, a Action) {
	if d.handler == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			d.logger.Error("orchestrator daemon: handler panicked", "session", a.SessionID, "panic", r)
		}
	}()
	d.handler(ctx, a)
}

// deriveDetail 生成人可读详情（worst PR 等）。
func deriveDetail(status statusboard.SessionStatus, prs []statusboard.PRFacts) string {
	if len(prs) == 0 {
		return string(status)
	}
	worst := ""
	for _, pr := range prs {
		if pr.Closed || pr.Merged {
			continue
		}
		if worst == "" {
			worst = pr.URL
		}
	}
	if worst != "" {
		return fmt.Sprintf("%s (%s)", status, worst)
	}
	return string(status)
}

// Snapshot 返回当前快照副本（调试/看板查询用）。
func (d *Daemon) Snapshot() map[string]SessionStatusEntry {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[string]SessionStatusEntry, len(d.lastStatus))
	for k, v := range d.lastStatus {
		out[k] = v
	}
	return out
}
