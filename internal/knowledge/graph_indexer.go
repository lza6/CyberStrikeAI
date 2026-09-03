package knowledge

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// GraphIndexer 图索引器：协调 [GraphStore] + [GraphVectorIndex] + [GraphExtractor]，
// 对单个知识项执行「清旧→分块抽取→合并→写图→写向量」的增量更新流程（对齐 LightRAG 的增量 index pipeline）。
//
// 职责：
//   - 对单个 knowledge_base_items.id 执行 ExtractAndIndex（增量）；
//   - 批量 IndexMissing / RebuildIndex（复用 indexer 的分块与并发控制）；
//   - 暴露状态供 HTTP handler 查询。
type GraphIndexer struct {
	db        *sql.DB
	store     GraphStore
	vecIndex  *GraphVectorIndex
	extractor *GraphExtractor
	indexer   *Indexer // 复用其分块能力（chunkSize/overlap/Eino splitter）
	logger    *zap.Logger

	mu         sync.RWMutex
	isRunning  bool
	total      int
	current    int
	failed     int
	startTime  time.Time
	lastItemID string
	lastError  string
}

// NewGraphIndexer 构造；store/vecIndex/extractor 任一为 nil 返回错误。
// indexer 用于复用分块配置（nil 时使用默认分块）。
func NewGraphIndexer(
	db *sql.DB,
	store GraphStore,
	vecIndex *GraphVectorIndex,
	extractor *GraphExtractor,
	indexer *Indexer,
	logger *zap.Logger,
) (*GraphIndexer, error) {
	if db == nil {
		return nil, fmt.Errorf("graph indexer: db is nil")
	}
	if store == nil {
		return nil, fmt.Errorf("graph indexer: store is nil")
	}
	if vecIndex == nil {
		return nil, fmt.Errorf("graph indexer: vec index is nil")
	}
	if extractor == nil {
		return nil, fmt.Errorf("graph indexer: extractor is nil")
	}
	return &GraphIndexer{
		db: db, store: store, vecIndex: vecIndex, extractor: extractor, indexer: indexer, logger: logger,
	}, nil
}

// EnsureSchema 确保图存储与图向量索引表已建好（幂等）。
func (g *GraphIndexer) EnsureSchema(ctx context.Context) error {
	if g == nil {
		return fmt.Errorf("graph indexer: nil")
	}
	if err := g.store.Init(ctx); err != nil {
		return fmt.Errorf("graph store init: %w", err)
	}
	if err := EnsureGraphVectorSchema(g.db); err != nil {
		return fmt.Errorf("graph vector schema: %w", err)
	}
	return nil
}

// ExtractAndIndex 增量索引单个知识项：
//  1. 读取 knowledge_base_items.content；
//  2. 按 chunkSize/overlap 分块（复用 Indexer 分块逻辑或直接按 token 切分）；
//  3. 逐块抽取实体/关系；
//  4. RemoveByItem 清旧图数据（节点/边/向量）；
//  5. upsert 节点/边/向量；
//  6. 返回快照。
//
// 幂等：同一 itemID 重复调用结果一致（先清后写）。
func (g *GraphIndexer) ExtractAndIndex(ctx context.Context, itemID string) (*GraphSnapshot, error) {
	if g == nil {
		return nil, fmt.Errorf("graph indexer: nil")
	}
	if strings.TrimSpace(itemID) == "" {
		return nil, fmt.Errorf("itemID is empty")
	}

	var content, category, title, filePath string
	err := g.db.QueryRowContext(ctx,
		`SELECT content, category, title, file_path FROM knowledge_base_items WHERE id = ?`, itemID).
		Scan(&content, &category, &title, &filePath)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("知识项 %s 不存在", itemID)
	}
	if err != nil {
		return nil, fmt.Errorf("查询知识项 %s: %w", itemID, err)
	}

	body := strings.TrimSpace(content)
	if body == "" {
		return &GraphSnapshot{ItemID: itemID}, nil
	}

	// 分块：复用 Indexer 的分块能力（若可用），否则按 rune 切分
	chunks, err := g.splitChunks(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("分块失败: %w", err)
	}

	// 清旧：先清图存储（节点/边）+ 向量索引
	if err := g.store.RemoveByItem(ctx, itemID); err != nil {
		return nil, fmt.Errorf("清旧图数据失败: %w", err)
	}
	if err := g.vecIndex.RemoveByItem(ctx, itemID); err != nil {
		return nil, fmt.Errorf("清旧图向量失败: %w", err)
	}

	snap := &GraphSnapshot{ItemID: itemID, ChunkCount: len(chunks)}

	// 逐块抽取，合并同实体/关系
	nodeAgg := newEntityAggregator()
	edgeAgg := newRelationAggregator()

	for i, ch := range chunks {
		chunkID := fmt.Sprintf("%s#%d", itemID, i)
		ents, rels, eerr := g.extractor.Extract(ctx, itemID, chunkID, ch)
		if eerr != nil {
			if g.logger != nil {
				g.logger.Warn("图抽取失败，跳过该块",
					zap.String("itemId", itemID), zap.Int("chunk", i), zap.Error(eerr))
			}
			continue
		}
		for _, e := range ents {
			nodeAgg.add(e)
		}
		for _, r := range rels {
			edgeAgg.add(r)
		}
	}

	// 写图存储
	entities := nodeAgg.entities()
	relations := edgeAgg.relations()
	for _, e := range entities {
		if err := g.store.UpsertNode(ctx, e); err != nil {
			return nil, fmt.Errorf("写节点 %s: %w", e.Name, err)
		}
	}
	for _, r := range relations {
		if err := g.store.UpsertEdge(ctx, r); err != nil {
			return nil, fmt.Errorf("写边 %s→%s: %w", r.SrcID, r.TgtID, err)
		}
	}

	// 写向量索引
	for _, e := range entities {
		if err := g.vecIndex.UpsertEntity(ctx, e); err != nil {
			if g.logger != nil {
				g.logger.Warn("写实体向量失败", zap.String("name", e.Name), zap.Error(err))
			}
		}
	}
	for _, r := range relations {
		if err := g.vecIndex.UpsertRelation(ctx, r); err != nil {
			if g.logger != nil {
				g.logger.Warn("写关系向量失败", zap.String("src", r.SrcID), zap.String("tgt", r.TgtID), zap.Error(err))
			}
		}
	}

	snap.Entities = entities
	snap.Relations = relations

	if g.logger != nil {
		g.logger.Info("图增量索引完成",
			zap.String("itemId", itemID),
			zap.Int("chunks", len(chunks)),
			zap.Int("entities", len(snap.Entities)),
			zap.Int("relations", len(snap.Relations)),
		)
	}
	g.mu.Lock()
	g.lastItemID = itemID
	g.mu.Unlock()
	return snap, nil
}

// splitChunks 分块：优先复用 Indexer 的 Eino splitter；不可用时按 rune 切分兜底。
func (g *GraphIndexer) splitChunks(ctx context.Context, body string) ([]string, error) {
	if g.indexer != nil {
		// 复用 Indexer 的分块能力（通过其内部 splitter 重新切分）
		// 这里直接调 chunkTextByIndexer：它会用 Eino recursive splitter。
		return chunkTextByIndexer(ctx, g.indexer, body)
	}
	// 兜底：按 512 字符切分，50 重叠
	const chunkSize = 512
	const overlap = 50
	return chunkByRunes(body, chunkSize, overlap), nil
}

// chunkByRunes 按 rune 切分（兜底，无 Eino 依赖）。
func chunkByRunes(body string, size, overlap int) []string {
	runes := []rune(body)
	if len(runes) == 0 {
		return nil
	}
	if size <= 0 {
		size = 512
	}
	if overlap < 0 {
		overlap = 0
	}
	if overlap >= size {
		overlap = size / 4
	}
	var out []string
	for i := 0; i < len(runes); i += size - overlap {
		end := i + size
		if end > len(runes) {
			end = len(runes)
		}
		chunk := strings.TrimSpace(string(runes[i:end]))
		if chunk != "" {
			out = append(out, chunk)
		}
		if end == len(runes) {
			break
		}
	}
	return out
}

// chunkTextByIndexer 通过 Indexer 的 Eino splitter 切分文本（复用配置）。
// 若 Indexer 未初始化或失败，回退到 rune 切分。
func chunkTextByIndexer(ctx context.Context, idx *Indexer, body string) ([]string, error) {
	if idx == nil {
		return chunkByRunes(body, 512, 50), nil
	}
	// 使用 Indexer 内部 splitter 生成 chunk 文本列表
	docs, err := idx.SplitTextForGraph(ctx, body)
	if err != nil || len(docs) == 0 {
		return chunkByRunes(body, 512, 50), nil
	}
	return docs, nil
}

// IndexMissing 为尚无图向量的知识项构建图索引（冷启动/中断续跑）。
func (g *GraphIndexer) IndexMissing(ctx context.Context) error {
	if g == nil {
		return fmt.Errorf("graph indexer: nil")
	}
	if err := g.beginRun(); err != nil {
		return err
	}
	defer g.finishRun()

	rows, err := g.db.QueryContext(ctx, `
		SELECT i.id
		FROM knowledge_base_items i
		LEFT JOIN knowledge_graph_node_vectors v ON v.source_id = i.id
		WHERE v.source_id IS NULL
		ORDER BY i.updated_at ASC, i.id ASC
	`)
	if err != nil {
		return fmt.Errorf("查询未索引知识项失败: %w", err)
	}
	defer rows.Close()
	ids, err := scanStringColumn(rows)
	if err != nil {
		return fmt.Errorf("扫描知识项 ID 失败: %w", err)
	}
	g.setTotal(len(ids))
	return g.indexIDs(ctx, ids, "图增量索引补齐完成")
}

// RebuildIndex 全量重建所有知识项的图索引（显式 opt-in，成本更高）。
func (g *GraphIndexer) RebuildIndex(ctx context.Context) error {
	if g == nil {
		return fmt.Errorf("graph indexer: nil")
	}
	if err := g.beginRun(); err != nil {
		return err
	}
	defer g.finishRun()

	rows, err := g.db.QueryContext(ctx, `SELECT id FROM knowledge_base_items ORDER BY updated_at ASC, id ASC`)
	if err != nil {
		return fmt.Errorf("查询知识项失败: %w", err)
	}
	defer rows.Close()
	ids, err := scanStringColumn(rows)
	if err != nil {
		return fmt.Errorf("扫描知识项 ID 失败: %w", err)
	}
	g.setTotal(len(ids))
	return g.indexIDs(ctx, ids, "图增量索引重建完成")
}

func (g *GraphIndexer) beginRun() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.isRunning {
		return fmt.Errorf("图索引任务已在进行中")
	}
	g.isRunning = true
	g.total = 0
	g.current = 0
	g.failed = 0
	g.startTime = time.Now()
	g.lastItemID = ""
	g.lastError = ""
	return nil
}

func (g *GraphIndexer) finishRun() {
	g.mu.Lock()
	g.isRunning = false
	g.mu.Unlock()
}

func (g *GraphIndexer) setTotal(n int) {
	g.mu.Lock()
	g.total = n
	g.mu.Unlock()
}

func (g *GraphIndexer) indexIDs(ctx context.Context, ids []string, doneMsg string) error {
	failed := 0
	consecutive := 0
	maxConsecutive := 5
	var firstErr error
	for i, id := range ids {
		if _, err := g.ExtractAndIndex(ctx, id); err != nil {
			failed++
			consecutive++
			if consecutive == 1 {
				firstErr = err
				if g.logger != nil {
					g.logger.Warn("图索引知识项失败", zap.String("itemId", id), zap.Error(err))
				}
			}
			if consecutive >= maxConsecutive {
				g.mu.Lock()
				g.lastError = fmt.Sprintf("连续 %d 个知识项图索引失败：%v", consecutive, firstErr)
				g.mu.Unlock()
				return fmt.Errorf("连续图索引失败次数过多：%v", firstErr)
			}
		} else {
			consecutive = 0
		}
		g.mu.Lock()
		g.current = i + 1
		g.failed = failed
		g.mu.Unlock()
	}
	if g.logger != nil {
		g.logger.Info(doneMsg, zap.Int("total", len(ids)), zap.Int("failed", failed))
	}
	return nil
}

// GetStatus 返回运行状态。
func (g *GraphIndexer) GetStatus() (isRunning bool, total, current, failed int, startTime time.Time, lastItemID string) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.isRunning, g.total, g.current, g.failed, g.startTime, g.lastItemID
}

// scanStringColumn 扫描单列字符串。
func scanStringColumn(rows *sql.Rows) ([]string, error) {
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// --- 聚合器：合并同实体/关系的描述与 chunk_ids ---

type entityAggregator struct {
	byName map[string]*Entity
	order  []string
}

func newEntityAggregator() *entityAggregator {
	return &entityAggregator{byName: make(map[string]*Entity)}
}

func (a *entityAggregator) add(e *Entity) {
	if e == nil || strings.TrimSpace(e.Name) == "" {
		return
	}
	name := strings.TrimSpace(e.Name)
	if existing, ok := a.byName[name]; ok {
		existing.Description = splitDescription(existing.Description, e.Description)
		existing.ChunkIDs = mergeStringSlicesUnique(existing.ChunkIDs, e.ChunkIDs)
		if strings.TrimSpace(e.Type) != "" {
			existing.Type = strings.TrimSpace(e.Type)
		}
		existing.UpdatedAt = time.Now().UTC()
		return
	}
	clone := *e
	clone.Name = name
	a.byName[name] = &clone
	a.order = append(a.order, name)
}

func (a *entityAggregator) entities() []*Entity {
	out := make([]*Entity, 0, len(a.order))
	for _, n := range a.order {
		out = append(out, a.byName[n])
	}
	return out
}

type relationAggregator struct {
	byKey map[string]*Relation
	order []string
}

func newRelationAggregator() *relationAggregator {
	return &relationAggregator{byKey: make(map[string]*Relation)}
}

func (a *relationAggregator) add(r *Relation) {
	if r == nil || strings.TrimSpace(r.SrcID) == "" || strings.TrimSpace(r.TgtID) == "" {
		return
	}
	key := edgeKeyString(r.SrcID, r.TgtID)
	if existing, ok := a.byKey[key]; ok {
		existing.Description = splitDescription(existing.Description, r.Description)
		existing.ChunkIDs = mergeStringSlicesUnique(existing.ChunkIDs, r.ChunkIDs)
		existing.Weight += r.Weight
		if strings.TrimSpace(r.Keywords) != "" {
			existing.Keywords = strings.TrimSpace(r.Keywords)
		}
		existing.UpdatedAt = time.Now().UTC()
		return
	}
	clone := *r
	ek := edgeKey(r.SrcID, r.TgtID)
	clone.SrcID, clone.TgtID = ek[0], ek[1]
	a.byKey[key] = &clone
	a.order = append(a.order, key)
}

func (a *relationAggregator) relations() []*Relation {
	out := make([]*Relation, 0, len(a.order))
	// 按 weight 降序、再按 key 字典序
	keys := make([]string, len(a.order))
	copy(keys, a.order)
	sort.Slice(keys, func(i, j int) bool {
		ki, kj := a.byKey[keys[i]], a.byKey[keys[j]]
		if ki.Weight != kj.Weight {
			return ki.Weight > kj.Weight
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys {
		out = append(out, a.byKey[k])
	}
	return out
}
