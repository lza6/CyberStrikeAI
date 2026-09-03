package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// SQLiteGraphStore 在 knowledge_graph_nodes / knowledge_graph_edges 上实现 [GraphStore]。
// 默认后端，与知识库共库（同 *sql.DB），便于零依赖部署。
type SQLiteGraphStore struct {
	db    *sql.DB
	mu    sync.RWMutex
	table string // nodes 表名（edges 表名据此推导）
}

// NewSQLiteGraphStore 构造；db 必须非 nil（与知识库共享同一个 *sql.DB）。
func NewSQLiteGraphStore(db *sql.DB) *SQLiteGraphStore {
	return &SQLiteGraphStore{db: db, table: "knowledge_graph_nodes"}
}

// EnsureKnowledgeGraphSchema 幂等建表：节点与边。
// 节点表：name 为主键；source_id 关联 knowledge_base_items.id；chunk_ids JSON 数组。
// 边表：(src_name, tgt_name) 为主键；source_id 关联知识项；weight 实数；chunk_ids JSON 数组。
func EnsureKnowledgeGraphSchema(db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS knowledge_graph_nodes (
	name TEXT PRIMARY KEY,
	entity_type TEXT NOT NULL DEFAULT '',
	description TEXT NOT NULL DEFAULT '',
	source_id TEXT NOT NULL DEFAULT '',
	chunk_ids TEXT NOT NULL DEFAULT '[]',
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL
);
CREATE TABLE IF NOT EXISTS knowledge_graph_edges (
	id TEXT PRIMARY KEY,
	src_name TEXT NOT NULL,
	tgt_name TEXT NOT NULL,
	keywords TEXT NOT NULL DEFAULT '',
	description TEXT NOT NULL DEFAULT '',
	weight REAL NOT NULL DEFAULT 0,
	source_id TEXT NOT NULL DEFAULT '',
	chunk_ids TEXT NOT NULL DEFAULT '[]',
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_knowledge_graph_edges_src_tgt ON knowledge_graph_edges(src_name, tgt_name);
CREATE INDEX IF NOT EXISTS idx_knowledge_graph_edges_src ON knowledge_graph_edges(src_name);
CREATE INDEX IF NOT EXISTS idx_knowledge_graph_edges_tgt ON knowledge_graph_edges(tgt_name);
CREATE INDEX IF NOT EXISTS idx_knowledge_graph_nodes_source ON knowledge_graph_nodes(source_id);
CREATE INDEX IF NOT EXISTS idx_knowledge_graph_edges_source ON knowledge_graph_edges(source_id);
`)
	return err
}

func (s *SQLiteGraphStore) Backend() string { return "sqlite" }

func (s *SQLiteGraphStore) Init(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("sqlite graph store: nil db")
	}
	return EnsureKnowledgeGraphSchema(s.db)
}

// IndexDoneCallback SQLite 每条 upsert 即时落盘；no-op。
func (s *SQLiteGraphStore) IndexDoneCallback(ctx context.Context) error { return nil }

func (s *SQLiteGraphStore) Close() error { return nil }

func (s *SQLiteGraphStore) Drop(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("sqlite graph store: nil db")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM knowledge_graph_edges; DELETE FROM knowledge_graph_nodes;`)
	return err
}

// mergeChunkIDs 将已有 JSON 数组与新 ID 合并去重，返回新 JSON 字符串。
func mergeChunkIDs(existingJSON string, add []string) (string, error) {
	var cur []string
	if strings.TrimSpace(existingJSON) != "" {
		if err := json.Unmarshal([]byte(existingJSON), &cur); err != nil {
			// 容错：按空处理
			cur = nil
		}
	}
	seen := make(map[string]struct{}, len(cur)+len(add))
	out := make([]string, 0, len(cur)+len(add))
	for _, id := range cur {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	for _, id := range add {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// splitDescription 拼接两段描述（以 "\n\n" 分隔），跳过重复片段。
func splitDescription(a, b string) string {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	if a == b {
		return a
	}
	return a + "\n\n" + b
}

func (s *SQLiteGraphStore) UpsertNode(ctx context.Context, e *Entity) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("sqlite graph store: nil db")
	}
	if e == nil || strings.TrimSpace(e.Name) == "" {
		return fmt.Errorf("upsert node: empty name")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	name := strings.TrimSpace(e.Name)
	now := time.Now().UTC()
	var existingDesc, existingChunks string
	var createdAt time.Time
	row := s.db.QueryRowContext(ctx, `SELECT description, chunk_ids, created_at FROM knowledge_graph_nodes WHERE name = ?`, name)
	err := row.Scan(&existingDesc, &existingChunks, &createdAt)
	if err == sql.ErrNoRows {
		chunks, _ := mergeChunkIDs("[]", e.ChunkIDs)
		_, err = s.db.ExecContext(ctx, `INSERT INTO knowledge_graph_nodes (name, entity_type, description, source_id, chunk_ids, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			name, strings.TrimSpace(e.Type), strings.TrimSpace(e.Description), strings.TrimSpace(e.SourceID), chunks, now, now)
		return err
	}
	if err != nil {
		return fmt.Errorf("query node %q: %w", name, err)
	}
	mergedDesc := splitDescription(existingDesc, strings.TrimSpace(e.Description))
	mergedChunks, err := mergeChunkIDs(existingChunks, e.ChunkIDs)
	if err != nil {
		return err
	}
	sourceID := strings.TrimSpace(e.SourceID)
	if sourceID == "" {
		// 保留旧值：取现有 source_id
		var old string
		if e2 := s.db.QueryRowContext(ctx, `SELECT source_id FROM knowledge_graph_nodes WHERE name = ?`, name).Scan(&old); e2 == nil {
			sourceID = old
		}
	}
	_, err = s.db.ExecContext(ctx, `UPDATE knowledge_graph_nodes SET entity_type = ?, description = ?, source_id = ?, chunk_ids = ?, updated_at = ? WHERE name = ?`,
		strings.TrimSpace(e.Type), mergedDesc, sourceID, mergedChunks, now, name)
	return err
}

// edgeKey 规范化为 (min, max) 字典序，保证无向比较一致。
func edgeKey(a, b string) [2]string {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a <= b {
		return [2]string{a, b}
	}
	return [2]string{b, a}
}

// edgeKeyString 返回 edgeKey 的字符串形式（用于以字符串为键的 map）。
func edgeKeyString(a, b string) string {
	k := edgeKey(a, b)
	return k[0] + "\x00" + k[1]
}

func (s *SQLiteGraphStore) UpsertEdge(ctx context.Context, r *Relation) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("sqlite graph store: nil db")
	}
	if r == nil || strings.TrimSpace(r.SrcID) == "" || strings.TrimSpace(r.TgtID) == "" {
		return fmt.Errorf("upsert edge: empty src/tgt")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	src := strings.TrimSpace(r.SrcID)
	tgt := strings.TrimSpace(r.TgtID)
	now := time.Now().UTC()

	var existingDesc, existingChunks string
	var existingWeight float64
	row := s.db.QueryRowContext(ctx, `SELECT description, chunk_ids, weight FROM knowledge_graph_edges WHERE src_name = ? AND tgt_name = ?`, src, tgt)
	err := row.Scan(&existingDesc, &existingChunks, &existingWeight)
	if err == sql.ErrNoRows {
		id := uuid.New().String()
		chunks, _ := mergeChunkIDs("[]", r.ChunkIDs)
		_, err = s.db.ExecContext(ctx, `INSERT INTO knowledge_graph_edges (id, src_name, tgt_name, keywords, description, weight, source_id, chunk_ids, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, src, tgt, strings.TrimSpace(r.Keywords), strings.TrimSpace(r.Description), r.Weight, strings.TrimSpace(r.SourceID), chunks, now, now)
		return err
	}
	if err != nil {
		return fmt.Errorf("query edge (%s,%s): %w", src, tgt, err)
	}
	mergedDesc := splitDescription(existingDesc, strings.TrimSpace(r.Description))
	mergedChunks, mErr := mergeChunkIDs(existingChunks, r.ChunkIDs)
	if mErr != nil {
		return mErr
	}
	weight := existingWeight + r.Weight
	if weight < 0 {
		weight = 0
	}
	_, err = s.db.ExecContext(ctx, `UPDATE knowledge_graph_edges SET keywords = ?, description = ?, weight = ?, source_id = ?, chunk_ids = ?, updated_at = ? WHERE src_name = ? AND tgt_name = ?`,
		strings.TrimSpace(r.Keywords), mergedDesc, weight, strings.TrimSpace(r.SourceID), mergedChunks, now, src, tgt)
	return err
}

func (s *SQLiteGraphStore) GetNode(ctx context.Context, name string) (*Entity, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("sqlite graph store: nil db")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	name = strings.TrimSpace(name)
	var e Entity
	var desc, chunks, createdAt, updatedAt string
	err := s.db.QueryRowContext(ctx, `SELECT name, entity_type, description, source_id, chunk_ids, created_at, updated_at FROM knowledge_graph_nodes WHERE name = ?`, name).
		Scan(&e.Name, &e.Type, &desc, &e.SourceID, &chunks, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get node %q: %w", name, err)
	}
	e.Description = desc
	if chunks != "" {
		_ = json.Unmarshal([]byte(chunks), &e.ChunkIDs)
	}
	e.CreatedAt = parseGraphTime(createdAt)
	e.UpdatedAt = parseGraphTime(updatedAt)
	return &e, nil
}

func (s *SQLiteGraphStore) GetEdge(ctx context.Context, src, tgt string) (*Relation, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("sqlite graph store: nil db")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	src = strings.TrimSpace(src)
	tgt = strings.TrimSpace(tgt)
	var r Relation
	var desc, chunks, createdAt, updatedAt string
	err := s.db.QueryRowContext(ctx, `SELECT id, src_name, tgt_name, keywords, description, weight, source_id, chunk_ids, created_at, updated_at FROM knowledge_graph_edges WHERE src_name = ? AND tgt_name = ?`, src, tgt).
		Scan(&r.ID, &r.SrcID, &r.TgtID, &r.Keywords, &desc, &r.Weight, &r.SourceID, &chunks, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get edge (%s,%s): %w", src, tgt, err)
	}
	r.Description = desc
	if chunks != "" {
		_ = json.Unmarshal([]byte(chunks), &r.ChunkIDs)
	}
	r.CreatedAt = parseGraphTime(createdAt)
	r.UpdatedAt = parseGraphTime(updatedAt)
	return &r, nil
}

func (s *SQLiteGraphStore) GetNodeEdges(ctx context.Context, name string) ([][2]string, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("sqlite graph store: nil db")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	name = strings.TrimSpace(name)
	rows, err := s.db.QueryContext(ctx, `SELECT src_name, tgt_name FROM knowledge_graph_edges WHERE src_name = ? OR tgt_name = ?`, name, name)
	if err != nil {
		return nil, fmt.Errorf("get node edges %q: %w", name, err)
	}
	defer rows.Close()
	out := [][2]string{}
	for rows.Next() {
		var a, b string
		if err := rows.Scan(&a, &b); err != nil {
			return nil, err
		}
		out = append(out, [2]string{a, b})
	}
	return out, rows.Err()
}

func (s *SQLiteGraphStore) HasNode(ctx context.Context, name string) (bool, error) {
	n, err := s.GetNode(ctx, name)
	return n != nil, err
}

func (s *SQLiteGraphStore) HasEdge(ctx context.Context, src, tgt string) (bool, error) {
	e, err := s.GetEdge(ctx, src, tgt)
	return e != nil, err
}

func (s *SQLiteGraphStore) NodeDegree(ctx context.Context, name string) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("sqlite graph store: nil db")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	name = strings.TrimSpace(name)
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM knowledge_graph_edges WHERE src_name = ? OR tgt_name = ?`, name, name).Scan(&n)
	return n, err
}

func (s *SQLiteGraphStore) NodeDegreesBatch(ctx context.Context, names []string) (map[string]int, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("sqlite graph store: nil db")
	}
	out := make(map[string]int, len(names))
	if len(names) == 0 {
		return out, nil
	}
	// SQLite 参数数量上限较大；按 src 与 tgt 两次 IN 查询，合并到同一 map。
	placeholders := strings.Repeat("?,", len(names))
	placeholders = placeholders[:len(placeholders)-1]
	args := strSliceToIfaces(names)

	// src 方向度数
	qSrc := fmt.Sprintf(`SELECT src_name, COUNT(*) FROM knowledge_graph_edges WHERE src_name IN (%s) GROUP BY src_name`, placeholders)
	if rows, err := s.db.QueryContext(ctx, qSrc, args...); err == nil {
		func() {
			defer rows.Close()
			for rows.Next() {
				var n string
				var c int
				if e := rows.Scan(&n, &c); e == nil {
					out[n] += c
				}
			}
		}()
	}

	// tgt 方向度数（累加，得到总度数）
	qTgt := fmt.Sprintf(`SELECT tgt_name, COUNT(*) FROM knowledge_graph_edges WHERE tgt_name IN (%s) GROUP BY tgt_name`, placeholders)
	if rows, err := s.db.QueryContext(ctx, qTgt, args...); err == nil {
		func() {
			defer rows.Close()
			for rows.Next() {
				var n string
				var c int
				if e := rows.Scan(&n, &c); e == nil {
					out[n] += c
				}
			}
		}()
	}
	return out, nil
}

// strSliceToIfaces 将 string 切片转 interface{} 切片。
func strSliceToIfaces(s []string) []interface{} {
	out := make([]interface{}, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

func (s *SQLiteGraphStore) GetNodesBatch(ctx context.Context, names []string) (map[string]*Entity, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("sqlite graph store: nil db")
	}
	out := make(map[string]*Entity, len(names))
	for _, n := range names {
		e, err := s.GetNode(ctx, n)
		if err != nil {
			return nil, err
		}
		if e != nil {
			out[n] = e
		}
	}
	return out, nil
}

func (s *SQLiteGraphStore) GetEdgesBatch(ctx context.Context, pairs [][2]string) (map[[2]string]*Relation, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("sqlite graph store: nil db")
	}
	out := make(map[[2]string]*Relation, len(pairs))
	for _, p := range pairs {
		r, err := s.GetEdge(ctx, p[0], p[1])
		if err != nil {
			return nil, err
		}
		if r != nil {
			out[edgeKey(p[0], p[1])] = r
		}
	}
	return out, nil
}

func (s *SQLiteGraphStore) GetNodeEdgesBatch(ctx context.Context, names []string) (map[string][][2]string, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("sqlite graph store: nil db")
	}
	out := make(map[string][][2]string, len(names))
	for _, n := range names {
		edges, err := s.GetNodeEdges(ctx, n)
		if err != nil {
			return nil, err
		}
		out[n] = edges
	}
	return out, nil
}

func (s *SQLiteGraphStore) RemoveByItem(ctx context.Context, itemID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("sqlite graph store: nil db")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	itemID = strings.TrimSpace(itemID)
	_, err := s.db.ExecContext(ctx, `DELETE FROM knowledge_graph_edges WHERE source_id = ?`, itemID)
	if err != nil {
		return fmt.Errorf("remove edges by item %q: %w", itemID, err)
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM knowledge_graph_nodes WHERE source_id = ?`, itemID)
	if err != nil {
		return fmt.Errorf("remove nodes by item %q: %w", itemID, err)
	}
	return nil
}

// parseGraphTime 宽松解析 SQLite DATETIME 文本。
func parseGraphTime(s string) time.Time {
	if strings.TrimSpace(s) == "" {
		return time.Time{}
	}
	formats := []string{
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02T15:04:05.999999999Z07:00",
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05",
		time.RFC3339,
		time.RFC3339Nano,
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil && !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}

// sortedUniqueStrings 去重并排序（用于测试断言稳定）。
func sortedUniqueStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

var _ GraphStore = (*SQLiteGraphStore)(nil)
