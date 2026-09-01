package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTestFile 在 path 写入内容（自动建父目录）。
func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func assertTestFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", path, data, want)
	}
}

func assertNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("%s still exists or stat failed: %v", path, err)
	}
}

// TestHomeDir_RespectsEnv 验证 $CYBERSTRIKEAI_HOME 覆盖默认。
func TestHomeDir_RespectsEnv(t *testing.T) {
	t.Setenv(HomeEnv, "/tmp/csa-home-test")
	if got := HomeDir(); got != "/tmp/csa-home-test" {
		t.Fatalf("HomeDir 应返回 env 值, got %q", got)
	}
}

// TestHomeDir_FallsBackToUserHome 验证 env 为空时回退到 ~/.cyberstrikeai。
func TestHomeDir_FallsBackToUserHome(t *testing.T) {
	t.Setenv(HomeEnv, "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("无法获取 user home")
	}
	want := filepath.Join(home, defaultHomeName)
	if got := HomeDir(); got != want {
		t.Fatalf("HomeDir 应回退到 %q, got %q", want, got)
	}
}

// TestMigrateLegacyData_MovesFiles 验证迁移把文件从 legacy 移到 home，源被清空。
func TestMigrateLegacyData_MovesFiles(t *testing.T) {
	legacy := t.TempDir()
	home := t.TempDir()
	writeTestFile(t, filepath.Join(legacy, "conversations.db"), "db")
	writeTestFile(t, filepath.Join(legacy, "logs", "app.log"), "log")
	writeTestFile(t, filepath.Join(legacy, "knowledge.db"), "kb")

	if err := MigrateLegacyData(legacy, home); err != nil {
		t.Fatalf("MigrateLegacyData: %v", err)
	}
	assertTestFile(t, filepath.Join(home, "conversations.db"), "db")
	assertTestFile(t, filepath.Join(home, "logs", "app.log"), "log")
	assertTestFile(t, filepath.Join(home, "knowledge.db"), "kb")
	// 源目录应已清空文件（空目录可能保留，但文件应消失）
	assertNotExist(t, filepath.Join(legacy, "conversations.db"))
	assertNotExist(t, filepath.Join(legacy, "knowledge.db"))
}

// TestMigrateLegacyData_SkipsExistingDestination 验证目标已存在时跳过，不覆盖。
func TestMigrateLegacyData_SkipsExistingDestination(t *testing.T) {
	legacy := t.TempDir()
	home := t.TempDir()
	writeTestFile(t, filepath.Join(legacy, "conversations.db"), "legacy-content")
	writeTestFile(t, filepath.Join(home, "conversations.db"), "home-wins")

	if err := MigrateLegacyData(legacy, home); err != nil {
		t.Fatalf("MigrateLegacyData: %v", err)
	}
	// 目标保留
	assertTestFile(t, filepath.Join(home, "conversations.db"), "home-wins")
}

// TestMigrateLegacyData_MovesSQLiteSidecars 验证 SQLite -wal/-shm sidecar 与主库一起迁移。
func TestMigrateLegacyData_MovesSQLiteSidecars(t *testing.T) {
	legacy := t.TempDir()
	home := t.TempDir()
	writeTestFile(t, filepath.Join(legacy, "conversations.db"), "main")
	writeTestFile(t, filepath.Join(legacy, "conversations.db-wal"), "wal")
	writeTestFile(t, filepath.Join(legacy, "conversations.db-shm"), "shm")

	if err := MigrateLegacyData(legacy, home); err != nil {
		t.Fatalf("MigrateLegacyData: %v", err)
	}
	assertTestFile(t, filepath.Join(home, "conversations.db"), "main")
	assertTestFile(t, filepath.Join(home, "conversations.db-wal"), "wal")
	assertTestFile(t, filepath.Join(home, "conversations.db-shm"), "shm")
}

// TestMigrateLegacyData_NoSourceReturnsNil 验证源目录不存在时返回 nil 不报错。
func TestMigrateLegacyData_NoSourceReturnsNil(t *testing.T) {
	home := t.TempDir()
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if err := MigrateLegacyData(missing, home); err != nil {
		t.Fatalf("源不存在应返回 nil, got %v", err)
	}
	// home 应被创建
	if _, err := os.Stat(home); err != nil {
		t.Fatalf("home 应被创建: %v", err)
	}
}

// TestMigrateLegacyData_SamePathReturnsNil 验证 legacy 与 home 相同路径时早退不操作。
func TestMigrateLegacyData_SamePathReturnsNil(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "keep.db"), "keep")
	if err := MigrateLegacyData(dir, dir); err != nil {
		t.Fatalf("相同路径应返回 nil, got %v", err)
	}
	// 文件应原样保留
	assertTestFile(t, filepath.Join(dir, "keep.db"), "keep")
}

// TestMigrateLegacyData_EmptyArgsReturnsNil 验证空参数返回 nil。
func TestMigrateLegacyData_EmptyArgsReturnsNil(t *testing.T) {
	if err := MigrateLegacyData("", t.TempDir()); err != nil {
		t.Fatalf("空 legacy 应返回 nil, got %v", err)
	}
	if err := MigrateLegacyData(t.TempDir(), ""); err != nil {
		t.Fatalf("空 home 应返回 nil, got %v", err)
	}
}

// TestMigrateLegacyData_IdempotentRetry 验证重复迁移不破坏数据（幂等）。
func TestMigrateLegacyData_IdempotentRetry(t *testing.T) {
	legacy := t.TempDir()
	home := t.TempDir()
	writeTestFile(t, filepath.Join(legacy, "conversations.db"), "db")

	if err := MigrateLegacyData(legacy, home); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	// 第二次：legacy 已无文件（被移走），应无错
	if err := MigrateLegacyData(legacy, home); err != nil {
		t.Fatalf("second migrate should be nil, got %v", err)
	}
	assertTestFile(t, filepath.Join(home, "conversations.db"), "db")
}

// TestMigrateLegacyData_NestedSubdirs 验证嵌套子目录递归迁移。
func TestMigrateLegacyData_NestedSubdirs(t *testing.T) {
	legacy := t.TempDir()
	home := t.TempDir()
	writeTestFile(t, filepath.Join(legacy, "a", "b", "c", "deep.txt"), "deep")

	if err := MigrateLegacyData(legacy, home); err != nil {
		t.Fatalf("MigrateLegacyData: %v", err)
	}
	assertTestFile(t, filepath.Join(home, "a", "b", "c", "deep.txt"), "deep")
}

// TestEnsureHome_CreatesDir 验证 EnsureHome 创建目录。
func TestEnsureHome_CreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "home")
	if err := EnsureHome(dir); err != nil {
		t.Fatalf("EnsureHome: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("目录未创建: %v", err)
	}
}

// TestEnsureHome_EmptyReturnsError 验证空 home 返回错误。
func TestEnsureHome_EmptyReturnsError(t *testing.T) {
	if err := EnsureHome(""); err == nil {
		t.Fatal("空 home 应报错")
	}
}

// TestSameFilesystemPath 验证路径相同性判断（Clean 后等价）。
func TestSameFilesystemPath(t *testing.T) {
	if !sameFilesystemPath("/a/b", "/a/b") {
		t.Fatal("相同路径应判定为 same")
	}
	if sameFilesystemPath("/a/b", "/a/c") {
		t.Fatal("不同路径应判定为 not same")
	}
}

// 确保测试文件被引用（避免 unused import 在某些构建配置下报错）。
var _ = strings.TrimSpace
