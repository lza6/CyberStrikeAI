// Package capability 提供破坏性工具的 Capability Provider 生命周期：
// supports → plan → validate → execute → rollback → collect_artifacts。
// 设计移植自 PE-reverse-skill providers/base.py（Go 重写，非 Python 搬运）。
// 只有注册了 provider 的破坏性工具走该生命周期；其余工具走原路径（向后兼容）。
package capability

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"cyberstrike-ai/internal/mcp"
)

// Plan 执行计划（预演产物）。
type Plan struct {
	Description string `json:"description"`            // 将执行什么
	Target      string `json:"target"`                 // 目标（文件路径/URL 等）
	Action      string `json:"action"`                 // 动作（modify/delete/create）
	BackupPath  string `json:"backup_path,omitempty"`  // 备份文件路径（execute 前填充）
}

// Artifact 证据工件（相对路径 + SHA256 + provenance）。
type Artifact struct {
	Path       string `json:"path"`                  // 相对 artifacts 根的路径
	SHA256     string `json:"sha256"`                // 内容哈希
	Provenance string `json:"provenance"`            // 来源（工具名+时间）
	CreatedAt  string `json:"created_at"`
}

// CapabilityProvider 破坏性工具生命周期接口。
type CapabilityProvider interface {
	// Supports 是否支持该工具（注册表按工具名路由，通常恒 true）。
	Supports(toolName string) bool
	// Plan 预演：描述将执行什么（供审计/UI 确认）。
	Plan(args map[string]interface{}) (Plan, error)
	// Validate 执行前校验（目标存在/可写/参数合法）。
	Validate(args map[string]interface{}) error
	// Execute 实际执行（provider 自管备份）。
	Execute(ctx context.Context, args map[string]interface{}) (*mcp.ToolResult, error)
	// Rollback 回滚到执行前状态（用 Plan.BackupPath）。
	Rollback(ctx context.Context, plan Plan) error
	// CollectArtifacts 收集证据工件（备份 SHA256 等）。
	CollectArtifacts(plan Plan) ([]Artifact, error)
}

// registry 工具名 → provider。
var registry = map[string]CapabilityProvider{}

// Register 注册 provider（重复注册覆盖）。
func Register(toolName string, p CapabilityProvider) {
	registry[toolName] = p
}

// GetProvider 查工具的 provider；未注册返回 nil。
func GetProvider(toolName string) CapabilityProvider {
	return registry[toolName]
}

// SupportedTools 返回已注册 provider 的工具名列表。
func SupportedTools() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	return out
}

// ComputeSHA256 文件内容哈希。
func ComputeSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// BackupFile 把 src 复制到 backupDir（按时间戳命名），返回备份路径。
func BackupFile(src, backupDir string) (string, error) {
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s_%s.bak", filepath.Base(src), time.Now().Format("20060102_150405"))
	dst := filepath.Join(backupDir, name)
	data, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(dst, data, 0644); err != nil {
		return "", err
	}
	return dst, nil
}

// RestoreFile 把备份恢复到 src。
func RestoreFile(backupPath, src string) error {
	data, err := os.ReadFile(backupPath)
	if err != nil {
		return err
	}
	return os.WriteFile(src, data, 0644)
}
