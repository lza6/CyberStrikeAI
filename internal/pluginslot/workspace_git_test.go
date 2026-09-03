package pluginslot_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberstrike-ai/internal/pluginslot"
)

// skipIfNoGit 在系统无 git 时跳过（CI/无 git 环境）。
func skipIfNoGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
}

// makeGitRepo 在 t.TempDir() 建一个带一个 commit 的 git 仓，返回仓路径。
func makeGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGitCmd(t, dir, "init", "-q")
	runGitCmd(t, dir, "config", "user.email", "test@test.com")
	runGitCmd(t, dir, "config", "user.name", "test")
	// 建一个初始 commit（否则 worktree add -b 无 base）。
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, dir, "add", "README.md")
	runGitCmd(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func runGitCmd(t *testing.T, repo string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"-C", repo}, args...)
	cmd := exec.Command("git", fullArgs...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, string(out))
	}
	return strings.TrimSpace(string(out))
}

// TestGitWorkspace_CreateAddsBranch 验证：create 为 worker 建独立 worktree + branch。
func TestGitWorkspace_CreateAddsBranch(t *testing.T) {
	skipIfNoGit(t)
	repo := makeGitRepo(t)
	root := t.TempDir()
	gw := pluginslot.NewGitWorkspace(root, "git")

	cfg := pluginslot.WorkspaceConfig{
		ProjectID: "proj-1", SessionID: "sess-1", Kind: "worker",
		RepoPath: repo, BaseBranch: "master",
	}
	info, err := gw.Create(cfg)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if info.Isolation != pluginslot.IsolationGitWorktree {
		t.Fatalf("Isolation = %q, want git-worktree", info.Isolation)
	}
	if info.Branch != "ao/sess-1" {
		t.Fatalf("Branch = %q, want ao/sess-1", info.Branch)
	}
	// worktree 实际存在且包含 README.md。
	if _, err := os.Stat(filepath.Join(info.Path, "README.md")); err != nil {
		t.Fatalf("worktree missing README.md: %v", err)
	}
	// git worktree list 注册了该路径（git 输出正斜杠，归一化后比较）。
	list := runGitCmd(t, repo, "worktree", "list", "--porcelain")
	normalizedList := strings.ReplaceAll(filepath.ToSlash(list), "\\", "/")
	if !strings.Contains(normalizedList, filepath.ToSlash(info.Path)) {
		t.Fatalf("worktree not registered: %s", list)
	}
}

// TestGitWorkspace_DestroyRemoves 验证：Destroy 移除干净 worktree。
func TestGitWorkspace_DestroyRemoves(t *testing.T) {
	skipIfNoGit(t)
	repo := makeGitRepo(t)
	root := t.TempDir()
	gw := pluginslot.NewGitWorkspace(root, "git")
	cfg := pluginslot.WorkspaceConfig{ProjectID: "p", SessionID: "s", Kind: "worker", RepoPath: repo, BaseBranch: "master"}
	info, err := gw.Create(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := gw.Destroy(info); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, err := os.Stat(info.Path); !os.IsNotExist(err) {
		t.Fatalf("worktree path should be removed, got err=%v", err)
	}
}

// TestGitWorkspace_DestroyDirtyRejected 验证：dirty worktree（未提交改动）拒绝删除。
func TestGitWorkspace_DestroyDirtyRejected(t *testing.T) {
	skipIfNoGit(t)
	repo := makeGitRepo(t)
	root := t.TempDir()
	gw := pluginslot.NewGitWorkspace(root, "git")
	cfg := pluginslot.WorkspaceConfig{ProjectID: "p", SessionID: "s", Kind: "worker", RepoPath: repo, BaseBranch: "master"}
	info, err := gw.Create(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// 在 worktree 里做未提交改动（模拟 agent 工作）。
	if err := os.WriteFile(filepath.Join(info.Path, "agent-work.txt"), []byte("uncommitted"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = gw.Destroy(info)
	if !pluginslot.IsWorkspaceDirty(err) {
		t.Fatalf("Destroy dirty worktree should return ErrWorkspaceDirty, got %v", err)
	}
	// 未提交工作保留。
	if _, err := os.Stat(filepath.Join(info.Path, "agent-work.txt")); err != nil {
		t.Fatalf("dirty work should be preserved, got err=%v", err)
	}
}

// TestGitWorkspace_RestoreReusesExisting 验证：Restore 复用已注册的 worktree + branch。
func TestGitWorkspace_RestoreReusesExisting(t *testing.T) {
	skipIfNoGit(t)
	repo := makeGitRepo(t)
	root := t.TempDir()
	gw := pluginslot.NewGitWorkspace(root, "git")
	cfg := pluginslot.WorkspaceConfig{ProjectID: "p", SessionID: "s", Kind: "worker", RepoPath: repo, BaseBranch: "master"}
	info1, err := gw.Create(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Restore 应复用同 path，不新建 branch。
	info2, err := gw.Restore(cfg)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if info1.Path != info2.Path || info1.Branch != info2.Branch {
		t.Fatalf("Restore drift: %+v vs %+v", info1, info2)
	}
}

// TestGitWorkspace_PathTraversalRejected 验证：sessionID 含 ".." 被 sanitize 且不逃逸 root。
func TestGitWorkspace_PathTraversalRejected(t *testing.T) {
	skipIfNoGit(t)
	repo := makeGitRepo(t)
	root := t.TempDir()
	gw := pluginslot.NewGitWorkspace(root, "git")
	cfg := pluginslot.WorkspaceConfig{ProjectID: "p", SessionID: "../../etc", Kind: "worker", RepoPath: repo, BaseBranch: "master"}
	info, err := gw.Create(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// 路径包含 __（替代 ..）。
	if strings.Contains(filepath.ToSlash(info.Path), "..") {
		t.Fatalf("path still contains ..: %s", info.Path)
	}
	// 路径在 root 内。
	if err := pluginslot.ValidateManagedPathForTest(info.Path, root); err != nil {
		t.Fatalf("path escaped root: %v", err)
	}
}

// TestGitWorkspace_ForceDestroyCleans 验证：ForceDestroy 用 --force 清 dirty worktree。
func TestGitWorkspace_ForceDestroyCleans(t *testing.T) {
	skipIfNoGit(t)
	repo := makeGitRepo(t)
	root := t.TempDir()
	gw := pluginslot.NewGitWorkspace(root, "git")
	cfg := pluginslot.WorkspaceConfig{ProjectID: "p", SessionID: "s", Kind: "worker", RepoPath: repo, BaseBranch: "master"}
	info, err := gw.Create(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(info.Path, "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := gw.ForceDestroy(context.Background(), info); err != nil {
		t.Fatalf("ForceDestroy: %v", err)
	}
	if _, err := os.Stat(info.Path); !os.IsNotExist(err) {
		t.Fatalf("ForceDestroy should remove path, got err=%v", err)
	}
}

// TestGitWorkspace_GitUnavailable 验证：git 不可用时 Create 返回 ErrGitUnavailable。
func TestGitWorkspace_GitUnavailable(t *testing.T) {
	root := t.TempDir()
	gw := pluginslot.NewGitWorkspace(root, "/nonexistent/git")
	cfg := pluginslot.WorkspaceConfig{ProjectID: "p", SessionID: "s", Kind: "worker", RepoPath: root}
	_, err := gw.Create(cfg)
	if err == nil {
		t.Fatal("Create with bad git bin should fail")
	}
}

// TestGitWorkspace_ParsePorcelain 验证 worktree list --porcelain 解析。
func TestGitWorkspace_ParsePorcelain(t *testing.T) {
	out := `worktree C:/repo/main
HEAD b2440373c18c3e2f2a3dbb47411cba5294fa0ef6
branch refs/heads/master

worktree C:/repo/wt
HEAD b2440373c18c3e2f2a3dbb47411cba5294fa0ef6
branch refs/heads/test-branch

`
	records := pluginslot.ParseWorktreePorcelainForTest(out)
	if len(records) != 2 {
		t.Fatalf("parsed %d records, want 2", len(records))
	}
	if records[0].Path != "C:/repo/main" || records[0].Branch != "master" {
		t.Fatalf("record[0] = %+v", records[0])
	}
	if records[1].Path != "C:/repo/wt" || records[1].Branch != "test-branch" {
		t.Fatalf("record[1] = %+v", records[1])
	}
}

// TestGitWorkspace_CreateCtxTimeout 验证 context 超时取消 git 命令。
func TestGitWorkspace_CreateCtxTimeout(t *testing.T) {
	skipIfNoGit(t)
	repo := makeGitRepo(t)
	root := t.TempDir()
	gw := pluginslot.NewGitWorkspace(root, "git")
	cfg := pluginslot.WorkspaceConfig{ProjectID: "p", SessionID: "s", Kind: "worker", RepoPath: repo, BaseBranch: "master"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := gw.CreateCtx(ctx, cfg)
	if err != nil {
		t.Fatalf("CreateCtx: %v", err)
	}
	_ = info
}

// 静态断言 GitWorkspace 实现 Workspace 接口。
var _ pluginslot.Workspace = (*pluginslot.GitWorkspace)(nil)
var _ pluginslot.Workspace = (*pluginslot.DirectoryWorkspace)(nil)

// TestGitWorkspace_BranchCheckedOutElsewhere 验证：branch 已被其他 worktree 占用时，
// 第二个 worker Create 同 branch 返回 ErrBranchCheckedOutElsewhere（审计发现 5 修复验证）。
func TestGitWorkspace_BranchCheckedOutElsewhere(t *testing.T) {
	skipIfNoGit(t)
	repo := makeGitRepo(t)
	root := t.TempDir()
	gw := pluginslot.NewGitWorkspace(root, "git")
	base := pluginslot.WorkspaceConfig{ProjectID: "p", SessionID: "s1", Kind: "worker", RepoPath: repo, BaseBranch: "master"}
	info1, err := gw.Create(base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = gw.ForceDestroy(context.Background(), info1)
	})
	// 同 branch（info1.Branch）不同 sessionID → 应拒绝。
	dup := base
	dup.SessionID = "other-session"
	dup.Branch = info1.Branch
	_, err = gw.Create(dup)
	if err == nil {
		t.Fatal("second worker same branch should be rejected")
	}
	if !strings.Contains(err.Error(), "branch checked out elsewhere") {
		t.Fatalf("err = %v, want branch checked out elsewhere", err)
	}
}

// TestGitWorkspace_DestroyWithEmptyManagedRoot 验证：managedRoot 为空时 Destroy/ForceDestroy
// 仍强制 fallback 根校验（审计发现 1 修复验证——逃逸路径被拒）。
func TestGitWorkspace_DestroyWithEmptyManagedRoot(t *testing.T) {
	skipIfNoGit(t)
	repo := makeGitRepo(t)
	gw := pluginslot.NewGitWorkspace("", "git") // managedRoot 空 → fallback
	escaped := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(escaped, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(escaped) })
	err := gw.ForceDestroy(context.Background(), pluginslot.WorkspaceInfo{Path: escaped, RepoPath: repo})
	if err == nil {
		t.Fatal("ForceDestroy escaped path should be rejected even with empty managedRoot")
	}
}
