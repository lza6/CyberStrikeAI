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
	if plan.Target != target || plan.Action != "modify" {
		t.Fatalf("Plan 不符: %+v", plan)
	}

	// Validate：文件存在可写
	if err := p.Validate(args); err != nil {
		t.Fatal(err)
	}

	// Validate：文件不存在报错
	if err := p.Validate(map[string]interface{}{"path": filepath.Join(dir, "nope.txt")}); err == nil {
		t.Fatal("不存在的文件应 Validate 失败")
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

	// CollectArtifacts：从 result 文本里提备份路径不便，直接用 plan+手动填
	plan.BackupPath = findBackup(backupDir)
	arts, err := p.CollectArtifacts(plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(arts) != 1 || len(arts[0].SHA256) != 64 {
		t.Fatalf("应产出 1 个 SHA256 工件: %+v", arts)
	}

	// Rollback：恢复 original
	if err := p.Rollback(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(target)
	if string(data) != "original" {
		t.Fatalf("回滚后应为 original，got %q", data)
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
