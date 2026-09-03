package knowledge

import "context"

// GraphStore 图存储抽象后端（对齐 LightRAG BaseGraphStorage 子集）。
//
// 语义约定（所有实现必须遵守）：
//   - 节点主键为 Entity.Name（title case，跨文档保持一致）；
//   - 边主键为 (SrcID, TgtID) 有序对（实现内部可规范化为字典序比较）；
//   - 所有 upsert 幂等：重复写入合并 description、去重合并 chunk_ids、累加 weight；
//   - RemoveByItem 删除某知识项关联的全部节点与边（增量更新时旧数据清理）。
//
// 默认 SQLite 实现（与知识库共库）；memory 实现用于测试/轻量场景。
// Neo4j/Postgres 后端只需实现本接口即可替换，不影响上层抽取/检索逻辑。
type GraphStore interface {
	// Init 初始化存储（建表/迁移）；幂等。
	Init(ctx context.Context) error

	// UpsertNode 插入或合并节点（description 拼接、chunk_ids 去重合并）。
	UpsertNode(ctx context.Context, e *Entity) error
	// UpsertEdge 插入或合并边（description 拼接、weight 累加、chunk_ids 合并）。
	UpsertEdge(ctx context.Context, r *Relation) error

	// GetNode 按名取节点；不存在返回 (nil, nil)。
	GetNode(ctx context.Context, name string) (*Entity, error)
	// GetEdge 按 (src,tgt) 取边；不存在返回 (nil, nil)。
	GetEdge(ctx context.Context, src, tgt string) (*Relation, error)
	// GetNodeEdges 返回与 name 相邻的边端点对列表（含 src 与 tgt 两个方向）。
	GetNodeEdges(ctx context.Context, name string) ([][2]string, error)

	// HasNode / HasEdge 存在性检查。
	HasNode(ctx context.Context, name string) (bool, error)
	HasEdge(ctx context.Context, src, tgt string) (bool, error)

	// NodeDegree 返回节点度数（出度+入度）。
	NodeDegree(ctx context.Context, name string) (int, error)
	// NodeDegreesBatch 批量度数；返回 name→degree 映射，缺失节点度为 0。
	NodeDegreesBatch(ctx context.Context, names []string) (map[string]int, error)

	// GetNodesBatch 批量取节点；缺失节点在映射中缺省。
	GetNodesBatch(ctx context.Context, names []string) (map[string]*Entity, error)
	// GetEdgesBatch 批量取边；缺失边在映射中缺省。
	GetEdgesBatch(ctx context.Context, pairs [][2]string) (map[[2]string]*Relation, error)
	// GetNodeEdgesBatch 批量取邻接边端点；缺失节点映射中缺省。
	GetNodeEdgesBatch(ctx context.Context, names []string) (map[string][][2]string, error)

	// RemoveByItem 删除来源为 itemID 的全部节点与边（增量更新清理旧数据）。
	RemoveByItem(ctx context.Context, itemID string) error

	// IndexDoneCallback 持久化钩子（SQLite 实现为 no-op，每条 upsert 已即时落盘）。
	IndexDoneCallback(ctx context.Context) error
	// Drop 清空图存储全部数据（重置为初始状态）。
	Drop(ctx context.Context) error
	// Close 释放底层资源。
	Close() error

	// Backend 返回后端标识（"sqlite" | "memory" | "neo4j" | "postgres"），用于日志与诊断。
	Backend() string
}

// normalizeBackendName 归一化后端名（trim+lower，空回 sqlite）。
func normalizeBackendName(s string) string {
	switch s {
	case "":
		return "sqlite"
	default:
		return s
	}
}
