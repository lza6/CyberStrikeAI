package pluginslot

// Notifier 推送通知接口（SlotNotifier）。移植自 agent-orchestrator Notifier（types.ts:857-868）。
// reactions 引擎的 notify 通道：executeReaction → notifyHuman → 遍历 Notifier.Notify。
//
// 设计：小 interface（1 必需方法），实现方可按需扩展 Post/NotifyWithActions（用类型断言）。
// 与参考项目对齐：notify(event) 是核心契约；参考项目还定义了可选 notifyWithActions/post，
// Go 版本用类型断言按需调用（类型在本文件定义，无独立 extras 文件）。
type Notifier interface {
	// Notify 推送一条通知事件。非阻塞语义：实现方应在内部异步发送，避免阻塞 reactions 引擎。
	// 返回 error 仅用于记日志，不影响 reactions 引擎对其他 notifier 的调用（容错）。
	Notify(event NotifyEvent) error
}

// NotifyEvent 通知事件。移植自 agent-orchestrator OrchestratorEvent（types.ts:924-933）。
// 字段对齐参考项目：id/type/priority/projectId/message/data，适配 CyberStrikeAI 安全事件语义。
type NotifyEvent struct {
	// ID 事件唯一 ID（reactions 引擎生成 uuid）。
	ID string
	// Type 事件类型（如 "high-impact-tool", "scope-violation"）。
	Type string
	// Priority 优先级（urgent/action/warning/info）。
	Priority string
	// ProjectID 所属项目（可选）。
	ProjectID string
	// Message 通知正文。
	Message string
	// Data 附加数据（工具名/会话 ID/风险等），实现方按需取用。
	Data map[string]interface{}
}

// NotifierWithActions 可选：支持带动作按钮的通知（Slack/钉钉等）。
// 移植自 agent-orchestrator Notifier.notifyWithActions（types.ts:860-862）。
// 实现方按需实现；reactions 引擎用类型断言探测：
//
//	if na, ok := n.(NotifierWithActions); ok { na.NotifyWithActions(ev, actions) }
type NotifierWithActions interface {
	Notifier
	NotifyWithActions(event NotifyEvent, actions []NotifyAction) error
}

// NotifyAction 通知动作按钮。
type NotifyAction struct {
	Label string
	URL   string
	Style string // primary/danger/default
}

// NotifierPoster 可选：支持向频道/群组直接发消息（区别于事件通知）。
// 移植自 agent-orchestrator Notifier.post（types.ts:864-866）。
type NotifierPoster interface {
	Notifier
	Post(message string, context map[string]interface{}) error
}
