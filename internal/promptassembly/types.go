// Package promptassembly 提供结构化 prompt 组装——移植自 OpenHands
// openhands/utils/prompt.py 的 ConversationInstructions / RepositoryInfo / RuntimeInfo
// 三 struct + PromptManager 模板渲染体系。
//
// 与 internal/projectprompt 的区别：
//   - projectprompt = 纯字符串拼接工厂（无 struct，无字段级断言）
//   - promptassembly = struct 化 + Go text/template 渲染，字段级可测试、
//     条件渲染、幂等输出（同一 struct 多次渲染一致）
//
// 设计为 leaf 包：只依赖标准库（text/template/strings/time），不反向导入
// agent/handler/project，避免循环依赖（与 internal/projectprompt 同一规避策略）。
//
// 与 Eino 衔接：Manager.Render(structs) → string 后，仍由调用方走
// project.AppendSystemPromptBlock 接到 Eino Instruction，无需改动 Eino 接口。
package promptassembly

import (
	"time"
)

// ConversationInstructions 贯穿整轮对话的附加指令。
// 移植自 openhands/utils/prompt.py:30-40 ConversationInstructions。
// 用途：本轮目标、授权边界、验证要求、resolver 指令等。
type ConversationInstructions struct {
	// Content 指令内容。
	Content string
}

// RepositoryInfo 仓库元信息。移植自 openhands/utils/prompt.py:21-27 RepositoryInfo。
// CyberStrikeAI 无仓库 clone 语义，此处字段保留为通用"项目资产元信息"载体；
// 调用方可不填（条件渲染会跳过空块），或映射为项目资产目录路径。
type RepositoryInfo struct {
	RepoName      string
	RepoDirectory string
	BranchName    string
}

// RuntimeInfo 运行时状态。移植自 openhands/utils/prompt.py:13-18 RuntimeInfo。
type RuntimeInfo struct {
	// Date UTC 日期（注入 prompt 帮助 LLM 感知当前时间）。
	Date string
	// WorkingDir 当前工作目录。
	WorkingDir string
	// AvailableHosts 可用主机:端口映射（可选）。
	AvailableHosts map[string]int
	// AdditionalAgentInstructions 额外运行时指令。
	AdditionalAgentInstructions string
	// CustomSecretsDescriptions 自定义密钥描述（可选）。
	CustomSecretsDescriptions map[string]string
}

// IsEmpty 是否无任何内容（用于条件渲染判断）。
func (r RuntimeInfo) IsEmpty() bool {
	return r.Date == "" && r.WorkingDir == "" && len(r.AvailableHosts) == 0 &&
		r.AdditionalAgentInstructions == "" && len(r.CustomSecretsDescriptions) == 0
}

// MicroagentKnowledge 触发的 microagent 载体（与 internal/microagent.Knowledge 对齐字段）。
// 移植自 openhands/events/observation/agent.py:47-59 MicroagentKnowledge。
type MicroagentKnowledge struct {
	Name    string
	Trigger string
	Content string
}

// Context 组装上下文——一次渲染所需的全部 struct 集合。
// 调用方构造此 struct 后传给 Manager.Render，返回拼好的 prompt 块字符串。
type Context struct {
	RepositoryInfo           RepositoryInfo
	RuntimeInfo              RuntimeInfo
	ConversationInstructions ConversationInstructions
	// RepoInstructions always-on repo microagent 拼接内容（来自 microagent.Registry.RepoContent）。
	RepoInstructions string
	// TriggeredMicroagents 关键词触发的 microagent 列表（来自 microagent.Registry.Retrieve）。
	TriggeredMicroagents []MicroagentKnowledge
}

// NewContext 构造空 Context。
func NewContext() Context { return Context{} }

// nowFunc 可被测试覆盖的时间函数（默认 time.Now().UTC）。
var nowFunc = func() time.Time { return time.Now().UTC() }

// DefaultDate 返回默认日期串（RFC3339 UTC），供 RuntimeInfo.Date 兜底。
func DefaultDate() string { return nowFunc().Format(time.RFC3339) }
