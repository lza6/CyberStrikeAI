// Package swarm — git worktree 隔离：每个 teammate agent 一个独立 worktree。
//
// 移植自 OpenHarness swarm/worktree.py WorktreeManager。
// 路径：<HomeDir>/worktrees/<flat_slug>/
// 分支：worktree-<flat_slug>
// 复用大目录（node_modules/.venv 等）软链，避免重复。
package swarm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// WorktreeInfo 描述一个受管 git worktree。移植自 OpenHarness worktree.py:63。
type WorktreeInfo struct {
	Slug         string
	Path         string
	Branch       string
	OriginalPath string
	CreatedAt    time.Time
	AgentID      string
}

var (
	validSegment      = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)
	maxSlugLength     = 64
	commonSymlinkDirs = []string{"node_modules", ".venv", "__pycache__", ".tox"}
)

// ValidateWorktreeSlug 校验 slug。移植自 OpenHarness worktree.py:21。
//
// 规则：≤64 字符；每段 [a-zA-Z0-9._-]+；禁 . 和 .. 段；禁绝对路径。
func ValidateWorktreeSlug(slug string) error {
	if slug == "" {
		return errors.New("swarm: worktree slug must not be empty")
	}
	if len(slug) > maxSlugLength {
		return fmt.Errorf("swarm: worktree slug must be %d chars or fewer (got %d)", maxSlugLength, len(slug))
	}
	if strings.HasPrefix(slug, "/") || strings.HasPrefix(slug, "\\") {
		return fmt.Errorf("swarm: worktree slug must not be an absolute path: %q", slug)
	}
	for _, seg := range strings.Split(slug, "/") {
		if seg == "." || seg == ".." {
			return fmt.Errorf("swarm: worktree slug %q: must not contain \".\" or \"..\" segments", slug)
		}
		if !validSegment.MatchString(seg) {
			return fmt.Errorf("swarm: worktree slug %q: each segment must be non-empty and match [a-zA-Z0-9._-]+", slug)
		}
	}
	return nil
}

// flattenSlug 将 '/' 替换为 '+'，避免嵌套目录/分支命名问题。移植自 worktree.py:78。
func flattenSlug(slug string) string {
	return strings.ReplaceAll(slug, "/", "+")
}

func worktreeBranch(slug string) string {
	return "worktree-" + flattenSlug(slug)
}

// runGit 执行 git 命令，返回 (code, stdout, stderr)。移植自 worktree.py:87。
func runGit(ctx context.Context, args []string, cwd string) (int, string, string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=")
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), stdout.String(), stderr.String(), nil
		}
		return -1, "", "", fmt.Errorf("swarm: git exec: %w", err)
	}
	return 0, stdout.String(), stderr.String(), nil
}

// symlinkCommonDirs 将大目录从主仓软链到 worktree，避免重复。移植自 worktree.py:105。
func symlinkCommonDirs(repoPath, worktreePath string) {
	for _, name := range commonSymlinkDirs {
		src := filepath.Join(repoPath, name)
		dst := filepath.Join(worktreePath, name)
		if _, err := os.Lstat(dst); err == nil {
			continue // 已存在
		}
		if _, err := os.Stat(src); os.IsNotExist(err) {
			continue
		}
		_ = os.Symlink(src, dst)
	}
}

// removeSymlinks 清理 symlinkCommonDirs 创建的软链。移植自 worktree.py:120。
func removeSymlinks(worktreePath string) {
	for _, name := range commonSymlinkDirs {
		dst := filepath.Join(worktreePath, name)
		if info, err := os.Lstat(dst); err == nil && info.Mode()&os.ModeSymlink != 0 {
			_ = os.Remove(dst)
		}
	}
}

// worktreeMeta 是 worktree 元数据 JSON（记录 slug 与 agentID，供 List/CleanupStale 恢复）。
type worktreeMeta struct {
	Slug    string `json:"slug"`
	AgentID string `json:"agent_id"`
}

const worktreeMetaFile = ".swarm-meta.json"

// writeWorktreeMeta 写元数据文件到 worktree 根。
func writeWorktreeMeta(wtPath string, m worktreeMeta) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(wtPath, worktreeMetaFile), data, 0o600)
}

// readWorktreeMeta 读元数据文件；不存在或损坏返回零值。
func readWorktreeMeta(wtPath string) worktreeMeta {
	var m worktreeMeta
	if data, err := os.ReadFile(filepath.Join(wtPath, worktreeMetaFile)); err == nil {
		_ = json.Unmarshal(data, &m)
	}
	return m
}

// WorktreeManager 管理隔离 agent 执行的 git worktree。移植自 worktree.py:135。
type WorktreeManager struct {
	baseDir string // 默认 <HomeDir>/worktrees/
}

// NewWorktreeManager 创建 manager。homeDir 为空时返回错误。
func NewWorktreeManager(homeDir string) (*WorktreeManager, error) {
	if strings.TrimSpace(homeDir) == "" {
		return nil, errors.New("swarm: worktree homeDir is empty")
	}
	return &WorktreeManager{baseDir: filepath.Join(homeDir, "worktrees")}, nil
}

// Create 创建（或恢复）一个 worktree。移植自 worktree.py:150 create_worktree。
func (w *WorktreeManager) Create(ctx context.Context, repoPath, slug, branch, agentID string) (WorktreeInfo, error) {
	if err := ValidateWorktreeSlug(slug); err != nil {
		return WorktreeInfo{}, err
	}
	absRepo, err := filepath.Abs(repoPath)
	if err != nil {
		return WorktreeInfo{}, fmt.Errorf("swarm: resolve repo path: %w", err)
	}
	if err := os.MkdirAll(w.baseDir, 0o700); err != nil {
		return WorktreeInfo{}, fmt.Errorf("swarm: mkdir worktree base: %w", err)
	}
	flat := flattenSlug(slug)
	wtPath := filepath.Join(w.baseDir, flat)
	wtBranch := branch
	if wtBranch == "" {
		wtBranch = worktreeBranch(slug)
	}

	// 快速恢复：worktree 目录已存在且是有效 git worktree
	if info, err := os.Stat(wtPath); err == nil && info.IsDir() {
		code, _, _, _ := runGit(ctx, []string{"rev-parse", "--git-dir"}, wtPath)
		if code == 0 {
			meta := readWorktreeMeta(wtPath)
			meta.AgentID = agentID // 调用方覆盖（resume 时更新归属）
			_ = writeWorktreeMeta(wtPath, meta)
			return WorktreeInfo{
				Slug: slug, Path: wtPath, Branch: wtBranch,
				OriginalPath: absRepo, CreatedAt: info.ModTime(), AgentID: agentID,
			}, nil
		}
	}

	// 新建 worktree：-B 重置孤儿分支
	code, _, stderr, _ := runGit(ctx, []string{"worktree", "add", "-B", wtBranch, wtPath, "HEAD"}, absRepo)
	if code != 0 {
		return WorktreeInfo{}, fmt.Errorf("swarm: git worktree add failed: %s", strings.TrimSpace(stderr))
	}
	symlinkCommonDirs(absRepo, wtPath)
	_ = writeWorktreeMeta(wtPath, worktreeMeta{AgentID: agentID, Slug: slug})
	return WorktreeInfo{
		Slug: slug, Path: wtPath, Branch: wtBranch,
		OriginalPath: absRepo, CreatedAt: time.Now(), AgentID: agentID,
	}, nil
}

// Remove 按 slug 移除 worktree。移植自 worktree.py:213 remove_worktree。
func (w *WorktreeManager) Remove(ctx context.Context, slug string) (bool, error) {
	if err := ValidateWorktreeSlug(slug); err != nil {
		return false, err
	}
	wtPath := filepath.Join(w.baseDir, flattenSlug(slug))
	if info, err := os.Stat(wtPath); os.IsNotExist(err) || (err == nil && !info.IsDir()) {
		return false, nil
	}
	removeSymlinks(wtPath)

	// 从 worktree 的 git 元数据探测主仓路径
	code, gitCommon, _, _ := runGit(ctx, []string{"rev-parse", "--git-common-dir"}, wtPath)
	if code == 0 && strings.TrimSpace(gitCommon) != "" {
		repoPath := filepath.Dir(filepath.Clean(gitCommon))
		if info, err := os.Stat(repoPath); err == nil && info.IsDir() {
			code, _, _, _ = runGit(ctx, []string{"worktree", "remove", "--force", wtPath}, repoPath)
			return code == 0, nil
		}
	}
	// 回退：从 base_dir 尝试移除
	code, _, _, _ = runGit(ctx, []string{"worktree", "remove", "--force", wtPath}, w.baseDir)
	return code == 0, nil
}

// List 列出所有受管 worktree。移植自 worktree.py:253 list_worktrees。
func (w *WorktreeManager) List(ctx context.Context) ([]WorktreeInfo, error) {
	entries, err := os.ReadDir(w.baseDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("swarm: read worktree base: %w", err)
	}
	var result []WorktreeInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		child := filepath.Join(w.baseDir, e.Name())
		code, _, _, _ := runGit(ctx, []string{"rev-parse", "--git-dir"}, child)
		if code != 0 {
			continue
		}
		branch := "unknown"
		if rc, out, _, _ := runGit(ctx, []string{"rev-parse", "--abbrev-ref", "HEAD"}, child); rc == 0 {
			branch = strings.TrimSpace(out)
		}
		original := child
		if rc, commonDir, _, _ := runGit(ctx, []string{"rev-parse", "--git-common-dir"}, child); rc == 0 && strings.TrimSpace(commonDir) != "" {
			if abs, err := filepath.Abs(filepath.Dir(filepath.Clean(commonDir))); err == nil {
				original = abs
			}
		}
		info, _ := e.Info()
		meta := readWorktreeMeta(child)
		result = append(result, WorktreeInfo{
			Slug: strings.ReplaceAll(e.Name(), "+", "/"),
			Path: child, Branch: branch, OriginalPath: original,
			CreatedAt: info.ModTime(), AgentID: meta.AgentID,
		})
	}
	return result, nil
}

// CleanupStale 移除无活跃 agent 的 worktree。移植自 worktree.py:295 cleanup_stale。
func (w *WorktreeManager) CleanupStale(ctx context.Context, activeAgentIDs map[string]bool) ([]string, error) {
	wts, err := w.List(ctx)
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, info := range wts {
		if info.AgentID == "" {
			continue
		}
		if activeAgentIDs != nil && activeAgentIDs[info.AgentID] {
			continue
		}
		ok, _ := w.Remove(ctx, info.Slug)
		if ok {
			removed = append(removed, info.Slug)
		}
	}
	return removed, nil
}
