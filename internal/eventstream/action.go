package eventstream

// RecallType 召回类型。移植自 openhands/events/recall_type.py:4-11 RecallType。
type RecallType string

const (
	// RecallTypeWorkspaceContext 工作区上下文（仓库指令、运行时信息）。
	RecallTypeWorkspaceContext RecallType = "workspace_context"
	// RecallTypeKnowledge 知识 microagent（关键词触发）。
	RecallTypeKnowledge RecallType = "knowledge"
)

// RecallAction 召回动作——一等公民 Action。
// 移植自 openhands/events/action/agent.py:89-104 RecallAction。
// AgentController 在收到用户消息时发布此事件，Memory 订阅者消费后产出 RecallObservation。
type RecallAction struct {
	BaseEvent
	// RecallType 召回类型。
	RecallType RecallType `json:"recall_type"`
	// Query 检索查询（用户消息内容或空）。
	Query string `json:"query"`
	// Thought 触发说明（可选）。
	Thought string `json:"thought,omitempty"`
}

// EventType 实现 Event 接口。
func (a *RecallAction) EventType() string { return "recall_action" }

// Message 人类可读摘要。移植自 openhands/events/action/agent.py:97-99 message 属性。
func (a *RecallAction) Message() string {
	q := a.Query
	if len(q) > 50 {
		q = q[:50]
	}
	return "Retrieving content for: " + q
}

// CondensationAction 上下文压缩动作——依赖 event_id 单调递增做范围遗忘。
// 移植自 openhands/events/action/agent.py:107-188 CondensationAction。
type CondensationAction struct {
	BaseEvent
	// ForgottenEventIDs 显式遗忘的事件 ID 列表。
	ForgottenEventIDs []int64 `json:"forgotten_event_ids,omitempty"`
	// ForgottenEventsStartID 范围遗忘起始 ID（含）。
	ForgottenEventsStartID int64 `json:"forgotten_events_start_id,omitempty"`
	// ForgottenEventsEndID 范围遗忘结束 ID（含）。
	ForgottenEventsEndID int64 `json:"forgotten_events_end_id,omitempty"`
	// Summary 压缩摘要。
	Summary string `json:"summary,omitempty"`
	// SummaryOffset 摘要覆盖起始事件 ID。
	SummaryOffset int64 `json:"summary_offset,omitempty"`
}

func (a *CondensationAction) EventType() string { return "condensation_action" }

// RecallObservation 召回观察——与 RecallAction 配对的 Observation。
// 移植自 openhands/events/observation/agent.py:62-138 RecallObservation。
// Cause 字段（来自 BaseEvent）记录对应 RecallAction 的 ID，形成 cause 链。
type RecallObservation struct {
	BaseEvent
	// RecallType 对应的召回类型。
	RecallType RecallType `json:"recall_type"`
	// === WORKSPACE_CONTEXT 分支字段 ===
	RepoName                    string            `json:"repo_name,omitempty"`
	RepoDirectory               string            `json:"repo_directory,omitempty"`
	RepoBranch                  string            `json:"repo_branch,omitempty"`
	RepoInstructions            string            `json:"repo_instructions,omitempty"`
	RuntimeHosts                map[string]int    `json:"runtime_hosts,omitempty"`
	AdditionalAgentInstructions string            `json:"additional_agent_instructions,omitempty"`
	Date                        string            `json:"date,omitempty"`
	CustomSecretsDescriptions   map[string]string `json:"custom_secrets_descriptions,omitempty"`
	ConversationInstructions    string            `json:"conversation_instructions,omitempty"`
	WorkingDir                  string            `json:"working_dir,omitempty"`
	// === KNOWLEDGE 分支字段 ===
	MicroagentKnowledge []MicroagentKnowledge `json:"microagent_knowledge,omitempty"`
}

// MicroagentKnowledge 触发的 microagent 载体。
// 移植自 openhands/events/observation/agent.py:47-59 MicroagentKnowledge。
// 注意：字段集与 internal/microagent.Knowledge、internal/promptassembly.MicroagentKnowledge
// 对齐（Name/Trigger/Content），改动需三方同步。三方独立定义以保持各包 leaf 无依赖。
type MicroagentKnowledge struct {
	Name    string `json:"name"`
	Trigger string `json:"trigger"`
	Content string `json:"content"`
}

func (o *RecallObservation) EventType() string { return "recall_observation" }
