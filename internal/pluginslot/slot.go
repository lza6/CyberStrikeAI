// Package pluginslot 提供可插拔槽位（PluginSlot）的 Go interface 与 Registry。
//
// 设计移植自参考项目 agent-orchestrator-main 的 8 PluginSlot 系统
// （packages/core/src/types.ts:1324 + plugin-registry.ts）。CyberStrikeAI 适配
// 为 6 个 slot（Runtime/Agent/Workspace/Notifier/Tool/Memory），Eino graph 保持
// 非可插拔（core lifecycle 固定）——只把"可替换的横切关注点"抽成 interface。
//
// 与 TS 版的区别：
//   - TS 用动态 import() 加载插件；Go 用 init() 注册工厂表（非动态加载）。
//   - TS 泛型 PluginModule<T>；Go 用 interface{} + 类型断言，每个 slot 有具体 Factory。
//   - TS 插件可 detect()；Go 的 Register 支持 detect 回调（binaryAvailable）。
//
// 使用方式：
//
//	func init() {
//	    pluginslot.Register(pluginslot.SlotNotifier, "desktop",
//	        func(cfg map[string]interface{}) pluginslot.Notifier { return &DesktopNotifier{cfg} },
//	        func() bool { _, err := exec.LookPath("notify-send"); return err == nil })
//	}
package pluginslot

// Slot 插件槽位类型。移植自 agent-orchestrator PluginSlot（TS），适配 CyberStrikeAI。
type Slot string

const (
	// SlotRuntime 执行运行时（Eino graph 非可插，此槽预留给未来非 Eino runtime）。
	SlotRuntime Slot = "runtime"
	// SlotAgent AI agent 工具（claude-code/glm 等，预留）。
	SlotAgent Slot = "agent"
	// SlotWorkspace 代码/工作区隔离（worktree/clone，预留）。
	SlotWorkspace Slot = "workspace"
	// SlotNotifier 推送通知（desktop/slack/webhook）——reactions 引擎的 notify 通道。
	SlotNotifier Slot = "notifier"
	// SlotTool 工具实现（破坏性工具的可插 provider，预留——现由 capability 包承担）。
	SlotTool Slot = "tool"
	// SlotMemory 记忆后端（blackboard memory/sqlite/future，预留）。
	SlotMemory Slot = "memory"
)

// Manifest 插件清单。移植自 agent-orchestrator PluginManifest（types.ts:1334-1349）。
type Manifest struct {
	// Name 插件名（如 "desktop", "slack", "webhook"），slot 内唯一。
	Name string
	// Slot 填充的槽位。
	Slot Slot
	// Description 人类可读描述。
	Description string
	// Version 版本号。
	Version string
	// DisplayName 展示名（如 "Desktop Notification"）。
	DisplayName string
}

// Factory 插件工厂。移植自 agent-orchestrator PluginModule.create（types.ts:1352-1357）。
// cfg 为用户配置（YAML plugins 段），返回插件实例（由调用方做类型断言到具体 slot interface）。
type Factory func(cfg map[string]interface{}) interface{}

// DetectFunc 可选：检测插件依赖的二进制是否可用。移植自 PluginModule.detect。
type DetectFunc func() bool

// entry Registry 内部条目。
type entry struct {
	manifest Manifest
	factory  Factory
	detect   DetectFunc
}

// registry / Registry 实现见 registry.go（registerEntry/Get/List/DetectAvailable）。
// 每个槽位的具体接口（Notifier/Workspace 等）在对应 *_notifier.go / workspace.go
// 定义为独立小 interface，实现方按需实现，app.go 类型断言注入。
