package security

import (
	"context"

	"cyberstrike-ai/internal/authctx"
	"cyberstrike-ai/internal/mcp"
)

// highImpactAuditRecorder 由上层（app.go）注入，用于在 HIGH_IMPACT 工具执行时
// 写一条 platform audit 记录。第二道“标记闸”：不阻断执行，仅审计留痕。
// 不依赖 audit 包类型，避免 security→audit→security 循环依赖。
type highImpactAuditRecorder interface {
	RecordHighImpactTool(executor, conversationID, toolName, risk string)
}

// hitlWhitelistChecker 由上层（handler）注入，回答“该工具是否在 HITL 免审批白名单”。
// 用于 HIGH_IMPACT 标记闸判定：白名单内的工具（如 tool_search/exit 等元工具）不打 high_impact 标。
// 返回 (inWhitelist, available)：available=false 表示未注入 checker，此时保守地标记。
type hitlWhitelistChecker interface {
	IsToolWhitelisted(conversationID, toolName string) bool
}

// projectScopeResolver 由上层（app.go）注入，按 projectID 返回该项目的授权范围 Scope。
// J4：会话级授权边界硬闸。executor 在工具执行前调用，把 project scope 与工具 target
// 合并校验，越界目标返回 IsError（不执行）。nil=未注入，跳过 project scope 校验（向后兼容）。
type projectScopeResolver interface {
	ResolveProjectScope(projectID string) Scope
}

// SetHITLWhitelist 注入 HITL 免审批白名单判定器；用于 HIGH_IMPACT 标记闸。
// 由 app.go 在 NewExecutor 后调用，传入 AgentHandler 的 NeedsToolApproval 反查能力。
func (e *Executor) SetHITLWhitelist(c hitlWhitelistChecker) {
	e.hitlWhitelist = c
}

// SetHighImpactAuditRecorder 注入审计记录器，HIGH_IMPACT 工具执行时记一条 audit。
func (e *Executor) SetHighImpactAuditRecorder(r highImpactAuditRecorder) {
	e.auditRecorder = r
}

// SetProjectScopeResolver 注入 project 级授权范围解析器（J4 会话级硬闸）。
// app.go 在 NewExecutor 后调用，传入 db 适配器：按 projectID 读 scope_json。
// nil=不启用 project scope 校验（向后兼容）。
func (e *Executor) SetProjectScopeResolver(r projectScopeResolver) {
	e.projectScope = r
}

// markHighImpact 在 ToolResult 上打 HIGH_IMPACT 标记（元数据，不阻断）。
// 仅当工具命中 HighImpactTools 且非白名单时打标；否则原样返回 result。
// nil-safe：result 为 nil 时返回 nil。
func (e *Executor) markHighImpact(result *mcp.ToolResult, hit bool, risk string) *mcp.ToolResult {
	if result == nil || !hit {
		return result
	}
	result.HighImpact = true
	result.RiskNote = risk
	return result
}

// isToolWhitelisted 判断工具是否在 HITL 免审批白名单内（白名单内不打标）。
// 未注入 checker 时保守返回 false（即标记为非白名单 → 打标）。
func (e *Executor) isToolWhitelisted(conversationID, toolName string) bool {
	if e == nil || e.hitlWhitelist == nil {
		return false
	}
	return e.hitlWhitelist.IsToolWhitelisted(conversationID, toolName)
}

// currentActor 从 ctx 中提取当前操作者（principal username），用于审计记录。
// 无 principal 时返回空串，由 audit.RecordSystem 兜底为 "admin"。
func (e *Executor) currentActor(ctx context.Context) string {
	if p, ok := authctx.PrincipalFromContext(ctx); ok {
		return p.Username
	}
	return ""
}
