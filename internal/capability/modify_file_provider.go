package capability

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"cyberstrike-ai/internal/mcp"
)

// ModifyFileProvider modify-file 工具的 Capability Provider：
// Plan 显示将修改/创建哪个文件 → Validate 检查父目录存在/目标可写 → Execute 前先备份
// （已存在文件）→ 失败可 Rollback（已存在恢复，新建删除）→ CollectArtifacts 返回备份 SHA256。
type ModifyFileProvider struct {
	// BackupDir 备份根目录（如 <spillRoot>/capability-backup）。空=默认 tmp。
	BackupDir string
}

// NewModifyFileProvider 构造并注册到注册表。
func NewModifyFileProvider(backupDir string) *ModifyFileProvider {
	p := &ModifyFileProvider{BackupDir: backupDir}
	Register("modify-file", p)
	return p
}

// Supports 恒 true（按工具名注册路由）。
func (p *ModifyFileProvider) Supports(toolName string) bool { return toolName == "modify-file" }

// Plan 预演：将修改/创建 args["path"] 指定的文件。
func (p *ModifyFileProvider) Plan(args map[string]interface{}) (Plan, error) {
	path, err := requirePathArg(args)
	if err != nil {
		return Plan{}, err
	}
	action := "modify"
	if _, statErr := os.Stat(path); statErr != nil {
		action = "create"
	}
	desc := "将修改文件 " + path + " 的内容"
	if action == "create" {
		desc = "将创建文件 " + path
	}
	return Plan{
		Description: desc,
		Target:      path,
		Action:      action,
	}, nil
}

// Validate 文件存在→校验可写；不存在→视为新建（对齐 Eino write_file 语义），校验父目录存在。
// 这避免 AI 用 write_file 创建新文件时被"文件不存在"误拦（P0 行为对齐修复）。
func (p *ModifyFileProvider) Validate(args map[string]interface{}) error {
	path, err := requirePathArg(args)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err == nil {
		// 文件存在：校验是文件且可写
		if info.IsDir() {
			return fmt.Errorf("目标是目录不是文件: %s", path)
		}
		f, oerr := os.OpenFile(path, os.O_WRONLY, 0)
		if oerr != nil {
			return fmt.Errorf("目标文件不可写: %s", path)
		}
		f.Close()
		return nil
	}
	// 文件不存在：视为新建（CreateFile 语义），校验父目录存在
	dir := filepath.Dir(path)
	if dinfo, derr := os.Stat(dir); derr != nil || !dinfo.IsDir() {
		return fmt.Errorf("目标父目录不存在: %s", dir)
	}
	return nil
}

// Execute 备份原文件（若已存在）→ 写入新内容 → 返回结果（含备份路径供回滚）。
// 新建文件（原不存在）不备份，仅记录"新建"。写入失败尝试回滚。
func (p *ModifyFileProvider) Execute(ctx context.Context, args map[string]interface{}) (*mcp.ToolResult, error) {
	path, err := requirePathArg(args)
	if err != nil {
		return nil, err
	}
	content, _ := args["content"].(string)
	// 确保父目录存在（对齐 Eino write_file 的 MkdirAll 语义）。
	if dir := filepath.Dir(path); dir != "" {
		if mkErr := os.MkdirAll(dir, 0755); mkErr != nil {
			return nil, fmt.Errorf("创建父目录失败: %w", mkErr)
		}
	}
	backupDir := p.BackupDir
	if backupDir == "" {
		backupDir = filepath.Join(os.TempDir(), "capability-backup")
	}
	// 仅对已存在的文件做备份（新建文件无原内容可备份）。
	hadExisting := false
	if _, statErr := os.Stat(path); statErr == nil {
		hadExisting = true
		backupPath, berr := BackupFile(path, backupDir)
		if berr != nil {
			return nil, fmt.Errorf("执行前备份失败: %w", berr)
		}
		// 记录到 args 供 Rollback（plan 不携带，用 args 暂存）。
		args["_backup_path"] = backupPath
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		if hadExisting {
			if bp, ok := args["_backup_path"].(string); ok && bp != "" {
				_ = RestoreFile(bp, path)
			}
		}
		return nil, fmt.Errorf("写入失败（已回滚）: %w", err)
	}
	if hadExisting {
		bp, _ := args["_backup_path"].(string)
		return &mcp.ToolResult{
			Content: []mcp.Content{{Type: "text", Text: fmt.Sprintf("已修改 %s（备份: %s）", path, bp)}},
		}, nil
	}
	return &mcp.ToolResult{
		Content: []mcp.Content{{Type: "text", Text: fmt.Sprintf("已创建 %s", path)}},
	}, nil
}

// Rollback 用 plan.BackupPath 恢复；新建文件（无备份）删除新建文件。
func (p *ModifyFileProvider) Rollback(ctx context.Context, plan Plan) error {
	if plan.BackupPath == "" {
		// 新建文件场景：删除新建的文件。
		if plan.Target != "" {
			if _, err := os.Stat(plan.Target); err == nil {
				return os.Remove(plan.Target)
			}
		}
		return fmt.Errorf("无备份路径且目标不存在，无法回滚")
	}
	return RestoreFile(plan.BackupPath, plan.Target)
}

// CollectArtifacts 返回备份文件的 SHA256 证据。
func (p *ModifyFileProvider) CollectArtifacts(plan Plan) ([]Artifact, error) {
	if plan.BackupPath == "" {
		return nil, nil
	}
	sum, err := ComputeSHA256(plan.BackupPath)
	if err != nil {
		return nil, err
	}
	return []Artifact{{
		Path:       filepath.Base(plan.BackupPath),
		SHA256:     sum,
		Provenance: "modify-file backup",
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
	}}, nil
}

// requirePathArg 提取 args 中的 path/file 参数。
func requirePathArg(args map[string]interface{}) (string, error) {
	for _, k := range []string{"path", "file", "file_path"} {
		if v, ok := args[k]; ok {
			if s, isStr := v.(string); isStr && s != "" {
				return s, nil
			}
		}
	}
	return "", fmt.Errorf("缺少 path/file 参数")
}
