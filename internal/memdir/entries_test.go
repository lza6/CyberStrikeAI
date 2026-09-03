package memdir

import (
	"crypto/sha1"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestProjectMemoryDir 验证目录路径与 sha1 隔离。
func TestProjectMemoryDir(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir() // 用真实存在的目录，Windows filepath.Abs 行为可预测
	dir, err := ProjectMemoryDir(home, cwd)
	if err != nil {
		t.Fatalf("ProjectMemoryDir: %v", err)
	}
	// M4 修复：显式断言 sha1(cwd)[:12] 派生（对 Windows 归一化路径后计算）
	abs, err := filepath.Abs(cwd)
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	h := sha1.Sum([]byte(abs))
	digest := hex.EncodeToString(h[:])[:12]
	expected := filepath.Join(home, "memory", filepath.Base(abs)+"-"+digest)
	if dir != expected {
		t.Errorf("dir = %s, want %s (sha1=%s)", dir, expected, digest)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Errorf("dir not created: %v", err)
	}
	// 不同 cwd 产生不同目录（隔离）
	dir2, _ := ProjectMemoryDir(home, t.TempDir())
	if dir == dir2 {
		t.Errorf("different cwd should produce different dirs: %s == %s", dir, dir2)
	}
}

// TestMemoryEntrypoint 验证入口文件路径。
func TestMemoryEntrypoint(t *testing.T) {
	home := t.TempDir()
	ep, err := MemoryEntrypoint(home, "/cwd")
	if err != nil {
		t.Fatalf("MemoryEntrypoint: %v", err)
	}
	if !strings.HasSuffix(ep, "MEMORY.md") {
		t.Errorf("entrypoint = %s, want MEMORY.md suffix", ep)
	}
}

// TestAddRemoveMemoryEntry 验证增删 + 索引维护。
func TestAddRemoveMemoryEntry(t *testing.T) {
	home := t.TempDir()
	cwd := "/proj"
	path, err := AddMemoryEntry(home, cwd, "My Title", "some content")
	if err != nil {
		t.Fatalf("AddMemoryEntry: %v", err)
	}
	if !strings.HasSuffix(path, "my_title.md") {
		t.Errorf("path = %s, want my_title.md", path)
	}
	// 索引应包含该条目
	ep, _ := MemoryEntrypoint(home, cwd)
	data, _ := os.ReadFile(ep)
	if !strings.Contains(string(data), "my_title.md") {
		t.Errorf("index missing entry: %s", data)
	}
	// 重复 Add 不应重复索引
	_, _ = AddMemoryEntry(home, cwd, "My Title", "other content")
	data, _ = os.ReadFile(ep)
	if strings.Count(string(data), "my_title.md") > 1 {
		t.Errorf("index duplicated entry: %s", data)
	}
	// 删除
	ok, err := RemoveMemoryEntry(home, cwd, "my_title")
	if err != nil || !ok {
		t.Fatalf("RemoveMemoryEntry: ok=%v err=%v", ok, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file not deleted after remove")
	}
	data, _ = os.ReadFile(ep)
	if strings.Contains(string(data), "my_title.md") {
		t.Errorf("index still contains entry after remove: %s", data)
	}
}

// TestLoadMemoryPrompt 验证 system prompt 注入。
func TestLoadMemoryPrompt(t *testing.T) {
	home := t.TempDir()
	cwd := "/proj"
	// 先加条目
	_, _ = AddMemoryEntry(home, cwd, "Topic", "detail")
	// 未创建 MEMORY.md 时应返回 not created yet
	prompt, err := LoadMemoryPrompt(home, "/empty-cwd", 200)
	if err != nil {
		t.Fatalf("LoadMemoryPrompt: %v", err)
	}
	if !strings.Contains(prompt, "(not created yet)") {
		t.Errorf("expected not created yet, got: %s", prompt)
	}
	// 有 MEMORY.md 时应包含内容
	_, _ = AddMemoryEntry(home, cwd, "Another", "more")
	prompt, _ = LoadMemoryPrompt(home, cwd, 200)
	if !strings.Contains(prompt, "## MEMORY.md") {
		t.Errorf("expected MEMORY.md section")
	}
	if !strings.Contains(prompt, "topic.md") {
		t.Errorf("expected index entries in prompt")
	}
}

// TestListMemoryFiles 验证列出 .md 文件（含 MEMORY.md 索引）。
func TestListMemoryFiles(t *testing.T) {
	home := t.TempDir()
	cwd := "/proj"
	_, _ = AddMemoryEntry(home, cwd, "Alpha", "a")
	_, _ = AddMemoryEntry(home, cwd, "Beta", "b")
	// 非 .md 文件应被忽略
	dir, _ := ProjectMemoryDir(home, cwd)
	_ = os.WriteFile(filepath.Join(dir, "note.txt"), []byte("x"), 0o644)
	files, err := ListMemoryFiles(home, cwd)
	if err != nil {
		t.Fatalf("ListMemoryFiles: %v", err)
	}
	// alpha.md + beta.md + MEMORY.md = 3（参照 OpenHarness memdir.py:11 glob *.md 含索引）
	if len(files) != 3 {
		t.Errorf("expected 3 .md files (alpha+beta+MEMORY), got %d", len(files))
	}
}

// TestScanMemory 验证 grep 式扫描。
func TestScanMemory(t *testing.T) {
	home := t.TempDir()
	cwd := "/proj"
	_, _ = AddMemoryEntry(home, cwd, "Alpha", "the quick brown fox")
	_, _ = AddMemoryEntry(home, cwd, "Beta", "lazy dog")
	hits, err := ScanMemory(home, cwd, "FOX")
	if err != nil {
		t.Fatalf("ScanMemory: %v", err)
	}
	if len(hits) != 1 {
		t.Errorf("expected 1 hit, got %d", len(hits))
	}
	if len(hits) > 0 && !strings.Contains(hits[0].Content, "fox") {
		t.Errorf("hit content = %s, want fox", hits[0].Content)
	}
	// 空查询返回 nil
	hits, _ = ScanMemory(home, cwd, "")
	if hits != nil {
		t.Errorf("empty query should return nil, got %v", hits)
	}
}

// TestEmptyHome 验证空 home 报错。
func TestEmptyHome(t *testing.T) {
	_, err := ProjectMemoryDir("", "/cwd")
	if err == nil {
		t.Error("expected error for empty home")
	}
}
