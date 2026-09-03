package pluginslot

// Workspace 是 SlotWorkspace 的具体接口（扩展 pluginslot 槽位预留）。
//
// 迁移自参考项目 agent-orchestrator/backend 的 ports.Workspace 适配器契约
// （adapters/workspace/gitworktree + scratch）。每个 worker 拿到独立工作区
// （目录或 git worktree），Destroy 时拒绝误删未提交工作（dirty 检测）。
//
// 设计：小 interface（Create/Restore/Destroy + Isolation mode），实现方按需扩展
// StashUncommitted/ApplyPreserved/ForceDestroy（用类型断言按需调用）。
type Workspace interface {
	// Create 为一个 worker 建独立工作区，返回路径/分支/基线信息。
	Create(config WorkspaceConfig) (WorkspaceInfo, error)
	// Restore 容忍已存在的 worktree（复用注册的 branch 而非强制 cfg.Branch）。
	Restore(config WorkspaceConfig) (WorkspaceInfo, error)
	// Destroy 销毁工作区；dirty 工作区（有未提交工作）返回 ErrWorkspaceDirty。
	Destroy(info WorkspaceInfo) error
}

// WorkspaceConfig 建工作区的配置。移植自参考项目 ports.WorkspaceConfig。
type WorkspaceConfig struct {
	// ProjectID 所属项目（决定 managedRoot 子目录）。
	ProjectID string
	// SessionID worker session 唯一 ID（决定 worktree 路径 + branch 名后缀）。
	SessionID string
	// Kind "worker" / "orchestrator"（orchestrator 用 {prefix}-orchestrator 路径）。
	Kind string
	// Branch 期望的 branch 名（git-worktree 模式用）。空=自动生成 "ao/{sessionID}"。
	Branch string
	// BaseBranch / BaseRef worktree 的基线（git-worktree 模式用）。
	// BaseRef 优先于 BaseBranch（支持 PR head / commit SHA）。
	BaseBranch string
	BaseRef    string
	// RepoPath 源 git 仓路径（git-worktree 模式必填）。
	RepoPath string
	// ManagedRoot 工作区根目录（所有 worker 工作区落在此下，validateManagedPath 强制）。
	ManagedRoot string
}

// WorkspaceInfo Create/Restore 的返回。移植自参考项目 ports.WorkspaceInfo。
type WorkspaceInfo struct {
	Path      string // 工作区绝对路径
	Branch    string // 实际使用的 branch（git-worktree 模式）
	BaseRef   string // 基线 ref（git-worktree 模式）
	SessionID string
	ProjectID string
	RepoPath  string             // 源 git 仓路径（git-worktree 模式；Destroy 需要）
	Isolation WorkspaceIsolation // 实际使用的隔离模式
}

// WorkspaceIsolation 隔离模式枚举。
type WorkspaceIsolation string

const (
	// IsolationDirectory 纯目录隔离（零 git，与现有 project/workspace.go 一致）。
	IsolationDirectory WorkspaceIsolation = "directory"
	// IsolationGitWorktree git worktree + 独立 branch（os/exec git，零新依赖）。
	IsolationGitWorktree WorkspaceIsolation = "git-worktree"
)

// WorkspaceError 错误类型，供 Destroy 分类。
var (
	// ErrWorkspaceDirty 工作区有未提交工作，拒绝误删。
	ErrWorkspaceDirty = workspaceError{"workspace is dirty (uncommitted work)"}
	// ErrWorkspaceNotFound 工作区不存在（已 Destroy 或从未 Create）。
	ErrWorkspaceNotFound = workspaceError{"workspace not found"}
	// ErrBranchCheckedOutElsewhere branch 已被其他 worktree check out。
	ErrBranchCheckedOutElsewhere = workspaceError{"branch checked out elsewhere"}
	// ErrGitUnavailable 系统未装 git（git-worktree 模式）。
	ErrGitUnavailable = workspaceError{"git binary not available"}
)

type workspaceError struct{ msg string }

func (e workspaceError) Error() string { return e.msg }

// IsWorkspaceDirty 报告 err 是否 ErrWorkspaceDirty（供调用方分类处理）。
func IsWorkspaceDirty(err error) bool {
	return err == ErrWorkspaceDirty || (err != nil && err.Error() == ErrWorkspaceDirty.Error())
}

// ValidateManagedPathForTest 暴露 validateManagedPath 供包外测试用（白盒）。
func ValidateManagedPathForTest(path, root string) error {
	return validateManagedPath(path, root)
}

// GitAvailableForTest 暴露 gitAvailable 供包外测试用（白盒）。
func GitAvailableForTest() bool { return gitAvailable() }

// ParseWorktreePorcelainForTest 暴露 parseWorktreePorcelain 供包外测试用（白盒）。
func ParseWorktreePorcelainForTest(out string) []WorktreeRecordForTest {
	recs := parseWorktreePorcelain(out)
	res := make([]WorktreeRecordForTest, len(recs))
	for i, r := range recs {
		res[i] = WorktreeRecordForTest{Path: r.Path, Branch: r.Branch, Head: r.Head, Bare: r.Bare, Locked: r.Locked}
	}
	return res
}

// WorktreeRecordForTest 暴露 worktreeRecord 供包外测试（白盒）。
type WorktreeRecordForTest struct {
	Path   string
	Branch string
	Head   string
	Bare   bool
	Locked bool
}
