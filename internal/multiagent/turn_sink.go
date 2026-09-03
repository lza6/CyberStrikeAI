package multiagent

// TurnSink turn 事件汇接口：定义 turn 生命周期的回调点。
// 当前 Web handler 直接消费 SSE 流（handler/multi_agent.go 等），
// 无 CLI/桌面独立 turn 实现（J16 审计结论：无重复可消）。
// 本接口作为未来扩展点保留——若新增 CLI/批处理 turn 消费方，
// 实现 TurnSink 即可复用 runEinoADKAgentLoop 的全部中间件链。
type TurnSink interface {
	// OnDelta 文本增量（assistant 回复流）。
	OnDelta(text string)
	// OnToolCall 工具调用开始。
	OnToolCall(toolName, callID string, args map[string]interface{})
	// OnToolResult 工具结果。
	OnToolResult(toolName string, output string)
	// OnTurnDone turn 结束（含统计）。
	OnTurnDone(summary TurnSummary)
}

// TurnSummary turn 结束摘要。
type TurnSummary struct {
	ConversationID string
	Turns          int
	ToolCalls      int
	TokensPrompt   int
	TokensCompletion int
	DurationMS     int64
}

// noopTurnSink 空实现（丢弃全部事件），供不需要消费事件的调用方使用。
type noopTurnSink struct{}

func (noopTurnSink) OnDelta(string)                              {}
func (noopTurnSink) OnToolCall(string, string, map[string]interface{}) {}
func (noopTurnSink) OnToolResult(string, string)                 {}
func (noopTurnSink) OnTurnDone(TurnSummary)                      {}

// NewNoopTurnSink 返回空 TurnSink。
func NewNoopTurnSink() TurnSink { return noopTurnSink{} }
