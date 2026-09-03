package multiagent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberstrike-ai/internal/capability"
	"cyberstrike-ai/internal/einomcp"
	"cyberstrike-ai/internal/mcp"

	"go.uber.org/zap"
)

// TestFilesystemCapabilityGuard_BlocksInvalidPath J5：write_file 经 modify-file provider
// 的 Validate 失败时被拦（不执行原 wrapped）。
func TestFilesystemCapabilityGuard_BlocksInvalidPath(t *testing.T) {
	backupDir := filepath.Join(t.TempDir(), "cap-backup")
	capability.NewModifyFileProvider(backupDir)
	guard := newFilesystemCapabilityGuard()

	// 目标父目录不存在 → Validate 失败 → 拦（provider 对“文件不存在但父目录存在”视为合法新建放行，
	// 与 Eino write_file 的 MkdirAll+创建语义对齐；故真正无效路径 = 父目录缺失）。
	missing := filepath.Join(t.TempDir(), "nonexistent-subdir", "deep", "nope.txt")
	res, blocked, success := guard.CheckFilesystemTool(context.Background(), "write_file", map[string]interface{}{
		"path":    missing,
		"content": "x",
	})
	if !blocked {
		t.Fatalf("父目录不存在的目标应被拦，got blocked=%v res=%q", blocked, res)
	}
	if success {
		t.Fatalf("Validate 失败应 success=false，got success=%v res=%q", success, res)
	}
	if !strings.HasPrefix(res, einomcp.ToolErrorPrefix) {
		t.Fatalf("拦截文本应带 ToolError 前缀，got %q", res)
	}
}

// TestFilesystemCapabilityGuard_AllowsValidWrite J5：合法写文件经 provider 执行（含备份），返回结果文本。
func TestFilesystemCapabilityGuard_AllowsValidWrite(t *testing.T) {
	backupDir := filepath.Join(t.TempDir(), "cap-backup-valid")
	capability.NewModifyFileProvider(backupDir)
	guard := newFilesystemCapabilityGuard()

	target := filepath.Join(t.TempDir(), "target.txt")
	if err := os.WriteFile(target, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	res, blocked, success := guard.CheckFilesystemTool(context.Background(), "write_file", map[string]interface{}{
		"path":    target,
		"content": "modified",
	})
	if !blocked {
		t.Fatalf("provider 命中应返回 blocked=true（结果替换原 wrapped），got %v", blocked)
	}
	if !success {
		t.Fatalf("provider 执行成功应 success=true，got success=%v", success)
	}
	data, _ := os.ReadFile(target)
	if string(data) != "modified" {
		t.Fatalf("文件应被 provider 改写为 modified，got %q", data)
	}
	if res == "" {
		t.Fatal("provider 执行成功应返回结果文本")
	}
}

// TestFilesystemCapabilityGuard_CreateNewFile J5 P0-5 回归：新文件写入（原不存在）应走 create 语义
// 不被 Validate 误拦（对齐 Eino write_file 创建或覆盖语义），执行成功、回滚删除。
func TestFilesystemCapabilityGuard_CreateNewFile(t *testing.T) {
	backupDir := filepath.Join(t.TempDir(), "cap-backup-create")
	capability.NewModifyFileProvider(backupDir)
	guard := newFilesystemCapabilityGuard()

	dir := t.TempDir()
	newFile := filepath.Join(dir, "fresh.txt")
	res, blocked, success := guard.CheckFilesystemTool(context.Background(), "write_file", map[string]interface{}{
		"path":    newFile,
		"content": "fresh-content",
	})
	if !blocked || !success {
		t.Fatalf("新文件写入应经 provider 成功执行，got blocked=%v success=%v res=%q", blocked, success, res)
	}
	data, err := os.ReadFile(newFile)
	if err != nil || string(data) != "fresh-content" {
		t.Fatalf("新文件应被创建，got err=%v data=%q", err, data)
	}
	if !strings.Contains(res, "已创建") {
		t.Fatalf("结果文本应含已创建，got %q", res)
	}
}

// TestFilesystemCapabilityGuard_ReadToolsNotGuarded J5：read_file 只读工具不映射 provider，放行。
func TestFilesystemCapabilityGuard_ReadToolsNotGuarded(t *testing.T) {
	guard := newFilesystemCapabilityGuard()
	res, blocked, success := guard.CheckFilesystemTool(context.Background(), "read_file", map[string]interface{}{"path": "/etc/hosts"})
	if blocked {
		t.Fatalf("read_file 只读不应被拦，got blocked=%v res=%q", blocked, res)
	}
	if success {
		t.Fatalf("read_file 未命中 provider 应 success=false，got success=%v", success)
	}
}

// TestFilesystemCapabilityGuard_EditFileNotGuarded Critic 终审 P0 回归：edit_file 不映射 provider。
// edit_file 参数是 file_path/old_string/new_string（无 content 键），若经 provider 会把
// args["content"] 缺省为空串 → os.WriteFile 空内容 → 整文件被清空。必须放行走原生 Edit 语义。
func TestFilesystemCapabilityGuard_EditFileNotGuarded(t *testing.T) {
	backupDir := filepath.Join(t.TempDir(), "cap-backup-edit")
	capability.NewModifyFileProvider(backupDir)
	guard := newFilesystemCapabilityGuard()

	// 模拟 Eino edit_file 的真实参数形态：file_path/old_string/new_string，无 content。
	res, blocked, success := guard.CheckFilesystemTool(context.Background(), "edit_file", map[string]interface{}{
		"file_path":  "/etc/hosts",
		"old_string": "80",
		"new_string": "8080",
	})
	if blocked {
		t.Fatalf("edit_file 不应映射 provider（会清空文件），got blocked=%v res=%q", blocked, res)
	}
	if success {
		t.Fatalf("edit_file 未命中 provider 应 success=false，got success=%v", success)
	}
}

// TestFilesystemCapabilityGuard_WriteFileEinoArgs J5：Eino write_file 真实参数形态
// （file_path/content 键名）经 requirePathArg 的 file_path 兼容可正确取到目标。
func TestFilesystemCapabilityGuard_WriteFileEinoArgs(t *testing.T) {
	backupDir := filepath.Join(t.TempDir(), "cap-backup-einoargs")
	capability.NewModifyFileProvider(backupDir)
	guard := newFilesystemCapabilityGuard()

	dir := t.TempDir()
	target := filepath.Join(dir, "eino-style.txt")
	// Eino writeFileArgs 的键是 file_path/content（非 path/content）。
	res, blocked, success := guard.CheckFilesystemTool(context.Background(), "write_file", map[string]interface{}{
		"file_path": target,
		"content":   "eino-content",
	})
	if !blocked || !success {
		t.Fatalf("eino 参数形态 write_file 应经 provider 成功，got blocked=%v success=%v res=%q", blocked, success, res)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "eino-content" {
		t.Fatalf("文件应被写入，got err=%v data=%q", err, data)
	}
}

// TestNewExecuteScopeGuard_NilWhenNoProject J4：projectID/db 为空返回 nil（不校验）。
func TestNewExecuteScopeGuard_NilWhenNoProject(t *testing.T) {
	if g := newExecuteScopeGuard(nil, "", zap.NewNop()); g != nil {
		t.Fatalf("db/projectID 为空应返回 nil，got %v", g)
	}
}

// 占位：保证 mcp import 使用。
var _ = mcp.MCPConversationIDFromContext
