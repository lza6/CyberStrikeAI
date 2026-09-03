package capability

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModifyFileProviderLifecycle(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	backupDir := filepath.Join(dir, "backup")
	if err := os.WriteFile(target, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	p := NewModifyFileProvider(backupDir)

	args := map[string]interface{}{"path": target, "content": "modified"}

	// Plan
	plan, err := p.Plan(args)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Target != target || (plan.Action != "modify" && plan.Action != "create") {
		t.Fatalf("Plan 不符: %+v", plan)
	}

	// Validate：已存在文件可写
	if err := p.Validate(args); err != nil {
		t.Fatal(err)
	}

	// Validate：新建文件（父目录存在）应通过（对齐 Eino write_file 语义）
	newFile := filepath.Join(dir, "subdir", "new.txt")
	if err := os.MkdirAll(filepath.Dir(newFile), 0755); err != nil {
		t.Fatal(err)
	}
	if err := p.Validate(map[string]interface{}{"path": newFile}); err != nil {
		t.Fatalf("新建文件 Validate 应通过: %v", err)
	}

	// Execute
	result, err := p.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(target)
	if string(data) != "modified" {
		t.Fatalf("文件内容应为 modified，got %q", data)
	}
	// 结果文本含备份路径
	if !strings.Contains(result.Content[0].Text, ".bak") {
		t.Fatalf("结果应含备份路径: %q", result.Content[0].Text)
	}

	// Rollback：Execute 把备份路径暂存到 args["_backup_path"]，回填 plan 后回滚。
	if bp, ok := args["_backup_path"].(string); ok {
		plan.BackupPath = bp
	} else {
		plan.BackupPath = findBackup(backupDir)
	}
	if err := p.Rollback(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(target)
	if string(data) != "original" {
		t.Fatalf("回滚后应为 original，got %q", data)
	}
}

// TestCreateFileProviderLifecycle J5 修复：新建文件（原不存在）走完整生命周期 + 回滚删除。
func TestCreateFileProviderLifecycle(t *testing.T) {
	dir := t.TempDir()
	backupDir := filepath.Join(dir, "backup")
	p := NewModifyFileProvider(backupDir)

	newFile := filepath.Join(dir, "fresh.txt")
	args := map[string]interface{}{"path": newFile, "content": "fresh-content"}

	plan, err := p.Plan(args)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != "create" {
		t.Fatalf("新建文件 Plan.Action 应为 create，got %q", plan.Action)
	}
	if err := p.Validate(args); err != nil {
		t.Fatalf("新建 Validate 应通过: %v", err)
	}
	result, err := p.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("新建 Execute 失败: %v", err)
	}
	if !strings.Contains(result.Content[0].Text, "已创建") {
		t.Fatalf("结果应含已创建，got %q", result.Content[0].Text)
	}
	data, _ := os.ReadFile(newFile)
	if string(data) != "fresh-content" {
		t.Fatalf("文件内容应为 fresh-content，got %q", data)
	}
	// 回滚：新建文件应被删除
	if err := p.Rollback(context.Background(), plan); err != nil {
		t.Fatalf("新建回滚失败: %v", err)
	}
	if _, err := os.Stat(newFile); !os.IsNotExist(err) {
		t.Fatalf("回滚后新建文件应被删除，got err=%v", err)
	}
}

func TestGetProvider(t *testing.T) {
	dir := t.TempDir()
	_ = NewModifyFileProvider(dir)
	if GetProvider("modify-file") == nil {
		t.Fatal("modify-file 应已注册 provider")
	}
	if GetProvider("nonexistent-tool") != nil {
		t.Fatal("未注册工具应返回 nil")
	}
	if !contains(SupportedTools(), "modify-file") {
		t.Fatalf("SupportedTools 应含 modify-file: %v", SupportedTools())
	}
}

func findBackup(dir string) string {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".bak") {
			return filepath.Join(dir, e.Name())
		}
	}
	return ""
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
