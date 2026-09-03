// Package securityevents 提供安全事件到 blackboard 的发布点（H1）。
//
// 设计动机（Critic H1 修复）：reactions 引擎订阅 blackboard 事件流触发反应式
// 通知，但 HIGH_IMPACT/scope 拦截等安全事件发生在 internal/security /
// internal/multiagent 包内，这些包不能直接依赖 app.go 的 board 实例（依赖方向
// 会反转：app→security 已存在，security→app 不可能）。故用包级注入点：
// app.go 启动时调 SetBoard 把 board 注册进来，事件发生点调 Publish* 函数；
// board 未注册（reactions 未启用/测试环境）时全部为 no-op，零开销。
package securityevents

import (
	"context"
	"fmt"
	"strings"

	"cyberstrike-ai/internal/blackboard"
)

// board 全局事件源（app.go 启动时注入）。nil=未启用（no-op）。
var board blackboard.Board

// SetBoard 注入事件源。由 app.go 在 reactions 接线时调用（传 nil 等价禁用）。
// 幂等：后注覆盖前注（单次启动只调一次）。
func SetBoard(b blackboard.Board) {
	board = b
}

// publish 构造 Finding 并广播；board 未启用/发布失败均静默（安全事件不因
// 通知通道故障阻断主流程——与 executor 的 audit 语义一致）。
func publish(f blackboard.Finding) {
	if board == nil {
		return
	}
	_, _ = board.Publish(context.Background(), f)
}

// PublishHighImpactTool 广播 HIGH_IMPACT 工具执行事件（reaction key: high-impact-tool）。
func PublishHighImpactTool(toolName, risk, conversationID string) {
	f := blackboard.Finding{
		Type:     "high-impact-tool",
		Title:    fmt.Sprintf("HIGH_IMPACT 工具执行: %s（%s）", toolName, risk),
		Severity: "high",
		Source:   "executor",
	}
	if conversationID != "" {
		f.Detail = "conversationId=" + conversationID
	}
	publish(f)
}

// PublishScopeViolation 广播 project scope 越界拦截事件（reaction key: scope-violation）。
func PublishScopeViolation(projectID, toolName, reason string) {
	publish(blackboard.Finding{
		Type:      "scope-violation",
		Title:     "工具目标越界被 project scope 拦截: " + reason,
		Detail:    fmt.Sprintf("project=%s tool=%s reason=%s", projectID, toolName, reason),
		Severity:  "warn",
		Source:    "scope_block",
		ProjectID: projectID,
	})
}

// PublishCapabilityRollback 广播 Capability Provider 回滚事件（reaction key: capability-rollback）。
func PublishCapabilityRollback(toolName, detail string) {
	publish(blackboard.Finding{
		Type:     "capability-rollback",
		Title:    "Capability Provider 执行失败已回滚: " + toolName,
		Detail:   detail,
		Severity: "warning",
		Source:   "capability",
	})
}

// PublishCapabilityArtifacts 广播 Capability Provider 成功执行的备份工件数
// （reaction key: capability-artifacts）。filesystemCapabilityGuard 成功路径
// 收集备份 SHA256 后调用，与 executor_run.go capability 分支对齐。
func PublishCapabilityArtifacts(toolName string, count int) {
	publish(blackboard.Finding{
		Type:     "capability-artifacts",
		Title:    fmt.Sprintf("Capability Provider 执行成功，收集 %d 个备份工件: %s", count, toolName),
		Detail:   fmt.Sprintf("tool=%s artifacts=%d", toolName, count),
		Severity: "info",
		Source:   "capability",
	})
}

// PublishAgentStuck 广播 agent 卡死事件（reaction key: agent-stuck）。
// K9：StuckDetector 四阈值（sameOutputRepeat/sameErrorRepeat/revisionLoop/monologue）
// 任一命中时调用，经 blackboard → reactions 引擎触发 agent-stuck 规则（默认 notify urgent）。
// reason 形如 "same-output-repeat:3" / "same-error-repeat:2" / "revision-loop:4" / "monologue:6"，
// 供通知正文与 audit 追溯。
func PublishAgentStuck(conversationID, reason string) {
	f := blackboard.Finding{
		Type:     "agent-stuck",
		Title:    "Agent 卡死检测触发: " + reason,
		Severity: "high",
		Source:   "stuck_detector",
	}
	if conversationID != "" {
		f.Detail = "conversationId=" + conversationID
	}
	publish(f)
}

// PublishHitlPending 广播 HITL 中断事件（reaction key: hitl-pending）。
// P1-3：原 deriveSessionStatus 依赖 hitl-pending finding，但全仓生产代码
// 从未发布该 Type（HITL 中断只走 SSE 到前端），reactions lifecycle 的
// hitl_pending 状态永不派生（空转）。由 handler.waitHITLApproval 在
// CreatePendingInterrupt 成功后调用，补齐事件源。board 未注入时 no-op。
func PublishHitlPending(conversationID, toolName, interruptID string) {
	f := blackboard.Finding{
		Type:     "hitl-pending",
		Title:    "HITL 中断待审批: " + toolName,
		Severity: "warning",
		Source:   "hitl",
	}
	parts := make([]string, 0, 3)
	if conversationID != "" {
		parts = append(parts, "conversationId="+conversationID)
	}
	if toolName != "" {
		parts = append(parts, "tool="+toolName)
	}
	if interruptID != "" {
		parts = append(parts, "interruptId="+interruptID)
	}
	if len(parts) > 0 {
		f.Detail = strings.Join(parts, " ")
	}
	publish(f)
}

// PublishRunComplete 广播 run 正常完成事件（reaction key: run-complete）。
// P1-3：deriveSessionStatus 的 done 状态依赖 run-complete finding，原全仓
// 生产代码无发布点。由 multiagent 的 eino run loop 正常退出前调用。
// run 异常/取消走 failed finding，不发本事件。board 未注入时 no-op。
func PublishRunComplete(conversationID string) {
	f := blackboard.Finding{
		Type:     "run-complete",
		Title:    "Agent run 正常完成",
		Severity: "info",
		Source:   "multiagent",
	}
	if conversationID != "" {
		f.Detail = "conversationId=" + conversationID
	}
	publish(f)
}

// PublishToolPending 广播工具调用待执行事件（reaction key: tool-pending）。
// P2-3：reactions lifecycle 状态机 deriveSessionStatus 需要真实 Type 的
// pending tool_call 信号（原 Detail 含 "pending" 的启发式对 hitl-pending
// 等 finding 永不命中）。由 executor 工具执行入口调用；board 未注入时 no-op。
func PublishToolPending(conversationID, toolName, toolCallID string) {
	f := blackboard.Finding{
		Type:     "tool-pending",
		Title:    "工具调用待执行: " + toolName,
		Severity: "info",
		Source:   "executor",
	}
	parts := make([]string, 0, 3)
	if conversationID != "" {
		parts = append(parts, "conversationId="+conversationID)
	}
	if toolName != "" {
		parts = append(parts, "tool="+toolName)
	}
	if toolCallID != "" {
		parts = append(parts, "toolCallId="+toolCallID)
	}
	if len(parts) > 0 {
		f.Detail = strings.Join(parts, " ")
	}
	publish(f)
}
