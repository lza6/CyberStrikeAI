package swarm

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateWorktreeSlug 验证 slug 校验规则。
func TestValidateWorktreeSlug(t *testing.T) {
	tests := []struct {
		slug string
		ok   bool
	}{
		{"feature-x", true},
		{"feature/x", true},
		{"a.b_c-d", true},
		{"", false},
		{strings.Repeat("a", 65), false},
		{"/abs", false},
		{"/backslash", false},
		{"./dot", false},
		{"../up", false},
		{"seg//empty", false},
		{"bad space", false},
	}
	for _, tt := range tests {
		err := ValidateWorktreeSlug(tt.slug)
		if (err == nil) != tt.ok {
			t.Errorf("ValidateWorktreeSlug(%q) ok=%v, err=%v", tt.slug, tt.ok, err)
		}
	}
}

// TestFlattenSlug 验证 / → +。
func TestFlattenSlug(t *testing.T) {
	if flattenSlug("a/b/c") != "a+b+c" {
		t.Errorf("flattenSlug failed")
	}
	if worktreeBranch("a/b") != "worktree-a+b" {
		t.Errorf("worktreeBranch failed")
	}
}

// TestWorktreeManagerCreateRemove 验证真实 git worktree 创建/移除（需 git 二进制）。
func TestWorktreeManagerCreateRemove(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	wm, err := NewWorktreeManager(home)
	if err != nil {
		t.Fatalf("NewWorktreeManager: %v", err)
	}
	// 准备一个真实 git repo
	repo := t.TempDir()
	ctx := context.Background()
	if code, _, _, e := runGit(ctx, []string{"init"}, repo); code != 0 || e != nil {
		t.Skipf("git init failed: %v", e)
	}
	// 写一个 commit（git worktree add HEAD 需要至少一个 commit）
	commitFile := filepath.Join(repo, "README.md")
	if err := writeFile(commitFile, "init\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if code, _, _, e := runGit(ctx, []string{"add", "."}, repo); e != nil {
		t.Skipf("git add: %v", e)
	} else if code != 0 {
		t.Skipf("git add code=%d", code)
	}
	if _, _, _, e := runGit(ctx, []string{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "init"}, repo); e != nil {
		t.Skipf("git commit: %v", e)
	}

	// 创建 worktree
	info, err := wm.Create(ctx, repo, "test-slug", "", "agent-1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if info.Slug != "test-slug" {
		t.Errorf("slug = %s", info.Slug)
	}
	if info.AgentID != "agent-1" {
		t.Errorf("agentID = %s", info.AgentID)
	}

	// 快速恢复：再次 Create 应直接返回（不重新 add）
	info2, err := wm.Create(ctx, repo, "test-slug", "", "agent-1")
	if err != nil {
		t.Fatalf("Create resume: %v", err)
	}
	if info2.Path != info.Path {
		t.Errorf("resume path mismatch: %s vs %s", info2.Path, info.Path)
	}

	// List
	list, err := wm.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 worktree, got %d", len(list))
	}

	// Remove
	ok, err := wm.Remove(ctx, "test-slug")
	if err != nil || !ok {
		t.Fatalf("Remove: ok=%v err=%v", ok, err)
	}
	list, _ = wm.List(ctx)
	if len(list) != 0 {
		t.Errorf("after Remove, expected 0, got %d", len(list))
	}
}

// TestWorktreeCleanupStale 验证 cleanup_stale。
func TestWorktreeCleanupStale(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	wm, _ := NewWorktreeManager(home)
	repo := initRepo(t)
	ctx := context.Background()

	_, _ = wm.Create(ctx, repo, "active", "", "agent-active")
	_, _ = wm.Create(ctx, repo, "stale", "", "agent-stale")

	removed, err := wm.CleanupStale(ctx, map[string]bool{"agent-active": true})
	if err != nil {
		t.Fatalf("CleanupStale: %v", err)
	}
	if len(removed) != 1 || removed[0] != "stale" {
		t.Errorf("expected [stale], got %v", removed)
	}
	list, _ := wm.List(ctx)
	if len(list) != 1 {
		t.Errorf("after cleanup, expected 1, got %d", len(list))
	}
}

// initRepo 创建一个带初始 commit 的 git repo（复用）。
func initRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	ctx := context.Background()
	_ = writeFile(filepath.Join(repo, "README.md"), "init\n")
	_, _, _, _ = runGit(ctx, []string{"init"}, repo)
	_, _, _, _ = runGit(ctx, []string{"add", "."}, repo)
	_, _, _, _ = runGit(ctx, []string{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "init"}, repo)
	return repo
}

// writeFile 是 os.WriteFile 的薄封装，便于测试。
func writeFile(path, content string) error {
	return writeFileSync(path, content)
}
