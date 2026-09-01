// Package playbooks 从 playbooks/ 目录加载分阶段攻击剧本（YAML）。
//
// 该包不依赖 database / handler / gin，仅做文件扫描 + yaml 解析，保持独立可测。
// 与 internal/config.LoadToolsFromDir、internal/agents.LoadMarkdownAgentsDir 风格一致。
package playbooks

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Playbook 一个攻击剧本（对应 playbooks/<name>.yaml）。
type Playbook struct {
	// Name 文件名（去 .yaml），作为 API 路由 :name 参数与前端 key。
	Name string `json:"name"`
	// DisplayName 展示名：优先取 yaml 顶层 name 字段，否则用文件名。
	DisplayName string `json:"display_name"`
	// Description yaml 顶层 description 字段。
	Description string `json:"description"`
	// Phases 阶段列表。
	Phases []PlaybookPhase `json:"phases"`
	// FilePath 源文件绝对路径（仅后端使用，不序列化到 API 摘要）。
	FilePath string `json:"-"`
}

// PlaybookPhase 单个阶段。
type PlaybookPhase struct {
	// Name 阶段名（如 reconnaissance、api_vulnerabilities）。
	Name string `yaml:"name" json:"name"`
	// Description 阶段说明（可选）。yaml 里通常用 post_analysis 承载说明，
	// 这里同时暴露 description 与 post_analysis 以兼容两种写法。
	Description string `yaml:"description" json:"description"`
	// Steps 阶段步骤提示（可选，对齐任务契约里的 Steps 字段）。
	Steps []string `yaml:"steps" json:"steps"`
	// Tools 该阶段调用的工具名列表（对应 tools/*.yaml 的工具名）。
	Tools []string `yaml:"tools" json:"tools"`
	// PostAnalysis 阶段间 LLM 分析提示（yaml 字段 post_analysis）。
	PostAnalysis string `yaml:"post_analysis" json:"post_analysis,omitempty"`
}

// rawPlaybook yaml 顶层结构（仅用于解析，不直接对外暴露）。
type rawPlaybook struct {
	Name        string          `yaml:"name"`
	Description string          `yaml:"description"`
	Phases      []rawPlaybookPhase `yaml:"phases"`
}

// rawPlaybookPhase yaml phases[] 节点。tools 节点支持两种写法：
//   - 字符串数组：["subfinder", "httpx"]
//   - 对象数组：[{ name: subfinder, options: {...} }]
type rawPlaybookPhase struct {
	Name         string        `yaml:"name"`
	Description  string        `yaml:"description"`
	Steps        []string      `yaml:"steps"`
	Tools        []interface{} `yaml:"tools"`
	PostAnalysis string        `yaml:"post_analysis"`
}

// LoadPlaybooksFromDir 扫描 dir 下所有 *.yaml 文件（跳过 README.md 与非 yaml），
// 解析为 []Playbook。dir 为空或不存在返回空 slice + nil（与 LoadToolsFromDir 行为一致）。
//
// 文件名（去 .yaml）作为 Name；若 yaml 顶层有 name 字段则用作 DisplayName，否则用文件名。
// 返回结果按 Name 字典序稳定排序，便于 API 输出可预测。
func LoadPlaybooksFromDir(dir string) ([]Playbook, error) {
	var playbooks []Playbook

	if dir == "" {
		return playbooks, nil
	}

	info, err := os.Stat(dir)
	if os.IsNotExist(err) {
		return playbooks, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取剧本目录失败: %w", err)
	}
	if !info.IsDir() {
		return playbooks, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("读取剧本目录失败: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		// 跳过 README.md 以及非 yaml 文件（与 LoadToolsFromDir 一致，同时兼容 .yml）
		if strings.EqualFold(name, "README.md") {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(name), ".yaml") && !strings.HasSuffix(strings.ToLower(name), ".yml") {
			continue
		}

		filePath := filepath.Join(dir, name)
		pb, err := loadPlaybookFromFile(filePath, name)
		if err != nil {
			// 记录错误但继续加载其他文件（与 LoadToolsFromDir 行为一致）
			fmt.Printf("警告: 加载剧本文件 %s 失败: %v\n", filePath, err)
			continue
		}
		playbooks = append(playbooks, pb)
	}

	sort.SliceStable(playbooks, func(i, j int) bool {
		return playbooks[i].Name < playbooks[j].Name
	})

	return playbooks, nil
}

// loadPlaybookFromFile 解析单个 playbook 文件。
func loadPlaybookFromFile(filePath, fileName string) (Playbook, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return Playbook{}, fmt.Errorf("读取文件失败: %w", err)
	}

	var raw rawPlaybook
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return Playbook{}, fmt.Errorf("解析剧本配置失败: %w", err)
	}

	// 文件名（去扩展名）作为稳定 Name（路由 :name 参数用它）
	base := fileName
	if idx := strings.LastIndex(base, "."); idx > 0 {
		base = base[:idx]
	}

	displayName := raw.Name
	if displayName == "" {
		displayName = base
	}

	phases := make([]PlaybookPhase, 0, len(raw.Phases))
	for _, rp := range raw.Phases {
		phases = append(phases, PlaybookPhase{
			Name:         rp.Name,
			Description:  rp.Description,
			Steps:        rp.Steps,
			Tools:        normalizeTools(rp.Tools),
			PostAnalysis: rp.PostAnalysis,
		})
	}

	return Playbook{
		Name:        base,
		DisplayName: displayName,
		Description: raw.Description,
		Phases:      phases,
		FilePath:    filePath,
	}, nil
}

// normalizeTools 把 yaml tools 节点（字符串数组 | 对象数组）统一规整为工具名列表。
// 对象写法形如 { name: subfinder, options: {...} }，仅取 name 字段；取不到则跳过。
func normalizeTools(rawTools []interface{}) []string {
	out := make([]string, 0, len(rawTools))
	for _, t := range rawTools {
		switch v := t.(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				out = append(out, s)
			}
		case map[string]interface{}:
			if name, ok := v["name"]; ok {
				if s, ok := name.(string); ok {
					if s = strings.TrimSpace(s); s != "" {
						out = append(out, s)
					}
				}
			} else if name, ok := v["Name"]; ok {
				if s, ok := name.(string); ok {
					if s = strings.TrimSpace(s); s != "" {
						out = append(out, s)
					}
				}
			}
		case map[interface{}]interface{}:
			// yaml.v3 在某些情况下解析为 map[interface{}]interface{}
			if name, ok := v["name"]; ok {
				if s, ok := name.(string); ok {
					if s = strings.TrimSpace(s); s != "" {
						out = append(out, s)
					}
				}
			}
		}
	}
	return out
}
