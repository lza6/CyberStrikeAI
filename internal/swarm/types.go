// Package swarm provides teammate execution backends for multi-agent swarms.
//
// 设计移植自参考项目 OpenHarness-main（Python）的 src/openharness/swarm/。
// 适配 CyberStrikeAI (Go)：
//
//   - 复用 internal/storage.HomeDir() 作为统一数据根（~/.cyberstrikeai/）。
//   - 进程内后端用 channel 投递消息；subprocess 后端用文件 mailbox（原子写 + flock）。
//   - 不内置 agent loop / prompt 解析（由 internal/multiagent 编排层负责）。
//   - 不内置权限同步（由 internal/permissions + internal/security/rbac 统一管）。
//   - PaneBackend（tmux/iterm2）保留接口，实现返回 ErrNotSupported（Web/Electron 形态无需 pane 可视化）。
package swarm

import (
	"context"
	"errors"
	"time"
)

// BackendType 标识 teammate 执行后端类型。
//
// 移植自 OpenHarness swarm/types.py:16 BackendType Literal。
type BackendType string

const (
	BackendSubprocess BackendType = "subprocess" // 子进程隔离（os/exec + 文件 mailbox）
	BackendInProcess   BackendType = "in_process" // 进程内（goroutine + channel）
	BackendTmux        BackendType = "tmux"        // tmux pane（保留，未实现）
	BackendITerm2      BackendType = "iterm2"      // iTerm2 pane（保留，未实现）
)

// ErrNotSupported 表示该后端在当前平台/配置下未实现。
var ErrNotSupported = errors.New("swarm: backend not supported on this platform")

// ErrAgentNotFound 表示指定 agent_id 不存在（未 spawn 或已 shutdown）。
var ErrAgentNotFound = errors.New("swarm: agent not found")

// TeammateIdentity 标识一个 teammate agent。
//
// 移植自 OpenHarness swarm/types.py:238 TeammateIdentity。
type TeammateIdentity struct {
	AgentID  string // 唯一 ID（格式：agentName@teamName）
	Name     string // agent 名（如 researcher）
	Team     string // team 名
	Color    string // UI 区分色（可选）
	ParentSessionID string // 父会话 ID（transcript 关联，可选）
}

// SpawnConfig 是 spawn 一个 teammate 的配置。
//
// 移植自 OpenHarness swarm/types.py:258 TeammateSpawnConfig，简化：
// 去掉 system_prompt/system_prompt_mode/plan_mode_required（由编排层管）。
type SpawnConfig struct {
	Name        string   // teammate 名
	Team        string   // team 名
	Prompt      string   // 初始 prompt/任务
	Cwd         string   // 工作目录
	ParentSessionID string // 父会话 ID
	Model       string   // 模型覆盖（可选）
	Color       string   // UI 色（可选）
	Permissions []string // 工具权限列表
	WorktreePath string   // git worktree 路径（可选，subprocess 后端用）
	SessionID   string   // 显式 session ID（空则生成）
}

// SpawnResult 是 spawn 的结果。
//
// 移植自 OpenHarness swarm/types.py:316 SpawnResult。
type SpawnResult struct {
	TaskID      string     // 任务管理器中的 task ID
	AgentID     string     // agent_id（agentName@teamName）
	BackendType BackendType // 使用的后端
	Success     bool       // 是否成功
	Error       string     // 失败原因（Success=false 时）
}

// Message 是发给 teammate 的消息。
//
// 移植自 OpenHarness swarm/types.py:336 TeammateMessage。
type Message struct {
	Text       string    // 消息正文
	FromAgent  string    // 发送方 agent_id
	Color      string    // 色（可选）
	Timestamp  time.Time // 时间戳
	Summary    string    // 摘要（可选）
}

// MailboxMessage 是 swarm agent 间交换的单条消息（文件 mailbox 用）。
//
// 移植自 OpenHarness swarm/mailbox.py:39 MailboxMessage。
type MailboxMessage struct {
	ID        string                 `json:"id"`
	Type      string                 `json:"type"`       // user_message/permission_request/permission_response/shutdown/idle_notification
	Sender    string                 `json:"sender"`
	Recipient string                 `json:"recipient"`
	Payload   map[string]interface{} `json:"payload"`
	Timestamp float64                `json:"timestamp"`
	Read      bool                   `json:"read"`
}

// Backend 抽象 teammate 执行后端（spawn/messaging/shutdown）。
//
// 移植自 OpenHarness swarm/types.py:352 TeammateExecutor Protocol。
// Go 改写：async → context + error；Protocol → interface。
type Backend interface {
	// Type 返回后端类型标识。
	Type() BackendType

	// IsAvailable 返回该后端在当前系统是否可用。
	IsAvailable(ctx context.Context) bool

	// Spawn 用给定配置 spawn 一个 teammate。
	Spawn(ctx context.Context, cfg SpawnConfig) (SpawnResult, error)

	// SendMessage 向运行中的 teammate 投递消息（进程内→channel；子进程→文件 mailbox）。
	SendMessage(ctx context.Context, agentID string, msg Message) error

	// Shutdown 终止 teammate。force=true 立即杀；false 优雅关闭。
	Shutdown(ctx context.Context, agentID string, force bool) (bool, error)
}
