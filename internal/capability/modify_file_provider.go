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
// Plan 显示将修改哪个文件 → Validate 检查存在+可写 → Execute 前先备份
// → 失败可 Rollback → CollectArtifacts 返回备份 SHA256。
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

// Plan 预演：将修改 args["path"] 指定的文件。
func (p *ModifyFileProvider) Plan(args map[string]interface{}) (Plan, error) {
	path, err := requirePathArg(args)
	if err != nil {
		return Plan{}, err
	}
	return Plan{
		Description: fmt.Sprintf("将修改文件 %s 的内容", path),
		Target:      path,
		Action:      "modify",
	}, nil
}

// Validate 文件存在且可写。
func (p *ModifyFileProvider) Validate(args map[string]interface{}) error {
	path, err := requirePathArg(args)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("目标文件不存在: %s", path)
	}
	if info.IsDir() {
		return fmt.Errorf("目标是目录不是文件: %s", path)
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("目标文件不可写: %s", path)
	}
	f.Close()
	return nil
}

// Execute 备份原文件 → 写入新内容 → 返回结果（含备份路径供回滚）。
func (p *ModifyFileProvider) Execute(ctx context.Context, args map[string]interface{}) (*mcp.ToolResult, error) {
	path, err := requirePathArg(args)
	if err != nil {
		return nil, err
	}
	content, _ := args["content"].(string)
	backupDir := p.BackupDir
	if backupDir == "" {
		backupDir = filepath.Join(os.TempDir(), "capability-backup")
	}
	backupPath, err := BackupFile(path, backupDir)
	if err != nil {
		return nil, fmt.Errorf("执行前备份失败: %w", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		// 写入失败尝试回滚
		_ = RestoreFile(backupPath, path)
		return nil, fmt.Errorf("写入失败（已回滚）: %w", err)
	}
	return &mcp.ToolResult{
		Content: []mcp.Content{{Type: "text", Text: fmt.Sprintf("已修改 %s（备份: %s）", path, backupPath)}},
	}, nil
}

// Rollback 用 Plan.BackupPath 恢复。
func (p *ModifyFileProvider) Rollback(ctx context.Context, plan Plan) error {
	if plan.BackupPath == "" {
		return fmt.Errorf("无备份路径，无法回滚")
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
