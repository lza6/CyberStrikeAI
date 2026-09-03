package pluginslot_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberstrike-ai/internal/pluginslot"
)

func TestDirectoryWorkspace_CreateAndDestroy(t *testing.T) {
	root := t.TempDir()
	dw := pluginslot.NewDirectoryWorkspace(root)
	cfg := pluginslot.WorkspaceConfig{
		ProjectID: "proj-1",
		SessionID: "sess-1",
		Kind:      "worker",
	}
	info, err := dw.Create(cfg)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if info.Isolation != pluginslot.IsolationDirectory {
		t.Fatalf("Isolation = %q, want directory", info.Isolation)
	}
	if !strings.HasSuffix(filepath.ToSlash(info.Path), "proj-1/sess-1") {
		t.Fatalf("Path = %q, want suffix proj-1/sess-1", info.Path)
	}
	if _, err := os.Stat(info.Path); err != nil {
		t.Fatalf("created dir not exists: %v", err)
	}
	// 空目录可直接 Destroy。
	if err := dw.Destroy(info); err != nil {
		t.Fatalf("Destroy empty: %v", err)
	}
	if _, err := os.Stat(info.Path); !os.IsNotExist(err) {
		t.Fatalf("dir should be removed, got err=%v", err)
	}
}

func TestDirectoryWorkspace_DestroyDirty(t *testing.T) {
	root := t.TempDir()
	dw := pluginslot.NewDirectoryWorkspace(root)
	cfg := pluginslot.WorkspaceConfig{ProjectID: "p", SessionID: "s"}
	info, err := dw.Create(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// 放一个文件（模拟未提交工作）。
	if err := os.WriteFile(filepath.Join(info.Path, "work.txt"), []byte("agent work"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = dw.Destroy(info)
	if !pluginslot.IsWorkspaceDirty(err) {
		t.Fatalf("Destroy dirty should return ErrWorkspaceDirty, got %v", err)
	}
	// 文件保留（未删）。
	if _, err := os.Stat(filepath.Join(info.Path, "work.txt")); err != nil {
		t.Fatalf("dirty work should be preserved, got err=%v", err)
	}
}

func TestDirectoryWorkspace_RestoreIdempotent(t *testing.T) {
	root := t.TempDir()
	dw := pluginslot.NewDirectoryWorkspace(root)
	cfg := pluginslot.WorkspaceConfig{ProjectID: "p", SessionID: "s"}
	info1, err := dw.Create(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Restore 容忍已存在。
	info2, err := dw.Restore(cfg)
	if err != nil {
		t.Fatalf("Restore existing: %v", err)
	}
	if info1.Path != info2.Path {
		t.Fatalf("Restore path drift: %q vs %q", info1.Path, info2.Path)
	}
}

func TestDirectoryWorkspace_PathTraversalRejected(t *testing.T) {
	root := t.TempDir()
	dw := pluginslot.NewDirectoryWorkspace(root)
	// ".." 在 sessionID 中 → validatePathComponent 把 .. 替换为 __。
	cfg := pluginslot.WorkspaceConfig{ProjectID: "p", SessionID: "../../etc"}
	info, err := dw.Create(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// 路径不应逃逸 root。
	if err := pluginslot.ValidateManagedPathForTest(info.Path, root); err != nil {
		t.Fatalf("path escaped root: %v (path=%s)", err, info.Path)
	}
	// 含 __ 而非 ..。
	if strings.Contains(filepath.ToSlash(info.Path), "..") {
		t.Fatalf("path still contains .. after sanitize: %s", info.Path)
	}
}

func TestDirectoryWorkspace_DestroyEscapedPathRejected(t *testing.T) {
	root := t.TempDir()
	dw := pluginslot.NewDirectoryWorkspace(root)
	// 手工构造一个逃逸路径的 info（模拟恶意调用方）。
	escaped := filepath.Join(root, "..", "outside-target")
	_ = os.MkdirAll(escaped, 0o755)
	t.Cleanup(func() { _ = os.RemoveAll(escaped) })
	err := dw.Destroy(pluginslot.WorkspaceInfo{Path: escaped, ProjectID: "p", SessionID: "s"})
	if err == nil {
		t.Fatal("Destroy escaped path should be rejected")
	}
}

func TestDirectoryWorkspace_OrchestratorSuffix(t *testing.T) {
	root := t.TempDir()
	dw := pluginslot.NewDirectoryWorkspace(root)
	cfg := pluginslot.WorkspaceConfig{ProjectID: "p", SessionID: "s", Kind: "orchestrator"}
	info, err := dw.Create(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(filepath.ToSlash(info.Path), "s-orchestrator") {
		t.Fatalf("orchestrator path should have -orchestrator suffix, got %s", info.Path)
	}
}

func TestDirectoryWorkspace_DestroyNotFound(t *testing.T) {
	root := t.TempDir()
	dw := pluginslot.NewDirectoryWorkspace(root)
	// 不存在的路径。
	missing := filepath.Join(root, "nope")
	err := dw.Destroy(pluginslot.WorkspaceInfo{Path: missing, ProjectID: "p", SessionID: "s"})
	if !errors.Is(err, pluginslot.ErrWorkspaceNotFound) {
		t.Fatalf("Destroy missing should return ErrWorkspaceNotFound, got %v", err)
	}
}

func TestGitAvailableIsBoolean(t *testing.T) {
	// 不断言具体值（CI 可能有/无 git），只断言不 panic 且返回 bool。
	_ = pluginslot.GitAvailableForTest()
}

// TestSlotWorkspaceFactoriesRegistered 验证 init() 已注册 directory + git-worktree 两个 Factory。
func TestSlotWorkspaceFactoriesRegistered(t *testing.T) {
	// 同包 registry_test 的 Reset() 可能清掉 init 注册；幂等恢复。
	pluginslot.RegisterWorkspaceFactories()
	mans := pluginslot.List(pluginslot.SlotWorkspace)
	names := make(map[string]bool, len(mans))
	for _, m := range mans {
		names[m.Name] = true
	}
	if !names["directory"] {
		t.Fatalf("directory factory not registered: %v", names)
	}
	if !names["git-worktree"] {
		t.Fatalf("git-worktree factory not registered: %v", names)
	}
}

// TestSlotWorkspaceGetDirectory 验证 Get(SlotWorkspace,"directory") 返回可用 Workspace。
func TestSlotWorkspaceGetDirectory(t *testing.T) {
	pluginslot.RegisterWorkspaceFactories() // 幂等恢复（同包 Reset 影响）
	root := t.TempDir()
	inst := pluginslot.Get(pluginslot.SlotWorkspace, "directory", map[string]interface{}{"managed_root": root})
	if inst == nil {
		t.Fatal("Get(directory) returned nil")
	}
	ws, ok := inst.(pluginslot.Workspace)
	if !ok {
		t.Fatalf("Get(directory) = %T, want pluginslot.Workspace", inst)
	}
	info, err := ws.Create(pluginslot.WorkspaceConfig{ProjectID: "p", SessionID: "s"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if info.Isolation != pluginslot.IsolationDirectory {
		t.Fatalf("Isolation = %q, want directory", info.Isolation)
	}
}

// TestSlotWorkspaceGetGitWorktree 验证 Get(SlotWorkspace,"git-worktree")（git 可用时真实建 worktree）。
func TestSlotWorkspaceGetGitWorktree(t *testing.T) {
	skipIfNoGit(t)
	pluginslot.RegisterWorkspaceFactories() // 幂等恢复（同包 Reset 影响）
	root := t.TempDir()
	repo := makeGitRepo(t)
	inst := pluginslot.Get(pluginslot.SlotWorkspace, "git-worktree", map[string]interface{}{"managed_root": root})
	if inst == nil {
		t.Fatal("Get(git-worktree) returned nil")
	}
	ws, ok := inst.(pluginslot.Workspace)
	if !ok {
		t.Fatalf("Get(git-worktree) = %T, want pluginslot.Workspace", inst)
	}
	info, err := ws.Create(pluginslot.WorkspaceConfig{
		ProjectID: "p", SessionID: "s", Kind: "worker",
		RepoPath: repo, BaseBranch: "master",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if info.Isolation != pluginslot.IsolationGitWorktree {
		t.Fatalf("Isolation = %q, want git-worktree", info.Isolation)
	}
	if info.Branch != "ao/s" {
		t.Fatalf("Branch = %q, want ao/s", info.Branch)
	}
	// 清理。
	if err := ws.Destroy(info); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
}

// TestSlotWorkspaceDetectAvailable 验证 DetectAvailable(SlotWorkspace) 至少含 directory；
// git-worktree 视环境而定。
func TestSlotWorkspaceDetectAvailable(t *testing.T) {
	pluginslot.RegisterWorkspaceFactories() // 幂等恢复（同包 Reset 影响）
	avail := pluginslot.DetectAvailable(pluginslot.SlotWorkspace)
	found := false
	for _, name := range avail {
		if name == "directory" {
			found = true
		}
	}
	if !found {
		t.Fatalf("DetectAvailable should contain directory, got %v", avail)
	}
}
