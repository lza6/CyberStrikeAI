package multiagent

import (
	"context"
	"fmt"
	"strings"

	"cyberstrike-ai/internal/capability"
	"cyberstrike-ai/internal/einomcp"
	"cyberstrike-ai/internal/securityevents"
)

// filesystemCapabilityGuard J5：Eino 内置 write_file 的破坏性工具能力闸。
// 把 Eino 工具名映射到 capability provider，命中时走 plan→validate→execute→rollback
// 生命周期（与 security.Executor 对 modify-file 等的处置一致）。
//
// 设计要点：
// - 仅 write_file 映射到 modify-file provider；edit_file **不映射**（参数语义不同：
//   edit_file 用 old_string/new_string 局部替换，provider 的 content 整文件写入会把
//   文件清空——Critic 终审 P0），edit_file 走原 wrapped 路径还原生语义。
//   read_file/ls/glob/grep 只读不映射；其余工具返回 (不拦) 走原 wrapped 路径（向后兼容）。
// - 越界/校验失败返回 (拦截文本, blocked=true, success=false)：调用方返回该文本不执行原 wrapped。
// - provider execute 成功返回 (结果文本, blocked=true, success=true)：调用方返回该文本
//   （provider 已实际执行含备份），跳过原 wrapped 避免双写。success 用于区分成功与拦截。
// - execute 失败自动 Rollback（与 security.Executor 一致）；成功路径收集备份 SHA256 工件审计。
type filesystemCapabilityGuard struct{}

// newFilesystemCapabilityGuard 构造能力闸。
func newFilesystemCapabilityGuard() *filesystemCapabilityGuard {
	return &filesystemCapabilityGuard{}
}

// CheckFilesystemTool 校验 write_file/edit_file 是否允许执行。
// 返回 (结果文本, blocked, success)。blocked=true 时调用方应直接返回该文本，不执行原 wrapped。
// success=true 表示 provider 已成功执行（结果文本为执行结果）；false 表示被拦/失败（文本为错误）。
//
// Rollback 闭环：provider.Execute 内部把备份路径回写到 args["_backup_path"]
// （modify_file_provider.go），此处从 args 取出回填 plan.BackupPath，确保 Rollback 拿得到备份路径。
func (g *filesystemCapabilityGuard) CheckFilesystemTool(ctx context.Context, toolName string, args map[string]interface{}) (string, bool, bool) {
	provider, mapped := resolveFilesystemProvider(toolName)
	if !mapped || provider == nil {
		return "", false, false
	}
	plan, perr := provider.Plan(args)
	if perr != nil {
		return capabilityError("Capability Plan 失败", perr), true, false
	}
	if verr := provider.Validate(args); verr != nil {
		return capabilityError("Capability Validate 失败", verr), true, false
	}
	result, xerr := provider.Execute(ctx, args)
	if xerr != nil {
		// 从 args 回填 plan.BackupPath（Execute 已把备份路径暂存在 args["_backup_path"]）。
		if bp, ok := args["_backup_path"].(string); ok && bp != "" {
			plan.BackupPath = bp
		}
		if rberr := provider.Rollback(ctx, plan); rberr != nil {
			securityevents.PublishCapabilityRollback(toolName, fmt.Sprintf("%v (rollback: %v)", xerr, rberr))
			return capabilityError("执行失败且回滚异常", fmt.Errorf("%w (rollback: %v)", xerr, rberr)), true, false
		}
		// H1：回滚成功也广播（capability-rollback 事件，reactions 引擎触发通知）。
		securityevents.PublishCapabilityRollback(toolName, xerr.Error())
		return capabilityError("执行失败已回滚", xerr), true, false
	}
	if result != nil && len(result.Content) > 0 {
		// provider 已实际执行（含备份），返回其结果文本，跳过原 wrapped 避免双写。
		// 成功路径同步收集备份 SHA256 审计工件（与 executor_run.go capability 分支对齐）。
		if bp, ok := args["_backup_path"].(string); ok && bp != "" {
			plan.BackupPath = bp
			if arts, aerr := provider.CollectArtifacts(plan); aerr == nil && len(arts) > 0 {
				securityevents.PublishCapabilityArtifacts(toolName, len(arts))
			}
		}
		return result.Content[0].Text, true, true
	}
	return "", true, true
}

// resolveFilesystemProvider 把 Eino 内置工具名映射到 capability provider。
// 仅 write_file → modify-file provider（创建/覆盖，语义与 provider 的"整文件写入"一致）。
// edit_file 不映射：其参数是 file_path/old_string/new_string/replace_all（无 content 键），
// 经 provider 会把 args["content"] 缺省为空串 → 整文件被清空（Critic 终审 P0）。edit_file
// 走原 wrapped 路径还原生 Edit 语义（读原文→替换→写回），破坏性由 HITL/HIGH_IMPACT 管控。
// read_file/ls/glob/grep 只读，不映射（无破坏性）。
func resolveFilesystemProvider(toolName string) (capability.CapabilityProvider, bool) {
	n := strings.ToLower(strings.TrimSpace(toolName))
	var providerName string
	switch n {
	case "write_file":
		providerName = "modify-file"
	default:
		return nil, false
	}
	p := capability.GetProvider(providerName)
	if p == nil || !p.Supports(providerName) {
		return nil, false
	}
	return p, true
}

// capabilityError 拼装拦截提示文本，带 ToolErrorPrefix 供 einomcp 桥标记 IsError。
func capabilityError(prefix string, err error) string {
	return einomcp.ToolErrorPrefix + fmt.Sprintf("%s: %v", prefix, err)
}

