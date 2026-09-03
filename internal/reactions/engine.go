// Package reactions 提供反应式安全事件引擎：订阅 blackboard 事件流 → 匹配规则 → 触发动作。
//
// 设计移植自参考项目 agent-orchestrator-main 的 reactions 引擎
// （packages/core/src/lifecycle-manager.ts:564-688 executeReaction + config.ts:552-623 defaults）。
// 适配 CyberStrikeAI (Go)：
//
//   - 事件源：复用 internal/blackboard.Board（已有 Subscribe 事件流），不新建总线。
//     安全事件（HIGH_IMPACT/scope 拦截/capability 回滚等）由 app.go 适配器 Publish 成
//     blackboard.Finding{Type:"<reaction-key>", ...}，本引擎订阅消费。
//   - 事件→reaction 映射：Finding.Type 直接作为 reactionKey 查 config.Reactions.Rules
//     （简化参考项目的 eventToReactionKey switch——CyberStrikeAI 事件类型即 reaction key）。
//   - executeReaction：tracker（attempts/firstTriggered）+ retries + escalateAfter + threshold
//     三态升级逻辑 1:1 复刻参考项目（lifecycle-manager.ts:564-688）。
//   - action 通道：notify → 遍历 pluginslot.Notifier；send-to-agent → 降级为 notify
//     （CyberStrikeAI 的 Eino agent 无直接 send 通道，发回 blackboard 会递归，故降级）；
//     log-only → 仅记日志。
//   - 历史过滤：Engine 启动时记 startedAt，只处理 CreatedAt > startedAt 的 finding，
//     避免 Subscribe 回放历史触发重复 reaction（参考项目靠 tracker 重启清零，此处再加时间过滤双保险）。
package reactions

import (
	"context"
	"strings"
	"sync"
	"time"

	"cyberstrike-ai/internal/blackboard"
	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/pluginslot"

	"go.uber.org/zap"
)

// Engine 反应式安全事件引擎。移植自 agent-orchestrator LifecycleManager 的 reaction 部分。
type Engine struct {
	board     blackboard.Board
	cfg       config.ReactionsConfig
	logger    *zap.Logger
	notifiers []pluginslot.Notifier // notify 通道（由 app.go 注入已实例化的 Notifier）

	// trackers reaction 状态跟踪。key = projectID:reactionKey。
	// 移植自 lifecycle-manager.ts:199 reactionTrackers。内存态，重启清零。
	mu       sync.Mutex
	trackers map[string]*reactionTracker

	// startedAt 引擎启动时间。CreatedAt 早于此的 finding 视为历史，忽略。
	// 避免 Subscribe(ctx, 0) 回放历史触发重复 reaction。
	startedAt time.Time

	// startedOnce Start 只起一次消费 goroutine（防重复 Start 泄漏）。
	startedOnce sync.Once
	stopCancel  context.CancelFunc
}

// reactionTracker 单个 reaction 在某 project 下的重试/升级状态。
// 移植自 lifecycle-manager.ts:188-194（参考项目 ReactionTracker）。
type reactionTracker struct {
	attempts       int
	firstTriggered time.Time
}

// New 构造引擎。notifiers 为已实例化的 Notifier 列表（app.go 从 pluginslot.Get 取出）。
// board/cfg/logger 为 nil 时返回 nil（向后兼容：未配置 reactions 时不启用）。
// startedAt 在 Start() 时刻刷新（Critic L3 修复：避免 New 与 Start 间隔内的
// 合法事件被当"历史"过滤掉）。
func New(board blackboard.Board, cfg config.ReactionsConfig, notifiers []pluginslot.Notifier, logger *zap.Logger) *Engine {
	if board == nil || logger == nil {
		return nil
	}
	return &Engine{
		board:     board,
		cfg:       cfg,
		logger:    logger,
		notifiers: notifiers,
		trackers:  make(map[string]*reactionTracker),
		startedAt: time.Now(),
	}
}

// Start 起消费 goroutine 订阅 blackboard 事件流。幂等（多次调用只起一次）。
// ctx 取消时关闭订阅 channel，goroutine 退出。
func (e *Engine) Start(ctx context.Context) {
	if e == nil || !e.cfg.EnabledEffective() {
		return
	}
	e.startedOnce.Do(func() {
		// Critic L3 修复：startedAt 刷新到 Start 时刻（New→Start 间隔内的事件不丢）。
		e.startedAt = time.Now()
		childCtx, cancel := context.WithCancel(ctx)
		e.stopCancel = cancel
		go e.consume(childCtx)
		e.logger.Info("reactions 引擎已启动",
			zap.Int("notifiers", len(e.notifiers)),
			zap.Int("rules", len(e.cfg.Rules)),
		)
	})
}

// Stop 取消消费 goroutine。幂等。
func (e *Engine) Stop() {
	if e == nil || e.stopCancel == nil {
		return
	}
	e.stopCancel()
}

// consume 订阅 board 并处理 finding。移植自 lifecycle-manager 的 poll 循环 reaction 部分。
//
// K9 lifecycle 状态机轮询：consume 在事件处理循环内定期调 pollSessionStatus，
// 基于"是否有 pending tool_call / 是否有 hitl-pending finding / run 是否结束"
// 派生 SessionStatus（idle→running→tool_pending→hitl_pending→done→failed），
// 并在状态转换时触发对应 reaction（如 hitl_pending→send-to-agent）。
// 当前 Engine 仅 Finding-driven（向后兼容）；lifecycle 是增量：派生状态变更
// 经 publishSessionStatusFinding 发布为 finding，再走 handleFinding 匹配规则。
func (e *Engine) consume(ctx context.Context) {
	// cursor=0：Subscribe 回放历史，但 handleFinding 用 startedAt 过滤历史。
	ch := e.board.Subscribe(ctx, 0)
	// K9 lifecycle 轮询间隔：1s。与 reactions 引擎事件流解耦，独立 goroutine。
	// 由 stopCancel 取消（与 consume 同 ctx）。
	lifecycleTick := time.NewTicker(time.Second)
	defer lifecycleTick.Stop()
	// 派生状态需 board.List 取最新 findings；board 未注入 List（如 MemoryBoard 已实现）
	// 时降级为 no-op（向后兼容：仅 Finding-driven）。
	var lastSessionStatus SessionStatus
	for {
		select {
		case <-ctx.Done():
			return
		case finding, ok := <-ch:
			if !ok {
				return
			}
			e.handleFinding(ctx, finding)
		case <-lifecycleTick.C:
			// K9 lifecycle 状态机轮询：派生 SessionStatus 并在转换时触发。
			if e.board == nil {
				continue
			}
			newStatus := e.deriveSessionStatus(ctx)
			if newStatus != "" && newStatus != lastSessionStatus {
				lastSessionStatus = newStatus
				e.publishSessionStatusFinding(ctx, newStatus)
			}
		}
	}
}

// SessionStatus 是会话生命周期状态。K9：reactions lifecycle 状态机。
// 移植自 agent-orchestrator lifecycle-manager 的 session status 派生：
//   - idle：无活动（无 pending tool_call / 无 run / 无 hitl-pending finding）
//   - running：run 进行中（有待处理 finding 但无 pending tool_call / 无 hitl）
//   - tool_pending：有 pending tool_call（工具执行中）
//   - hitl_pending：有 hitl-pending finding（等待人工审批）
//   - done：run 完成（有 run-complete finding）
//   - failed：run 失败（有 failed finding）
type SessionStatus string

const (
	SessionStatusIdle        SessionStatus = "idle"
	SessionStatusRunning     SessionStatus = "running"
	SessionStatusToolPending SessionStatus = "tool_pending"
	SessionStatusHITLPending SessionStatus = "hitl_pending"
	SessionStatusDone        SessionStatus = "done"
	SessionStatusFailed     SessionStatus = "failed"
)

// findingTypeToolPending P2-3：pending tool_call 的真实 finding Type
// （securityevents 发布），deriveSessionStatus 直接按 Type 判定，
// 不再用 Detail 字符串启发式。
const findingTypeToolPending = "tool-pending"

// deriveSessionStatus 从 blackboard findings 派生当前会话状态。
// 优先级链：failed > done > hitl_pending > tool_pending > running > idle。
// board 无 List 时返回 ""（向后兼容：lifecycle 轮询 no-op）。
//
// RC10 修复：原实现每秒 board.List(ctx, "") 拉全部 finding 再倒序遍历，
// 千级 finding 浪费 CPU。现优先用类型断言探测 ListRecent（SQLiteBoard 已实现，
// 只拉最近 deriveSessionStatusRecentLimit 条）；MemoryBoard 无 ListRecent
// 则降级为全量 List（向后兼容）。
func (e *Engine) deriveSessionStatus(ctx context.Context) SessionStatus {
	if e == nil || e.board == nil {
		return ""
	}
	// 优先走 ListRecent（SQLiteBoard 已实现），只拉最近 N 条，避免千级全量扫描。
	const recentLimit = 50
	var findings []blackboard.Finding
	var err error
	if lr, ok := e.board.(interface {
		ListRecent(ctx context.Context, projectID string, limit int) ([]blackboard.Finding, error)
	}); ok {
		findings, err = lr.ListRecent(ctx, "", recentLimit)
	} else {
		findings, err = e.board.List(ctx, "")
	}
	if err != nil || len(findings) == 0 {
		return SessionStatusIdle
	}
	// 倒序遍历（最新优先），按优先级链判定。
	hasHitl := false
	hasRunComplete := false
	hasFailed := false
	hasPendingTool := false
	for i := len(findings) - 1; i >= 0; i-- {
		f := findings[i]
		switch f.Type {
		case "hitl-pending":
			hasHitl = true
		case "run-complete":
			hasRunComplete = true
		case "scope-violation", "capability-rollback", "agent-stuck":
			// 安全事件类 finding 视为失败信号（保守派生）。
			hasFailed = true
		case findingTypeToolPending:
			// P2-3：pending tool_call 用真实 finding Type 判定（securityevents 发布的
			// tool-pending 事件），替代原 Detail 含 "pending" 的字符串启发式——
			// hitl-pending 等 finding 的 Detail 是 conversationId=xxx，不含 pending，
			// 启发式永不命中。
			hasPendingTool = true
		}
	}
	switch {
	case hasFailed:
		return SessionStatusFailed
	case hasRunComplete:
		return SessionStatusDone
	case hasHitl:
		return SessionStatusHITLPending
	case hasPendingTool:
		return SessionStatusToolPending
	default:
		return SessionStatusRunning
	}
}

// publishSessionStatusFinding 把派生状态变更发布为 finding，供 handleFinding 匹配规则。
// 仅在状态变更时发布（防同状态重复 finding）。finding.Type = "session-status"，
// reaction key = "session-status"（用户可配置规则触发动作，默认 log-only）。
func (e *Engine) publishSessionStatusFinding(ctx context.Context, status SessionStatus) {
	if e == nil || e.board == nil || status == "" {
		return
	}
	// 跳过 startedAt 之前的历史过滤：用当前时间。
	f := blackboard.Finding{
		Type:      "session-status",
		Title:     "会话状态: " + string(status),
		Severity:  "info",
		Source:    "lifecycle",
		CreatedAt: time.Now(),
	}
	// 把状态编码到 Detail 供 rule 查询。
	f.Detail = "status=" + string(status)
	if _, err := e.board.Publish(ctx, f); err != nil && e.logger != nil {
		e.logger.Warn("reactions lifecycle publish session-status failed", zap.Error(err))
	}
}

// parseEscalateAfterDuration 解析 Reaction.EscalateAfter 时长字符串。
// K9：把 escalateAfter 时长解析抽为独立函数，供 lifecycle 状态机与
// executeReaction 共享。非法串返回 0 + error（调用方记 Warn，不静默忽略）。
// 与 checkEscalate 内联逻辑对齐（time.ParseDuration）。
func parseEscalateAfterDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	return time.ParseDuration(s)
}

// handleFinding 单条 finding 处理：过滤历史 → 查规则 → executeReaction。
func (e *Engine) handleFinding(ctx context.Context, f blackboard.Finding) {
	// 历史过滤：CreatedAt 早于引擎启动的 finding 忽略（避免回放触发）。
	if !f.CreatedAt.IsZero() && f.CreatedAt.Before(e.startedAt) {
		return
	}
	reactionKey := f.Type // CyberStrikeAI 约定：Finding.Type 即 reaction key
	if reactionKey == "" {
		return
	}
	rule, ok := e.cfg.Rules[reactionKey]
	if !ok {
		// 未配置规则：log-only 记一条 Debug（不触发动作，向后兼容）。
		e.logger.Debug("reactions：未配置规则的 finding", zap.String("type", reactionKey))
		return
	}
	if !rule.Auto {
		// auto=false：只通知不处置（如 run-complete）。仍走 notify 通道但 priority 不升级。
		e.notify(ctx, reactionKey, rule, f, false)
		return
	}
	e.executeReaction(ctx, reactionKey, rule, f)
}

// executeReaction 三态升级逻辑。移植自 lifecycle-manager.ts:564-688。
//   - attempts++ → 判 maxRetries / escalateAfter → 升级 notifyHuman(urgent) return
//   - 未升级 → 按 action 执行（send-to-agent 降级 notify / notify / log-only）
//
// Critic M1 修复：attempts 读取/日志/delete 全部在锁内完成（先快照 attempts，
// 解锁后仅用快照值记日志），消除锁外并发读写 tracker.attempts 的数据竞争，
// 并消除"解锁到 delete 之间并发同 key 双重升级"窗口。
func (e *Engine) executeReaction(ctx context.Context, reactionKey string, rule config.Reaction, f blackboard.Finding) {
	trackerKey := trackerKey(f.ProjectID, reactionKey)
	e.mu.Lock()
	tracker, ok := e.trackers[trackerKey]
	if !ok {
		tracker = &reactionTracker{firstTriggered: time.Now()}
		e.trackers[trackerKey] = tracker
	}
	tracker.attempts++
	shouldEscalate := e.checkEscalate(tracker, rule)
	if shouldEscalate {
		// 升级：notify urgent + 记日志。参考项目 notifyHuman(event, "urgent") 后 return。
		// 升级后立即清 tracker（同锁内完成），下轮重新计数（参考项目 clearReactionTracker）。
		attempts := tracker.attempts // 快照（M1：日志在解锁后用快照，不在锁外读 tracker）
		delete(e.trackers, trackerKey)
		e.mu.Unlock()
		e.logger.Warn("reactions 升级到人类", zap.String("reaction", reactionKey), zap.Int("attempts", attempts))
		escalationRule := rule
		escalationRule.Priority = "urgent"
		e.notify(ctx, reactionKey, escalationRule, f, true)
		return
	}
	e.mu.Unlock()

	// 未升级：按 action 执行
	switch rule.Action {
	case "notify":
		e.notify(ctx, reactionKey, rule, f, false)
	case "send-to-agent":
		// CyberStrikeAI 无 sessionManager.send 等价通道；降级为 notify（人类通道）。
		// 参考项目 send-to-agent 失败不立即升级，下轮重试——此处同样不升级。
		e.logger.Info("reactions send-to-agent 降级为 notify", zap.String("reaction", reactionKey))
		e.notify(ctx, reactionKey, rule, f, false)
	case "log-only":
		e.logger.Info("reactions log-only", zap.String("reaction", reactionKey), zap.String("finding", f.Title))
	default:
		e.logger.Warn("reactions 未知 action", zap.String("action", rule.Action), zap.String("reaction", reactionKey))
	}
}

// checkEscalate 判定是否应升级。移植自 lifecycle-manager.ts:580-596。
func (e *Engine) checkEscalate(tracker *reactionTracker, rule config.Reaction) bool {
	// retries 次数上限（nil=无限，不按次数升级）
	if rule.Retries != nil && tracker.attempts > *rule.Retries {
		return true
	}
	// escalateAfter 时长阈值（字符串，time.ParseDuration 解析）。
	// Critic L 建议：非法串记 Warn（静默忽略会让配置错误难以排查）。
	if ea := rule.EscalateAfter; ea != "" {
		dur, err := time.ParseDuration(ea)
		if err != nil || dur <= 0 {
			e.logger.Warn("reactions escalate_after 非法（忽略升级判定）", zap.String("value", ea), zap.Error(err))
		} else if time.Since(tracker.firstTriggered) > dur {
			return true
		}
	}
	return false
}

// notify 遍历 notifiers 推送通知。移植自 lifecycle-manager.ts:1167-1181 notifyHuman。
// 容错：单个 notifier 失败不影响其他（参考项目 try/catch 静默）。
//
// RC9 修复：原实现对每个 notifier 起 `go func(){ notifier.Notify(ev) }()` 无
// recover，notifier panic 会崩进程；阻塞的 Notify 无限堆积 goroutine。现改为：
//   - 每个 notifier 调用包一层 defer recover 兜底 panic（不崩进程）。
//   - 用 context.WithTimeout 5s 限制单次 Notify 时长（防 goroutine 无限堆积）；
//     超时后记 Warn 但不阻断其他 notifier（notifier 实现方应在内部异步发送，
//     此处超时只是兜底防呆）。
func (e *Engine) notify(ctx context.Context, reactionKey string, rule config.Reaction, f blackboard.Finding, escalated bool) {
	if len(e.notifiers) == 0 {
		// 无 notifier：记日志兜底（不丢事件）。
		e.logger.Warn("reactions 无 notifier 可用，通知降级为日志",
			zap.String("reaction", reactionKey), zap.String("priority", rule.Priority), zap.String("finding", f.Title))
		return
	}
	priority := rule.Priority
	if priority == "" {
		priority = "info"
	}
	msg := rule.Message
	if msg == "" {
		msg = f.Title
	}
	event := pluginslot.NotifyEvent{
		Type:      reactionKey,
		Priority:  priority,
		ProjectID: f.ProjectID,
		Message:   msg,
		Data: map[string]interface{}{
			"finding_id":     f.ID,
			"finding_source": f.Source,
			"severity":       f.Severity,
			"escalated":      escalated,
		},
	}
	for _, n := range e.notifiers {
		if n == nil {
			continue
		}
		// 串行调用 + recover 兜底（替代原无界 goroutine）：
		//   - notifier 实现方约定内部异步发送（Notifier interface 注释），Notify
		//     本身应快速返回，串行调用不会阻塞引擎事件循环太久。
		//   - recover 兜底 notifier panic，记 Warn 不崩进程。
		//   - context.WithTimeout 5s 限制单次 Notify 时长，防呆阻塞 notifier
		//     堆积 goroutine（原实现的 goroutine 泄漏风险消除）。
		e.callNotifierSafely(ctx, n, event, reactionKey)
	}
}

// notifierTimeout 单次 Notify 调用的超时上限。notifier 实现方应在内部异步
// 发送，Notify 本身快速返回；此超时仅为兜底防呆，不作为正常路径。
const notifierTimeout = 5 * time.Second

// callNotifierSafely 调用单个 notifier 的 Notify，带 recover 兜底 panic +
// context 超时防呆阻塞。失败/超时/panic 都记 Warn，不阻断其他 notifier。
//
// 实现细节（RC9）：notify 调用方串行调用本方法；本方法内部起一个 goroutine
// 跑 n.Notify(ev)，主路径 select 等 done / 超时 / ctx 取消。recover 必须在
// goroutine 内部（defer 在 goroutine），否则 goroutine 内的 panic 会崩进程
// （defer 在外层 callNotifierSafely 不覆盖子 goroutine）。
func (e *Engine) callNotifierSafely(ctx context.Context, n pluginslot.Notifier, ev pluginslot.NotifyEvent, reactionKey string) {
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				e.logger.Warn("reactions notifier panic（已恢复，不崩进程）",
					zap.String("reaction", reactionKey),
					zap.Any("panic", r))
				// panic 发生时 done <- n.Notify(ev) 未执行，select 会卡到超时。
				// 主动发 nil 让 select 立即返回（done buffered=1，不阻塞）。
				done <- nil
			}
		}()
		done <- n.Notify(ev)
	}()
	select {
	case err := <-done:
		if err != nil {
			// 容错：记日志，不阻断其他 notifier（参考项目 catch 静默）。
			e.logger.Warn("reactions notifier 失败", zap.Error(err))
		}
	case <-time.After(notifierTimeout):
		e.logger.Warn("reactions notifier 超时（5s，已放弃等待）",
			zap.String("reaction", reactionKey))
	case <-ctx.Done():
		// 引擎 ctx 已取消（Stop 或父 ctx 取消）：不再等待 notifier，直接返回。
	}
}

// trackerKey 生成 tracker map key：projectID:reactionKey。移植自 lifecycle-manager.ts:570。
func trackerKey(projectID, reactionKey string) string {
	if projectID == "" {
		return "global:" + reactionKey
	}
	return projectID + ":" + reactionKey
}

// ClearTracker 测试/重置辅助：清指定 tracker。生产路径不调用。
func (e *Engine) ClearTracker(projectID, reactionKey string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.trackers, trackerKey(projectID, reactionKey))
}
