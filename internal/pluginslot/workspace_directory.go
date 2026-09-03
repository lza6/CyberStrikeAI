package pluginslot

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DirectoryWorkspace 是 SlotWorkspace 的纯目录隔离实现。
//
// 与主项目 internal/project/workspace.go 的隔离机制一致（纯目录，无 git/无 chroot），
// 但作为 SlotWorkspace 的独立 Factory 注册，供 worker 隔离场景统一调度。
//
// 设计（与参考项目 scratch/workspace.go 对齐）：
//   - 每 worker 独立子目录 {managedRoot}/{projectID}/{sessionID}/。
//   - validateManagedPath 强制路径在 managedRoot 内（防 traversal）。
//   - validatePathComponent 拒绝 ".."/路径分隔符（防 traversal）。
//   - Destroy 拒绝非空目录（ErrWorkspaceDirty）保护未提交工作。
//
// 零外部依赖：仅标准库 os/path/filepath/strings。
type DirectoryWorkspace struct {
	managedRoot string
}

// NewDirectoryWorkspace 构造。managedRoot 是所有 worker 工作区根目录。
func NewDirectoryWorkspace(managedRoot string) *DirectoryWorkspace {
	return &DirectoryWorkspace{managedRoot: strings.TrimSpace(managedRoot)}
}

// Create 实现 Workspace。为 worker 建独立子目录。
func (d *DirectoryWorkspace) Create(cfg WorkspaceConfig) (WorkspaceInfo, error) {
	if d == nil {
		return WorkspaceInfo{}, errors.New("directory workspace: nil receiver")
	}
	if err := validateDirectoryConfig(cfg); err != nil {
		return WorkspaceInfo{}, err
	}
	path, err := d.managedPath(cfg)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return WorkspaceInfo{}, fmt.Errorf("directory workspace: mkdir: %w", err)
	}
	return WorkspaceInfo{
		Path:      path,
		SessionID: cfg.SessionID,
		ProjectID: cfg.ProjectID,
		Isolation: IsolationDirectory,
	}, nil
}

// Restore 实现 Workspace。容忍已存在目录（复用，不报错）。
func (d *DirectoryWorkspace) Restore(cfg WorkspaceConfig) (WorkspaceInfo, error) {
	info, err := d.Create(cfg)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	return info, nil
}

// Destroy 实现 Workspace。拒绝非空目录（ErrWorkspaceDirty）保护未提交工作。
func (d *DirectoryWorkspace) Destroy(info WorkspaceInfo) error {
	path := strings.TrimSpace(info.Path)
	if path == "" {
		return ErrWorkspaceNotFound
	}
	// 验证路径在 managedRoot 内（防 traversal 误删）。
	if d != nil && d.managedRoot != "" {
		if err := validateManagedPath(path, d.managedRoot); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrWorkspaceNotFound
		}
		return fmt.Errorf("directory workspace: read dir: %w", err)
	}
	if len(entries) > 0 {
		return ErrWorkspaceDirty
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("directory workspace: remove: %w", err)
	}
	return nil
}

// managedPath 计算并验证 worker 目录路径：{managedRoot}/{projectID}/{sessionID}。
func (d *DirectoryWorkspace) managedPath(cfg WorkspaceConfig) (string, error) {
	root := strings.TrimSpace(d.managedRoot)
	if root == "" {
		root = filepath.Join("tmp", "workspace", "workers")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("directory workspace: abs root: %w", err)
	}
	pid := validatePathComponent(cfg.ProjectID, "default")
	sid := validatePathComponent(cfg.SessionID, "default")
	if cfg.Kind == "orchestrator" {
		sid = sid + "-orchestrator"
	}
	return filepath.Join(abs, pid, sid), nil
}

// validateDirectoryConfig 校验配置非空。
func validateDirectoryConfig(cfg WorkspaceConfig) error {
	if strings.TrimSpace(cfg.SessionID) == "" {
		return errors.New("directory workspace: sessionID is empty")
	}
	return nil
}

// validatePathComponent 拒绝 ".."/路径分隔符，移植自参考项目 gitworktree/workspace.go validatePathComponent。
//
// **与参考项目的策略差异（有意为之，审计发现 6 披露）**：参考项目遇不安全组件返回
// ErrUnsafePath 硬报错；本实现静默改写（.. → __、分隔符 → -、超长截断），与主项目
// internal/project/workspace.go sanitizeWorkspacePathSegment 策略一致。代价：字面
// "a-b" 与 "a/b" 映射到同一路径（碰撞面），调用方应避免仅靠分隔符区分 sessionID；
// 安全性不受影响（改写后的路径不可能包含 .. 或路径分隔符）。
//
// 空值降级为 fallback（非报错，便于默认场景）。
func validatePathComponent(s, fallback string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	if strings.Contains(s, "..") {
		return strings.ReplaceAll(s, "..", "__")
	}
	s = strings.ReplaceAll(s, string(filepath.Separator), "-")
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, "\\", "-")
	if len(s) > 180 {
		s = s[:180]
	}
	return s
}

// validateManagedPath 强制 path 在 root 内（防 traversal 误删）。
// 移植自参考项目 gitworktree/workspace.go validateManagedPath + pathWithin。
// Windows 8.3 短名注意：root（如 t.TempDir）可能是短名（ADMINI~1.DES），path 是长名，
// 故两侧都过 EvalSymlinks 展开后再比较。
func validateManagedPath(path, root string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("validate managed path: abs path: %w", err)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("validate managed path: abs root: %w", err)
	}
	// 展开 8.3 短名（EvalSymlinks 对存在的路径生效；不存在的 path 用父目录展开）。
	if resolved, err := filepath.EvalSymlinks(absRoot); err == nil {
		absRoot = resolved
	}
	if resolved, err := resolveLongPath(absPath); err == nil {
		absPath = resolved
	}
	// filepath.Rel 保证 absPath 在 absRoot 下（否则 ../ 逃逸）。
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return fmt.Errorf("validate managed path: rel: %w", err)
	}
	if strings.HasPrefix(rel, "..") || rel == ".." {
		return fmt.Errorf("validate managed path: path %q escapes root %q", path, root)
	}
	return nil
}

// resolveLongPath 展开路径中的 8.3 短名组件（对已存在的最深前缀做 EvalSymlinks，
// 不存在部分保留原样）。path 可能尚未创建（Create 前校验场景）。
func resolveLongPath(path string) (string, error) {
	// 尝试直接展开（路径已存在场景）。
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved, nil
	}
	// 路径不存在：逐级向上找存在的最深祖先，展开后拼回剩余部分。
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	for dir != filepath.Dir(dir) { // 未到根
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(resolved, base), nil
		}
		base = filepath.Join(filepath.Base(dir), base)
		dir = filepath.Dir(dir)
	}
	return path, nil // 无法展开（罕见），原样返回
}

// gitAvailable 报告系统是否装了 git（git-worktree 模式探测用）。
func gitAvailable() bool {
	_, err := exec.LookPath("git")
	return err == nil
}
