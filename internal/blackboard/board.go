// Package blackboard 提供进程内的黑板（blackboard）模式实现，
// 用于多个 Agent / 工具之间共享渗透测试发现（findings）。
//
// 设计思想移植自 Pentest-Swarm-AI：Agent 把新发现 Publish 到黑板，
// 其他 Agent 通过 Subscribe 订阅增量；Supersede 用于在新发现取代旧发现时
// 标记旧 finding 已被取代（例如：误报被真实漏洞取代）。
//
// 本实现是进程内内存版（MemoryBoard），不依赖数据库；若需持久化或跨进程
// 共享，可后续实现一个 SQLiteBoard 满足同一接口。
package blackboard

import (
	"context"
	"time"
)

// Finding 黑板上的一条发现。
type Finding struct {
	// ID 黑板分配的唯一 ID（uuid）。Publish 时若留空则自动生成。
	ID string `json:"id"`
	// Type 发现类型：vuln / asset / cred / lateral 等。
	Type string `json:"type"`
	// Title 简短标题。
	Title string `json:"title"`
	// Detail 详细描述（可含 POC、证据）。
	Detail string `json:"detail"`
	// Severity 严重程度：info / low / medium / high / critical。
	Severity string `json:"severity"`
	// Source 来源（agent 名 / 工具名）。
	Source string `json:"source"`
	// CreatedAt 创建时间。
	CreatedAt time.Time `json:"created_at"`
	// SupersededBy 若非空，表示本 finding 已被该 ID 的新 finding 取代。
	SupersededBy string `json:"superseded_by,omitempty"`
	// ProjectID 所属项目（可选），用于按项目过滤。
	ProjectID string `json:"project_id,omitempty"`
}

// Board 是所有黑板实现必须满足的接口。Agent 只依赖此接口，
// 可自由替换内存版、SQLite 版或未来的分布式实现。
type Board interface {
	// Publish 追加一条 finding 到黑板，返回分配的 ID。
	Publish(ctx context.Context, finding Finding) (string, error)
	// Get 按 ID 获取单条 finding；不存在返回 (zero, false, nil)。
	Get(ctx context.Context, id string) (Finding, bool, error)
	// List 列出某项目下的所有 finding（按 CreatedAt 升序）。
	// projectID 为空时返回全部。
	List(ctx context.Context, projectID string) ([]Finding, error)
	// Subscribe 返回一个带缓冲的 channel，从 cursor 开始接收 finding。
	// 投递语义：at-least-once；订阅者用 cursor（已收到的最大 finding 序号）
	// 实现去重以达 exactly-once。重复订阅同一 cursor 会重复收到。
	// ctx 取消时关闭 channel。
	Subscribe(ctx context.Context, cursor int64) <-chan Finding
	// Supersede 标记 oldID 被 newFinding 取代，返回新 finding 的 ID。
	// oldID 不存在时返回错误。
	Supersede(ctx context.Context, oldID string, newFinding Finding) (string, error)
}
