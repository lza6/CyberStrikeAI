// cmd/verbs-gate 实跑工具引用漂移门：扫描 skills/ 下 SKILL.md 引用的工具名，
// 与"真实工具全集"（builtin + tools/*.yaml name）比对，报告幽灵工具。
// 默认 report 模式 exit 0（只打报告）；-strict 模式发现幽灵 exit 1（供 CI 门禁）。
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/mcp/builtin"
	"cyberstrike-ai/internal/skillpackage"
)

func main() {
	skillsDir := flag.String("skills", "skills", "skills 目录")
	toolsDir := flag.String("tools", "tools", "tools 目录")
	strict := flag.Bool("strict", false, "发现幽灵工具时 exit 1（CI 门禁用）")
	flag.Parse()

	absSkills, _ := filepath.Abs(*skillsDir)

	// 真实工具全集
	realTools := map[string]bool{}
	for _, n := range builtin.GetAllBuiltinTools() {
		realTools[n] = true
	}
	// tools/*.yaml 的 name 字段
	yamlTools, err := loadYamlToolNames(*toolsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取 tools 目录失败: %v\n", err)
		os.Exit(2)
	}
	for _, n := range yamlTools {
		realTools[n] = true
	}
	// config.Security.Tools 内联工具（config.yaml 里的 tools 段）
	if cfg, err := config.Load("config.yaml"); err == nil {
		for _, t := range cfg.Security.Tools {
			if t.Name != "" {
				realTools[t.Name] = true
			}
		}
	}
	// 把 skill 自身目录名也加入白名单（skill 间相互引用是合法的，不算幽灵工具）
	if entries, derr := os.ReadDir(absSkills); derr == nil {
		for _, e := range entries {
			if e.IsDir() {
				realTools[e.Name()] = true
			}
		}
	}

	fmt.Printf("真实工具全集: %d 个（builtin %d + yaml %d + inline）\n",
		len(realTools), len(builtin.GetAllBuiltinTools()), len(yamlTools))

	violations, err := skillpackage.ScanToolReferences(absSkills, realTools)
	if err != nil {
		fmt.Fprintf(os.Stderr, "扫描失败: %v\n", err)
		os.Exit(2)
	}
	if len(violations) == 0 {
		fmt.Println("✓ 无幽灵工具引用（所有 skill 引用的工具名均在真实工具全集中）")
		os.Exit(0)
	}
	fmt.Printf("发现 %d 处幽灵工具引用：\n", len(violations))
	for _, v := range violations {
		fmt.Printf("  - [%s] %s:%d 引用 %q（%s）\n", v.Skill, v.File, v.Line, v.Referenced, v.Hint)
	}
	if *strict {
		os.Exit(1)
	}
	os.Exit(0)
}

// loadYamlToolNames 扫描 toolsDir 下所有 *.yaml，取 name 字段。
func loadYamlToolNames(toolsDir string) ([]string, error) {
	entries, err := os.ReadDir(toolsDir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || (filepath.Ext(e.Name()) != ".yaml" && filepath.Ext(e.Name()) != ".yml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(toolsDir, e.Name()))
		if err != nil {
			continue
		}
		// 简易取 name: xxx（避免引入 yaml 依赖；config.MergeToolsFromDir 已做完整解析，这里只做 verbs-gate 用的名字集）
		lines := string(data)
		for _, line := range splitLines(lines) {
			trim := trimLeftSpaces(line)
			if len(trim) > 6 && trim[:5] == "name:" {
				v := trim[5:]
				for len(v) > 0 && (v[0] == ' ' || v[0] == '\t') {
					v = v[1:]
				}
				// 去引号
				if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') {
					v = v[1 : len(v)-1]
				}
				v = trimRightSpace(v)
				if v != "" {
					names = append(names, v)
				}
				break
			}
		}
	}
	return names, nil
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func trimLeftSpaces(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] != ' ' && s[i] != '\t' {
			return s[i:]
		}
	}
	return ""
}

func trimRightSpace(s string) string {
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
