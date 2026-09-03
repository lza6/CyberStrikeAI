package knowledge

import (
	"strings"
	"time"
)

// 图存储元数据键（写入 schema.Document.MetaData，供 Eino 链路识别图向量来源）。
const (
	metaGraphNodeID   = "kg_node_id"
	metaGraphNodeName = "kg_node_name"
	metaGraphItemType = "kg_item_type" // "entity" | "relation"
)

// GraphEntityType 图存储支持的实体类型分类（与 LightRAG entity_types 对齐）。
// 由配置 KnowledgeConfig.Graph.EntityTypes 注入；空则用默认安全领域类型。
var defaultGraphEntityTypes = []string{
	"CVE", "漏洞", "攻击技术", "检测方法", "防御措施", "资产", "目标", "工具", "其他",
}

// Entity 图节点（与 LightRAG kg node 对齐：name+type+description+source_id）。
type Entity struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`        // 实体名（title case，跨文档保持一致）
	Type        string    `json:"type"`        // 实体类型（CVE/漏洞/攻击技术…）
	Description string    `json:"description"` // 描述（综合各 chunk）
	SourceID    string    `json:"sourceId"`    // 来源知识项 ID（knowledge_base_items.id）
	ChunkIDs    []string  `json:"chunkIds"`    // 来源 chunk ID 列表
	Embedding   []float32 `json:"-"`           // name+description 的向量（不序列化）
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// Relation 图边（与 LightRAG kg edge 对齐：src+tgt+keywords+description+weight）。
type Relation struct {
	ID          string    `json:"id"`
	SrcID       string    `json:"srcId"`       // 源实体名
	TgtID       string    `json:"tgtId"`       // 目标实体名
	Keywords    string    `json:"keywords"`    // 高层关键词（逗号分隔）
	Description string    `json:"description"` // 关系描述
	Weight      float64   `json:"weight"`      // 权重（默认 1.0，可按共现次数累加）
	SourceID    string    `json:"sourceId"`    // 来源知识项 ID
	ChunkIDs    []string  `json:"chunkIds"`    // 来源 chunk ID 列表
	Embedding   []float32 `json:"-"`           // keywords+description 的向量（不序列化）
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// GraphSnapshot 增量更新快照：一次抽取产出的实体/关系集合。
// ExtractAndIndex 返回此结构，供调用方记录日志与调试。
type GraphSnapshot struct {
	ItemID     string      `json:"itemId"`
	Entities   []*Entity   `json:"entities"`
	Relations  []*Relation `json:"relations"`
	ChunkCount int         `json:"chunkCount"`
}

// GraphSearchMode 双层检索模式（与 LightRAG QueryParam.mode 对齐子集）。
type GraphSearchMode string

const (
	GraphSearchLocal  GraphSearchMode = "local"  // 实体向量→节点→邻边→chunks
	GraphSearchGlobal GraphSearchMode = "global" // 关系向量→边→实体→chunks
	GraphSearchHybrid GraphSearchMode = "hybrid" // local+global round-robin 合并
)

// String 返回模式字符串（实现 fmt.Stringer，便于日志/JSON）。
func (m GraphSearchMode) String() string { return string(m) }

// GraphSearchRequest 图检索请求。
type GraphSearchRequest struct {
	Query    string          `json:"query"`
	RiskType string          `json:"riskType,omitempty"`
	Mode     GraphSearchMode `json:"mode"`
	TopK     int             `json:"topK,omitempty"`
}

// GraphSearchResult 图检索结果（含命中实体/关系 + 关联 chunks）。
type GraphSearchResult struct {
	Mode      GraphSearchMode    `json:"mode"`
	Entities  []*Entity          `json:"entities"`
	Relations []*Relation        `json:"relations"`
	Chunks    []*RetrievalResult `json:"chunks"` // 关联的原始知识 chunks
	Score     float64            `json:"score"`  // 综合最高分
}

// normalizeGraphEntityTypes 返回非空类型列表；空则回退默认。
func normalizeGraphEntityTypes(types []string) []string {
	out := make([]string, 0, len(types))
	for _, t := range types {
		if s := strings.TrimSpace(t); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return defaultGraphEntityTypes
	}
	return out
}
