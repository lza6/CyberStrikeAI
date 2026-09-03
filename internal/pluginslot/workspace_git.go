package pluginslot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// GitWorkspace 是 SlotWorkspace 的 git worktree 隔离实现。
//
// 迁移自参考项目 agent-orchestrator/backend 的 internal/adapters/workspace/gitworktree。
// 核心设计（与参考项目一致）：
//   - 每 worker 一个独立 git worktree + 独立 branch（{prefix}/{sessionID}）。
//   - 全部走 os/exec git CLI（无 go-git 依赖）；GIT_TERMINAL_PROMPT=0 避免交互卡死。
//   - validateManagedPath 强制 worktree 路径在 managedRoot 内（防 traversal）。
//   - validatePathComponent 拒绝 ".."/路径分隔符。
//   - Destroy 走 git worktree remove（不带 --force），dirty worktree 拒绝（ErrWorkspaceDirty）
//     保护未提交 agent 工作；ForceDestroy（类型断言）才 --force + RemoveAll 兜底。
//   - worktree add --force 用于"路径不存在但注册还在"的恢复（single --force，不传两次）。
//
// 与参考项目的差异（Go 重写，适配 CyberStrikeAI）：
//   - 参考项目用 context.Context 贯穿；本实现接受 context（供取消/超时）。
//   - 参考项目 git 参数构造在 commands.go 独立文件；本实现内联（单文件，便于维护）。
//   - 参考项目支持多 repo workspace project；本实现只做单 repo worktree（CyberStrikeAI
//     渗透场景不需要多 repo stack，YAGNI）。
//
// 零新增 go.mod 依赖：仅标准库 os/exec + path/filepath + context。
type GitWorkspace struct {
	managedRoot string
	gitBin      string // git 二进制路径（测试可注入；空=exec.LookPath("git")）
}

// NewGitWorkspace 构造。managedRoot 是所有 worker worktree 根目录。
// gitBin 为空时用 exec.LookPath("git") 探测；探测不到 Create 返回 ErrGitUnavailable。
func NewGitWorkspace(managedRoot, gitBin string) *GitWorkspace {
	return &GitWorkspace{managedRoot: strings.TrimSpace(managedRoot), gitBin: strings.TrimSpace(gitBin)}
}

// gitBinary 返回 git 路径或错误。
func (w *GitWorkspace) gitBinary() (string, error) {
	if w == nil {
		return "", errors.New("git workspace: nil receiver")
	}
	if w.gitBin != "" {
		return w.gitBin, nil
	}
	bin, err := exec.LookPath("git")
	if err != nil {
		return "", ErrGitUnavailable
	}
	return bin, nil
}

// Create 实现 Workspace。为一个 worker 建独立 git worktree + branch。
func (w *GitWorkspace) Create(cfg WorkspaceConfig) (WorkspaceInfo, error) {
	ctx := context.Background()
	return w.CreateCtx(ctx, cfg)
}

// CreateCtx 带 context 的 Create（供取消/超时）。
func (w *GitWorkspace) CreateCtx(ctx context.Context, cfg WorkspaceConfig) (WorkspaceInfo, error) {
	if w == nil {
		return WorkspaceInfo{}, errors.New("git workspace: nil receiver")
	}
	if err := validateGitConfig(cfg); err != nil {
		return WorkspaceInfo{}, err
	}
	bin, err := w.gitBinary()
	if err != nil {
		return WorkspaceInfo{}, err
	}
	repo := strings.TrimSpace(cfg.RepoPath)
	if err := w.validateBranch(ctx, bin, repo, cfg.Branch); err != nil {
		return WorkspaceInfo{}, err
	}
	path, err := w.managedPath(cfg)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	// 复用已存在的 worktree（Restore 语义）。
	if info, ok, err := w.existingWorktree(ctx, bin, repo, path, cfg); err != nil {
		return WorkspaceInfo{}, err
	} else if ok {
		return info, nil
	}
	branch := cfg.Branch
	if branch == "" {
		branch = defaultSessionBranchName(cfg.SessionID)
	}
	baseRef, err := w.addWorktree(ctx, bin, repo, path, branch, cfg.BaseBranch, cfg.BaseRef)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	return WorkspaceInfo{
		Path:      path,
		Branch:    branch,
		BaseRef:   baseRef,
		SessionID: cfg.SessionID,
		ProjectID: cfg.ProjectID,
		RepoPath:  cfg.RepoPath,
		Isolation: IsolationGitWorktree,
	}, nil
}

// Restore 实现 Workspace。容忍已存在的 worktree（复用注册的 branch）。
func (w *GitWorkspace) Restore(cfg WorkspaceConfig) (WorkspaceInfo, error) {
	return w.Create(cfg)
}

// Destroy 实现 Workspace。git worktree remove（不带 --force）→ dirty 拒绝。
func (w *GitWorkspace) Destroy(info WorkspaceInfo) error {
	ctx := context.Background()
	return w.DestroyCtx(ctx, info)
}

// DestroyCtx 带 context 的 Destroy。
func (w *GitWorkspace) DestroyCtx(ctx context.Context, info WorkspaceInfo) error {
	if w == nil {
		return errors.New("git workspace: nil receiver")
	}
	bin, err := w.gitBinary()
	if err != nil {
		return err
	}
	path := strings.TrimSpace(info.Path)
	if path == "" {
		return ErrWorkspaceNotFound
	}
	repo := strings.TrimSpace(info.RepoPath)

	// 验证路径在 managedRoot 内。managedRoot 为空时用 fallback 根
	// （tmp/workspace/workers，与 managedPath 默认一致）仍强制校验——
	// 防 factory 构造（cfg 无 managed_root）后 Destroy/ForceDestroy 对
	// 调用方传入的任意 info.Path 执行 Remove/RemoveAll（审计 C4 防御纵深）。
	root := w.managedRoot
	if root == "" {
		root = filepath.Join("tmp", "workspace", "workers")
	}
	if err := validateManagedPath(path, root); err != nil {
		return err
	}

	// 无 repo 信息时无法定位 worktree 注册，回退到路径判定（空目录移除，否则 dirty）。
	if repo == "" {
		_, _ = w.runGit(ctx, bin, "", "worktree", "prune")
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
			return ErrWorkspaceNotFound
		}
		entries, _ := os.ReadDir(path)
		if len(entries) > 0 {
			return ErrWorkspaceDirty
		}
		return os.Remove(path)
	}

	// git worktree remove（不带 --force）：dirty worktree 会失败 → 转 ErrWorkspaceDirty。
	if out, err := w.runGit(ctx, bin, repo, "worktree", "remove", path); err != nil {
		if isDirtyWorktreeRemovalError(out, err) {
			return ErrWorkspaceDirty
		}
		// worktree 未注册（已 remove 或从未 add）→ 尝试 prune 后再检查目录。
		_, _ = w.runGit(ctx, bin, repo, "worktree", "prune")
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
			return ErrWorkspaceNotFound
		}
		// 目录还在但 worktree 未注册 → 若为空则移除，否则保留（dirty）。
		entries, _ := os.ReadDir(path)
		if len(entries) > 0 {
			return ErrWorkspaceDirty
		}
		return os.Remove(path)
	}
	// 成功 remove 后 prune 残留注册。
	_, _ = w.runGit(ctx, bin, repo, "worktree", "prune")
	return nil
}

// ForceDestroy 用 --force + RemoveAll 兜底（仅在已捕获未提交工作后才调用）。
// 移植自参考项目 workspace.go:426 ForceDestroy。
func (w *GitWorkspace) ForceDestroy(ctx context.Context, info WorkspaceInfo) error {
	bin, err := w.gitBinary()
	if err != nil {
		return err
	}
	path := strings.TrimSpace(info.Path)
	if path == "" {
		return ErrWorkspaceNotFound
	}
	// 与 DestroyCtx 同样的防御纵深：managedRoot 为空时用 fallback 根仍强制校验。
	root := w.managedRoot
	if root == "" {
		root = filepath.Join("tmp", "workspace", "workers")
	}
	if err := validateManagedPath(path, root); err != nil {
		return err
	}
	_, _ = w.runGit(ctx, bin, info.RepoPath, "worktree", "remove", "--force", path)
	_, _ = w.runGit(ctx, bin, info.RepoPath, "worktree", "prune")
	_ = os.RemoveAll(path)
	return nil
}

// managedPath 计算并验证 worktree 路径：{managedRoot}/{projectID}/{sessionID}。
// 返回 EvalSymlinks 展开后的长名路径（避免 Windows 8.3 短名与 git 输出长名不匹配）。
func (w *GitWorkspace) managedPath(cfg WorkspaceConfig) (string, error) {
	root := strings.TrimSpace(w.managedRoot)
	if root == "" {
		root = filepath.Join("tmp", "workspace", "workers")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("git workspace: abs root: %w", err)
	}
	// 展开 8.3 短名（t.TempDir 场景），与 git 输出长名对齐。
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", fmt.Errorf("git workspace: mkdir root: %w", err)
	}
	pid := validatePathComponent(cfg.ProjectID, "default")
	sid := validatePathComponent(cfg.SessionID, "default")
	if cfg.Kind == "orchestrator" {
		sid = sid + "-orchestrator"
	}
	return filepath.Join(abs, pid, sid), nil
}

// addWorktree 执行 git worktree add。移植自参考项目 commands.go worktreeAdd*Args。
//
// 若本地 refs/heads/{branch} 存在 → worktree add {path} {branch}（复用 branch）；
// 否则 worktree add -b {branch} {path} {baseRef}（新建 branch from base）。
// --force 用于恢复"path 不存在但注册还在"的 stale 注册（single --force，不传两次）。
func (w *GitWorkspace) addWorktree(ctx context.Context, bin, repo, path, branch, baseBranch, baseRef string) (string, error) {
	// 决定 baseRef：优先 cfg.BaseRef，其次 origin/{baseBranch}，否则默认 branch。
	ref := baseRef
	if ref == "" {
		if baseBranch != "" {
			// 探测 origin/{baseBranch} 是否存在。
			if out, err := w.runGit(ctx, bin, repo, "rev-parse", "--verify", "--quiet", "origin/"+baseBranch); err == nil && strings.TrimSpace(out) != "" {
				ref = "origin/" + baseBranch
			}
		}
		if ref == "" {
			// 回退：用默认 branch（git rev-parse HEAD 或 main/master）。
			ref = "HEAD"
		}
	}
	// 若本地 branch 已存在 → 复用前检查是否被其他 worktree 占用（审计发现 5：
	// 无检查时 --force 允许同 branch 双 worktree，两 worker 在同 branch 工作互相污染）。
	if _, err := w.runGit(ctx, bin, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		branchOwner, qerr := w.findWorktreeByBranch(ctx, bin, repo, branch)
		if qerr == nil && branchOwner != "" && !pathEqual(branchOwner, path) {
			return "", fmt.Errorf("git workspace: %w (branch %s checked out at %s)", ErrBranchCheckedOutElsewhere, branch, branchOwner)
		}
		// branch 空闲（未被占用或已被本 path 占用）：worktree add（带 --force 容忍 stale 注册）。
		if _, err := w.runGit(ctx, bin, repo, "worktree", "add", "--force", path, branch); err != nil {
			return "", fmt.Errorf("git workspace: worktree add existing branch: %w", err)
		}
		return ref, nil
	}
	// branch 不存在：worktree add -b {branch} {path} {baseRef}。
	if _, err := w.runGit(ctx, bin, repo, "worktree", "add", "-b", branch, path, ref); err != nil {
		// 恢复 stale 注册：带 --force 重试（single --force）。
		if _, err2 := w.runGit(ctx, bin, repo, "worktree", "add", "--force", "-b", branch, path, ref); err2 != nil {
			return "", fmt.Errorf("git workspace: worktree add -b: %w (retry: %v)", err, err2)
		}
	}
	return ref, nil
}

// findWorktreeByBranch 在 worktree 注册表中查找 check out 了 branch 的 worktree 路径。
// 未找到返回空串。移植自参考项目 findWorktreeByBranch（拒绝同 branch 双 worktree）。
func (w *GitWorkspace) findWorktreeByBranch(ctx context.Context, bin, repo, branch string) (string, error) {
	out, err := w.runGit(ctx, bin, repo, "worktree", "list", "--porcelain")
	if err != nil {
		return "", err
	}
	for _, r := range parseWorktreePorcelain(out) {
		if r.Branch == branch {
			return r.Path, nil
		}
	}
	return "", nil
}

// existingWorktree 检查 path 是否已是注册的 worktree。移植自参考项目 workspace.go existingWorktree。
// 若已注册且目录在 → 返回 info, true, nil；若注册在但目录丢 → 走 addWorktree --force 重注册。
func (w *GitWorkspace) existingWorktree(ctx context.Context, bin, repo, path string, cfg WorkspaceConfig) (WorkspaceInfo, bool, error) {
	out, err := w.runGit(ctx, bin, repo, "worktree", "list", "--porcelain")
	if err != nil {
		return WorkspaceInfo{}, false, nil // 查询失败不阻断，让后续 addWorktree 尝试
	}
	records := parseWorktreePorcelain(out)
	for _, r := range records {
		if pathEqual(r.Path, path) {
			return WorkspaceInfo{
				Path: path, Branch: r.Branch, BaseRef: "", SessionID: cfg.SessionID, ProjectID: cfg.ProjectID, RepoPath: cfg.RepoPath, Isolation: IsolationGitWorktree,
			}, true, nil
		}
	}
	return WorkspaceInfo{}, false, nil
}

// validateBranch 用 git check-ref-format --branch 校验 branch 名合法。
// 移植自参考项目 commands.go checkRefFormatBranchArgs。
func (w *GitWorkspace) validateBranch(ctx context.Context, bin, repo, branch string) error {
	if branch == "" {
		return nil // 空分支名合法（后续自动生成）
	}
	if _, err := w.runGit(ctx, bin, repo, "check-ref-format", "--branch", branch); err != nil {
		return fmt.Errorf("git workspace: invalid branch name %q: %w", branch, err)
	}
	return nil
}

// runGit 执行 git 子命令，返回（stdout, error）。
// Windows 关键：git 输出正斜杠路径（C:/...），filepath 是反斜杠（C:\...），
// 故 stdout 内路径需 normPath 归一化（供路径匹配）。错误信息在 stderr，
// 故 err 时把 stderr 拼进 stdout 一并返回（供 isDirtyWorktreeRemovalError 读取）。
func (w *GitWorkspace) runGit(ctx context.Context, bin, repo string, args ...string) (string, error) {
	fullArgs := append([]string{"-C", repo}, args...)
	cmd := exec.CommandContext(ctx, bin, fullArgs...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=Never")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// 错误信息在 stderr，返回 stdout+stderr 供上层分类（dirty/未注册）。
		return stdout.String() + stderr.String(), fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// normPath 归一化 git 输出路径为 filepath 可比较的绝对路径（正斜杠→反斜杠 + Clean）。
// Windows 关键：t.TempDir()/managedPath 可能返回 8.3 短名（ADMINI~1.DES），而 git 输出长名
// （Administrator.DESKTOP-EGNE9ND），故用 EvalSymlinks 展开两侧再比较。
func normPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = strings.ReplaceAll(p, "/", string(filepath.Separator))
	p = filepath.Clean(p)
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	return p
}

// pathEqual 报告两个路径是否指向同一位置（归一化后比较，Windows 大小写不敏感）。
func pathEqual(a, b string) bool {
	a, b = normPath(a), normPath(b)
	if a == "" || b == "" {
		return false
	}
	return strings.EqualFold(a, b)
}

// validateGitConfig 校验 git-worktree 模式必填字段。
func validateGitConfig(cfg WorkspaceConfig) error {
	if strings.TrimSpace(cfg.SessionID) == "" {
		return errors.New("git workspace: sessionID is empty")
	}
	if strings.TrimSpace(cfg.RepoPath) == "" {
		return errors.New("git workspace: repoPath is empty (required for git-worktree mode)")
	}
	return nil
}

// defaultSessionBranchName 默认 branch 名。移植自参考项目 workspace.go:1563。
func defaultSessionBranchName(sessionID string) string {
	return "ao/" + validatePathComponent(sessionID, "default")
}

// isDirtyWorktreeRemovalError 报告 git worktree remove 的失败是否因 dirty worktree。
// git 输出 "fatal: cannot remove ...: ...contains modified files" 或 "contains untracked files"。
func isDirtyWorktreeRemovalError(out string, err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(out)
	return strings.Contains(lower, "modified files") ||
		strings.Contains(lower, "untracked files") ||
		strings.Contains(lower, "is dirty") ||
		strings.Contains(lower, "contains uncommitted")
}

// worktreeRecord 是 git worktree list --porcelain 的一条记录。
type worktreeRecord struct {
	Path   string
	Branch string
	Head   string
	Bare   bool
	Locked bool
}

// parseWorktreePorcelain 解析 git worktree list --porcelain 输出。
// 移植自参考项目 gitworktree/parse.go parseWorktreePorcelain。
func parseWorktreePorcelain(out string) []worktreeRecord {
	var records []worktreeRecord
	var cur *worktreeRecord
	flush := func() {
		if cur != nil && cur.Path != "" {
			records = append(records, *cur)
		}
		cur = nil
	}
	lines := strings.Split(out, "\n")
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			flush()
			continue
		}
		key, val, hasValue := strings.Cut(line, " ")
		switch key {
		case "worktree":
			flush()
			cur = &worktreeRecord{}
			if hasValue {
				cur.Path = val
			}
		case "HEAD":
			if cur != nil && hasValue {
				cur.Head = val
			}
		case "branch":
			if cur != nil && hasValue {
				cur.Branch = strings.TrimPrefix(val, "refs/heads/")
			}
		case "bare":
			if cur != nil {
				cur.Bare = true
			}
		case "locked":
			if cur != nil {
				cur.Locked = true
			}
		}
	}
	flush()
	return records
}
