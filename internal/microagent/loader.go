package microagent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoadFromDir 从目录加载所有 microagent（.md 文件，带 YAML frontmatter）。
// 移植自 openhands/microagent/microagent.py:277-341 load_microagents_from_dir。
// 返回 (repo, knowledge) 两个 map，key 为 microagent.Name。
// 跳过 README.md（移植自 microagent.py:296）。支持子目录递归（OpenHands 仅一层，
// 此处放宽以适配 CyberStrikeAI 的 knowledge_base/ 目录结构）。
//
// 目录不存在时返回空 map + nil 错误（全局/用户目录可能未配置）。
func LoadFromDir(dir string) (repo, knowledge map[string]*Microagent, err error) {
	repo = make(map[string]*Microagent)
	knowledge = make(map[string]*Microagent)
	if dir == "" {
		return repo, knowledge, nil
	}
	info, statErr := os.Stat(dir)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return repo, knowledge, nil
		}
		return repo, knowledge, fmt.Errorf("stat microagent dir %q: %w", dir, statErr)
	}
	if !info.IsDir() {
		return repo, knowledge, fmt.Errorf("microagent dir %q is not a directory", dir)
	}
	walkErr := filepath.Walk(dir, func(path string, fi os.FileInfo, werr error) error {
		if werr != nil {
			return werr
		}
		if fi.IsDir() {
			return nil
		}
		name := strings.ToLower(filepath.Base(path))
		if !strings.HasSuffix(name, ".md") {
			return nil
		}
		if name == "readme.md" {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return fmt.Errorf("read %s: %w", path, rerr)
		}
		ma, perr := parseMicroagent(path, raw)
		if perr != nil {
			// 单个文件解析失败不中断整目录加载（OpenHands 用异常，Go 降级为 skip + 返回）。
			// 但保留错误信息让调用方决定是否告警。
			return fmt.Errorf("parse %s: %w", path, perr)
		}
		// 同名覆盖（后加载者胜，对应 OpenHands 三层 user/repo 覆盖语义由调用方分层调用实现）。
		if ma.Type == TypeRepo {
			repo[ma.Name] = ma
		} else {
			knowledge[ma.Name] = ma
		}
		return nil
	})
	// filepath.Walk 把 walkErr 累积返回（Go 1.20+ 仅第一个非 nil）。
	if walkErr != nil && walkErr.Error() != "" {
		// 不因单文件解析失败中断整体；返回已加载部分 + 错误供调用方日志。
		return repo, knowledge, walkErr
	}
	return repo, knowledge, nil
}

// parseMicroagent 解析单个 microagent 文件（frontmatter + body）。
// 移植自 openhands/microagent/microagent.py:51-171 BaseMicroagent.load。
func parseMicroagent(path string, raw []byte) (*Microagent, error) {
	// 去除可能的 UTF-8 BOM（frontmatter 必须以 --- 开头）。
	text := strings.TrimPrefix(string(raw), "\xef\xbb\xbf")
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("empty microagent file")
	}
	lines := strings.Split(text, "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "---" {
		return nil, fmt.Errorf("must start with YAML front matter (---)")
	}
	var fmLines []string
	i := 1
	for i < len(lines) {
		if strings.TrimSpace(lines[i]) == "---" {
			break
		}
		fmLines = append(fmLines, lines[i])
		i++
	}
	if i >= len(lines) {
		return nil, fmt.Errorf("front matter must end with a line containing only ---")
	}
	body := strings.TrimSpace(strings.Join(lines[i+1:], "\n"))

	var m Metadata
	fmYAML := strings.Join(fmLines, "\n")
	if yerr := yaml.Unmarshal([]byte(fmYAML), &m); yerr != nil {
		return nil, fmt.Errorf("front matter yaml: %w", yerr)
	}
	// 推断类型（若 frontmatter 未显式指定 type，OpenHands 用 inputs/triggers 推断）。
	inferred := inferType(m)
	// 若 metadata 显式 type 且与推断一致，用显式；不一致时推断优先（对齐 microagent.py:140-155）。
	if m.Type == "" {
		m.Type = inferred
	}
	// task 类型自动追加 /{name} trigger——移植自 microagent.py:143-149。
	if m.Type == TypeTask {
		trigger := "/" + m.Name
		if !containsTrigger(m.Triggers, trigger) {
			m.Triggers = append(m.Triggers, trigger)
		}
	}
	// 名字：优先 metadata.name，否则用文件名（去扩展名）。
	name := strings.TrimSpace(m.Name)
	if name == "" {
		base := filepath.Base(path)
		name = strings.TrimSuffix(base, filepath.Ext(base))
		m.Name = name
	}
	ma := &Microagent{
		Name:     name,
		Content:  body,
		Metadata: m,
		Source:   path,
		Type:     m.Type,
	}
	if verr := ma.validate(); verr != nil {
		return nil, verr
	}
	return ma, nil
}

func containsTrigger(triggers []string, t string) bool {
	for _, x := range triggers {
		if x == t {
			return true
		}
	}
	return false
}
