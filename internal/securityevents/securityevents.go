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
