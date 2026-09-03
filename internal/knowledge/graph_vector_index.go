package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/google/uuid"
)

// GraphVectorIndex 图向量索引（实体向量 + 关系向量）。
// 与 LightRAG entities_vdb + relationships_vdb 对齐：将 name/description 与
// keywords/description 嵌入后存表，检索时按余弦相似度 TopK 召回，再回图取节点/边细节。
//
// 存储表：
//   - knowledge_graph_node_vectors（name, embedding, source_id, chunk_ids, created_at）
//   - knowledge_graph_edge_vectors（src_name, tgt_name, embedding, source_id, chunk_ids, created_at）
//
// 该索引与 [SQLiteGraphStore] 共库，但独立维护——图存储是事实，向量是召回索引。
type GraphVectorIndex struct {
	db       *sql.DB
	embedder *Embedder
	logger   *zap.Logger
}

// NewGraphVectorIndex 构造；db 与 embedder 必须非 nil。
func NewGraphVectorIndex(db *sql.DB, embedder *Embedder, logger *zap.Logger) (*GraphVectorIndex, error) {
	if db == nil {
		return nil, fmt.Errorf("graph vector index: db is nil")
	}
	if embedder == nil {
		return nil, fmt.Errorf("graph vector index: embedder is nil")
	}
	return &GraphVectorIndex{db: db, embedder: embedder, logger: logger}, nil
}

// EnsureGraphVectorSchema 幂等建表。
func EnsureGraphVectorSchema(db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS knowledge_graph_node_vectors (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	embedding TEXT NOT NULL,
	embedding_model TEXT NOT NULL DEFAULT '',
	embedding_dim INTEGER NOT NULL DEFAULT 0,
	source_id TEXT NOT NULL DEFAULT '',
	chunk_ids TEXT NOT NULL DEFAULT '[]',
	created_at DATETIME NOT NULL
);
CREATE TABLE IF NOT EXISTS knowledge_graph_edge_vectors (
	id TEXT PRIMARY KEY,
	src_name TEXT NOT NULL,
	tgt_name TEXT NOT NULL,
	keywords TEXT NOT NULL DEFAULT '',
	embedding TEXT NOT NULL,
	embedding_model TEXT NOT NULL DEFAULT '',
	embedding_dim INTEGER NOT NULL DEFAULT 0,
	source_id TEXT NOT NULL DEFAULT '',
	chunk_ids TEXT NOT NULL DEFAULT '[]',
	created_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_kg_node_vec_name ON knowledge_graph_node_vectors(name);
CREATE INDEX IF NOT EXISTS idx_kg_node_vec_source ON knowledge_graph_node_vectors(source_id);
CREATE INDEX IF NOT EXISTS idx_kg_edge_vec_pair ON knowledge_graph_edge_vectors(src_name, tgt_name);
CREATE INDEX IF NOT EXISTS idx_kg_edge_vec_source ON knowledge_graph_edge_vectors(source_id);
`)
	return err
}

// UpsertEntity 嵌入实体描述并写入/更新向量行（按 name 幂等：先删后插）。
func (g *GraphVectorIndex) UpsertEntity(ctx context.Context, e *Entity) error {
	if g == nil || g.db == nil {
		return fmt.Errorf("graph vector index: nil")
	}
	if e == nil || strings.TrimSpace(e.Name) == "" {
		return fmt.Errorf("upsert entity vector: empty name")
	}
	if err := EnsureGraphVectorSchema(g.db); err != nil {
		return err
	}
	name := strings.TrimSpace(e.Name)
	// 先删旧向量行
	if _, err := g.db.ExecContext(ctx, `DELETE FROM knowledge_graph_node_vectors WHERE name = ?`, name); err != nil {
		return fmt.Errorf("delete old entity vector %q: %w", name, err)
	}

	text := formatEntityEmbeddingInput(e)
	vec, err := g.embedder.EmbedText(ctx, text)
	if err != nil {
		return fmt.Errorf("embed entity %q: %w", name, err)
	}
	vecJSON, err := json.Marshal(vec)
	if err != nil {
		return fmt.Errorf("marshal entity embedding: %w", err)
	}
	chunksJSON, _ := json.Marshal(e.ChunkIDs)
	id := uuid.New().String()
	model := g.embedder.EmbeddingModelName()
	_, err = g.db.ExecContext(ctx, `INSERT INTO knowledge_graph_node_vectors (id, name, embedding, embedding_model, embedding_dim, source_id, chunk_ids, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, name, string(vecJSON), model, len(vec), strings.TrimSpace(e.SourceID), string(chunksJSON), time.Now().UTC())
	return err
}

// UpsertRelation 嵌入关系 keywords+description 并写入/更新（按 src,tgt 幂等）。
func (g *GraphVectorIndex) UpsertRelation(ctx context.Context, r *Relation) error {
	if g == nil || g.db == nil {
		return fmt.Errorf("graph vector index: nil")
	}
	if r == nil || strings.TrimSpace(r.SrcID) == "" || strings.TrimSpace(r.TgtID) == "" {
		return fmt.Errorf("upsert relation vector: empty src/tgt")
	}
	if err := EnsureGraphVectorSchema(g.db); err != nil {
		return err
	}
	src := strings.TrimSpace(r.SrcID)
	tgt := strings.TrimSpace(r.TgtID)
	if _, err := g.db.ExecContext(ctx, `DELETE FROM knowledge_graph_edge_vectors WHERE src_name = ? AND tgt_name = ?`, src, tgt); err != nil {
		return fmt.Errorf("delete old relation vector (%s,%s): %w", src, tgt, err)
	}
	text := formatRelationEmbeddingInput(r)
	vec, err := g.embedder.EmbedText(ctx, text)
	if err != nil {
		return fmt.Errorf("embed relation (%s,%s): %w", src, tgt, err)
	}
	vecJSON, err := json.Marshal(vec)
	if err != nil {
		return fmt.Errorf("marshal relation embedding: %w", err)
	}
	chunksJSON, _ := json.Marshal(r.ChunkIDs)
	id := uuid.New().String()
	model := g.embedder.EmbeddingModelName()
	_, err = g.db.ExecContext(ctx, `INSERT INTO knowledge_graph_edge_vectors (id, src_name, tgt_name, keywords, embedding, embedding_model, embedding_dim, source_id, chunk_ids, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, src, tgt, strings.TrimSpace(r.Keywords), string(vecJSON), model, len(vec), strings.TrimSpace(r.SourceID), string(chunksJSON), time.Now().UTC())
	return err
}

// formatEntityEmbeddingInput 实体嵌入输入文本（与 LightRAG entity embedding 对齐）。
func formatEntityEmbeddingInput(e *Entity) string {
	return fmt.Sprintf("[类型：%s] [名称：%s]\n%s", strings.TrimSpace(e.Type), strings.TrimSpace(e.Name), strings.TrimSpace(e.Description))
}

// formatRelationEmbeddingInput 关系嵌入输入文本（keywords + description）。
func formatRelationEmbeddingInput(r *Relation) string {
	return fmt.Sprintf("[关键词：%s] [关系：%s→%s]\n%s", strings.TrimSpace(r.Keywords), strings.TrimSpace(r.SrcID), strings.TrimSpace(r.TgtID), strings.TrimSpace(r.Description))
}

// SearchEntities 向量检索实体：返回 TopK 命中（按余弦相似度降序）。
// 返回每个命中的 name 与 score；调用方据此回图取节点细节与邻边。
func (g *GraphVectorIndex) SearchEntities(ctx context.Context, query string, topK int, threshold float64) ([]GraphNodeHit, error) {
	if g == nil || g.db == nil {
		return nil, fmt.Errorf("graph vector index: nil")
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, fmt.Errorf("query is empty")
	}
	if topK <= 0 {
		topK = 5
	}
	if threshold <= 0 {
		threshold = 0.2 // 与 LightRAG cosine_better_than_threshold 默认一致
	}
	qVec, err := g.embedder.EmbedText(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	rows, err := g.db.QueryContext(ctx, `SELECT name, embedding, source_id, chunk_ids FROM knowledge_graph_node_vectors`)
	if err != nil {
		return nil, fmt.Errorf("query entity vectors: %w", err)
	}
	defer rows.Close()

	type cand struct {
		name   string
		score  float64
		source string
		chunks []string
	}
	cands := make([]cand, 0)
	for rows.Next() {
		var name, embJSON, source, chunksJSON string
		if err := rows.Scan(&name, &embJSON, &source, &chunksJSON); err != nil {
			continue
		}
		var emb []float32
		if err := json.Unmarshal([]byte(embJSON), &emb); err != nil {
			continue
		}
		if len(emb) != len(qVec) {
			continue
		}
		s := cosineSimilarity(qVec, emb)
		if s >= threshold {
			var ch []string
			if chunksJSON != "" {
				_ = json.Unmarshal([]byte(chunksJSON), &ch)
			}
			cands = append(cands, cand{name: name, score: s, source: source, chunks: ch})
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].score > cands[j].score })
	if len(cands) > topK {
		cands = cands[:topK]
	}
	out := make([]GraphNodeHit, 0, len(cands))
	for _, c := range cands {
		out = append(out, GraphNodeHit{Name: c.name, Score: c.score, SourceID: c.source, ChunkIDs: c.chunks})
	}
	return out, nil
}

// SearchRelations 向量检索关系：返回 TopK 命中（src_name, tgt_name + score）。
func (g *GraphVectorIndex) SearchRelations(ctx context.Context, keywords string, topK int, threshold float64) ([]GraphEdgeHit, error) {
	if g == nil || g.db == nil {
		return nil, fmt.Errorf("graph vector index: nil")
	}
	q := strings.TrimSpace(keywords)
	if q == "" {
		return nil, fmt.Errorf("keywords is empty")
	}
	if topK <= 0 {
		topK = 5
	}
	if threshold <= 0 {
		threshold = 0.2
	}
	qVec, err := g.embedder.EmbedText(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	rows, err := g.db.QueryContext(ctx, `SELECT src_name, tgt_name, keywords, embedding, source_id, chunk_ids FROM knowledge_graph_edge_vectors`)
	if err != nil {
		return nil, fmt.Errorf("query relation vectors: %w", err)
	}
	defer rows.Close()

	type cand struct {
		src, tgt, keywords, source string
		score                      float64
		chunks                     []string
	}
	cands := make([]cand, 0)
	for rows.Next() {
		var src, tgt, kw, embJSON, source, chunksJSON string
		if err := rows.Scan(&src, &tgt, &kw, &embJSON, &source, &chunksJSON); err != nil {
			continue
		}
		var emb []float32
		if err := json.Unmarshal([]byte(embJSON), &emb); err != nil {
			continue
		}
		if len(emb) != len(qVec) {
			continue
		}
		s := cosineSimilarity(qVec, emb)
		if s >= threshold {
			var ch []string
			if chunksJSON != "" {
				_ = json.Unmarshal([]byte(chunksJSON), &ch)
			}
			cands = append(cands, cand{src: src, tgt: tgt, keywords: kw, score: s, source: source, chunks: ch})
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].score > cands[j].score })
	if len(cands) > topK {
		cands = cands[:topK]
	}
	out := make([]GraphEdgeHit, 0, len(cands))
	for _, c := range cands {
		out = append(out, GraphEdgeHit{SrcName: c.src, TgtName: c.tgt, Score: c.score, Keywords: c.keywords, SourceID: c.source, ChunkIDs: c.chunks})
	}
	return out, nil
}

// RemoveByItem 删除某知识项关联的全部图向量行（实体与关系均清）。
func (g *GraphVectorIndex) RemoveByItem(ctx context.Context, itemID string) error {
	if g == nil || g.db == nil {
		return fmt.Errorf("graph vector index: nil")
	}
	itemID = strings.TrimSpace(itemID)
	if _, err := g.db.ExecContext(ctx, `DELETE FROM knowledge_graph_node_vectors WHERE source_id = ?`, itemID); err != nil {
		return err
	}
	if _, err := g.db.ExecContext(ctx, `DELETE FROM knowledge_graph_edge_vectors WHERE source_id = ?`, itemID); err != nil {
		return err
	}
	return nil
}

// GraphNodeHit 实体向量召回命中。
type GraphNodeHit struct {
	Name     string
	Score    float64
	SourceID string
	ChunkIDs []string
}

// GraphEdgeHit 关系向量召回命中。
type GraphEdgeHit struct {
	SrcName  string
	TgtName  string
	Keywords string
	Score    float64
	SourceID string
	ChunkIDs []string
}
