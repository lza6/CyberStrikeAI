// Package memdir 提供基于文件的持久化项目记忆（memdir 范式）。
//
// 设计移植自参考项目 OpenHarness-main 的 src/openharness/memory/：
//   - paths.py:11  get_project_memory_dir —— 项目记忆目录（sha1(cwd)[:12] 隔离）
//   - memdir.py:11 list_memory_files / add_memory_entry / remove_memory_entry
//   - manager.py:10 load_memory_prompt —— 加载 MEMORY.md + 注入 system prompt
//   - scan.py / search.py —— 简单 grep 式扫描（轻量，非向量检索）
//
// 适配 CyberStrikeAI (Go)：
//   - 复用 internal/storage.HomeDir()（~/.cyberstrikeai/）作为数据根。
//   - 目录：<HomeDir>/memory/<projname>-<sha1(cwd)[:12]>/
//   - 与被其他会话占用的 internal/memory/（LightRAG 半成品）隔离，故用 internal/memdir/。
//   - 不依赖 CGO/SQLite，纯文件 + 标准库。
package memdir

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ProjectMemoryDir 返回项目的持久化记忆目录（不存在则创建）。
//
// 移植自 OpenHarness memory/paths.py:11 get_project_memory_dir。
// 路径：<homeDir>/memory/<projname>-<sha1(cwd)[:12]>/
func ProjectMemoryDir(homeDir, cwd string) (string, error) {
	if homeDir == "" {
		return "", errors.New("memdir: homeDir is empty")
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("memdir: resolve cwd: %w", err)
	}
	h := sha1.Sum([]byte(abs))
	digest := hex.EncodeToString(h[:])[:12]
	name := filepath.Base(abs)
	dir := filepath.Join(homeDir, "memory", fmt.Sprintf("%s-%s", name, digest))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("memdir: mkdir project memory: %w", err)
	}
	return dir, nil
}

// MemoryEntrypoint 返回项目记忆入口文件 MEMORY.md 的路径。
//
// 移植自 OpenHarness memory/paths.py:20 get_memory_entrypoint。
func MemoryEntrypoint(homeDir, cwd string) (string, error) {
	dir, err := ProjectMemoryDir(homeDir, cwd)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "MEMORY.md"), nil
}

// ListMemoryFiles 列出项目所有 .md 记忆文件（按文件名升序）。
//
// 移植自 OpenHarness memory/memdir.py:11 list_memory_files。
func ListMemoryFiles(homeDir, cwd string) ([]string, error) {
	dir, err := ProjectMemoryDir(homeDir, cwd)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("memdir: read memory dir: %w", err)
	}
	var result []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".md") {
			result = append(result, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(result)
	return result, nil
}

// slugRe 将标题转为文件名安全 slug。
var slugRe = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// AddMemoryEntry 创建一个记忆文件并追加 MEMORY.md 索引。
//
// 移植自 OpenHarness memory/memdir.py:17 add_memory_entry。
func AddMemoryEntry(homeDir, cwd, title, content string) (string, error) {
	dir, err := ProjectMemoryDir(homeDir, cwd)
	if err != nil {
		return "", err
	}
	slug := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(title)), "_"), "_")
	if slug == "" {
		slug = "memory"
	}
	path := filepath.Join(dir, slug+".md")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(content)+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("memdir: write memory entry: %w", err)
	}
	entry, err := MemoryEntrypoint(homeDir, cwd)
	if err != nil {
		return path, nil // 文件已写，索引失败不阻断
	}
	existing := "# Memory Index\n"
	if data, err := os.ReadFile(entry); err == nil {
		existing = string(data)
	}
	if !strings.Contains(existing, slug+".md") {
		existing = strings.TrimRight(existing, "\n") + fmt.Sprintf("\n- [%s](%s)\n", title, filepath.Base(path))
		if err := os.WriteFile(entry, []byte(existing), 0o600); err != nil {
			return path, nil
		}
	}
	return path, nil
}

// RemoveMemoryEntry 删除一个记忆文件并从 MEMORY.md 索引移除。
//
// 移植自 OpenHarness memory/memdir.py:32 remove_memory_entry。返回是否删除成功。
func RemoveMemoryEntry(homeDir, cwd, name string) (bool, error) {
	dir, err := ProjectMemoryDir(homeDir, cwd)
	if err != nil {
		return false, err
	}
	// 匹配 stem 或全名
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, fmt.Errorf("memdir: read memory dir: %w", err)
	}
	var target string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.TrimSuffix(e.Name(), ".md") == name || e.Name() == name {
			target = filepath.Join(dir, e.Name())
			break
		}
	}
	if target == "" {
		return false, nil
	}
	if err := os.Remove(target); err != nil {
		return false, fmt.Errorf("memdir: remove memory entry: %w", err)
	}
	// 从 MEMORY.md 移除索引行
	entry, err := MemoryEntrypoint(homeDir, cwd)
	if err != nil {
		return true, nil
	}
	if data, err := os.ReadFile(entry); err == nil {
		var lines []string
		for _, line := range strings.Split(string(data), "\n") {
			if !strings.Contains(line, filepath.Base(target)) {
				lines = append(lines, line)
			}
		}
		out := strings.TrimSpace(strings.Join(lines, "\n"))
		if out == "" {
			out = "# Memory Index"
		}
		_ = os.WriteFile(entry, []byte(out+"\n"), 0o600)
	}
	return true, nil
}

// LoadMemoryPrompt 返回注入 system prompt 的记忆段落。
//
// 移植自 OpenHarness memory/manager.py:10 load_memory_prompt。
// maxLines 限制 MEMORY.md 行数（默认 200）。
func LoadMemoryPrompt(homeDir, cwd string, maxLines int) (string, error) {
	if maxLines <= 0 {
		maxLines = 200
	}
	dir, err := ProjectMemoryDir(homeDir, cwd)
	if err != nil {
		return "", err
	}
	entry, err := MemoryEntrypoint(homeDir, cwd)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	sb.WriteString("# Memory\n")
	sb.WriteString(fmt.Sprintf("- Persistent memory directory: %s\n", dir))
	sb.WriteString("- Use this directory to store durable user or project context that should survive future sessions.\n")
	sb.WriteString("- Prefer concise topic files plus an index entry in MEMORY.md.\n")
	if data, err := os.ReadFile(entry); err == nil {
		lines := strings.Split(string(data), "\n")
		if len(lines) > maxLines {
			lines = lines[:maxLines]
		}
		if len(lines) > 0 {
			sb.WriteString("\n## MEMORY.md\n```md\n")
			sb.WriteString(strings.Join(lines, "\n"))
			sb.WriteString("\n```\n")
		}
	} else {
		sb.WriteString("\n## MEMORY.md\n(not created yet)\n")
	}
	return sb.String(), nil
}

// Hit 是扫描记忆文件命中的单条结果。
type Hit struct {
	File    string // 命中文件路径
	Line    int    // 行号（1-indexed）
	Content string // 命中行内容
}

// ScanMemory 在项目记忆文件中扫描包含 query 的行（大小写不敏感）。
//
// 移植自 OpenHarness memory/scan.py + search.py 的轻量 grep 式扫描。
// 非向量检索，纯字符串包含匹配，按文件名→行号排序。
func ScanMemory(homeDir, cwd, query string) ([]Hit, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil, nil
	}
	files, err := ListMemoryFiles(homeDir, cwd)
	if err != nil {
		return nil, err
	}
	var hits []Hit
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(strings.ToLower(line), q) {
				hits = append(hits, Hit{File: f, Line: i + 1, Content: line})
			}
		}
	}
	return hits, nil
}
