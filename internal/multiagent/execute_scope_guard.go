package multiagent

import (
	"strings"

	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/security"
	"cyberstrike-ai/internal/securityevents"

	"go.uber.org/zap"
)

// newExecuteScopeGuard 构建 Eino 内置 execute 工具的授权范围闸（J4）。
// projectID/db 为空时返回 nil（不校验，向后兼容）。
// 与 internal/security/executor.go 的 project scope 同源：按 projectID 读
// projects.scope_json，越界目标在 execute 真正执行前被拦。
// OnViolation（H1）：越界拦截广播 scope-violation 安全事件（securityevents 包，
// app.go 注入 board；board 未启用时 publishSecurityEvent 为 no-op）。
func newExecuteScopeGuard(db *database.DB, projectID string, logger *zap.Logger) *security.ExecuteScopeGuard {
	pid := strings.TrimSpace(projectID)
	if pid == "" || db == nil {
		return nil
	}
	return &security.ExecuteScopeGuard{
		Resolve: func(projectID string) security.Scope {
			return security.ScopeFromProject(db, projectID)
		},
		Logger: logger,
		OnViolation: func(pID, toolName, reason string) {
			securityevents.PublishScopeViolation(pID, toolName, reason)
		},
	}
}
