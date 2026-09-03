// Package microagent 提供可插拔上下文单元（microagent）——移植自 OpenHands
// microagent 体系。每个 microagent 是一个带 YAML frontmatter 的 Markdown 文件，
// 作为独立"上下文 bundle"按关键词触发或 always-on 注入对话。
//
// 与 internal/knowledge 的区别：
//   - knowledge = 向量语义检索（agent 主动调用 MCP 工具，不确定命中）
//   - microagent = 关键词确定性触发（substring 匹配，命中即注入）
//
// 设计为 leaf 包：只依赖标准库 + yaml，不反向导入 agent/handler/project，
// 避免循环依赖（与 internal/projectprompt 同一规避策略）。
package microagent

// MicroagentType microagent 类型。移植自 openhands/microagent/types.py:11-16。
type MicroagentType string

const (
	// TypeKnowledge 关键词触发的可选 microagent（triggers 非空时推断）。
	// 对应 OpenHands KnowledgeMicroagent。
	TypeKnowledge MicroagentType = "knowledge"
	// TypeRepo 仓库级 always-on microagent（无 triggers，始终注入）。
	// 对应 OpenHands RepoMicroagent。
	TypeRepo MicroagentType = "repo"
	// TypeTask 需要用户输入变量的特殊类型（/{agent_name} 触发）。
	// 对应 OpenHands TaskMicroagent。首期暂不完整支持变量提取。
	TypeTask MicroagentType = "task"
)

// InputMetadata task microagent 的输入变量元数据。
// 移植自 openhands/microagent/types.py:19-22。
type InputMetadata struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// Metadata microagent 元数据。移植自 openhands/microagent/types.py:26-37。
// YAML tag 对齐 frontmatter 字段名。
type Metadata struct {
	Name     string          `yaml:"name"`
	Type     MicroagentType  `yaml:"type"`
	Version  string          `yaml:"version"`
	Agent    string          `yaml:"agent"`
	Triggers []string        `yaml:"triggers"`
	Inputs   []InputMetadata `yaml:"inputs"`
}

// Knowledge 运行时载体——触发后注入上下文的轻量结构。
// 移植自 openhands/events/observation/agent.py:47-59 的 MicroagentKnowledge。
type Knowledge struct {
	// Name 触发的 microagent 名。
	Name string `json:"name"`
	// Trigger 命中的关键词（always-on repo 用空串）。
	Trigger string `json:"trigger"`
	// Content microagent 正文内容。
	Content string `json:"content"`
}
