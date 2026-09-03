package knowledge

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
)

// GraphRetriever 双层图检索器（local + global + hybrid），对齐 LightRAG kg_query。
//
//   - local：实体向量召回 → 图节点 → 邻边 → 关联 chunks；
//   - global：关系向量召回 → 边 → 实体 → 关联 chunks；
//   - hybrid：local+global round-robin 合并去重。
//
// 依赖：
//   - [GraphVectorIndex]（实体/关系向量召回）
//   - [GraphStore]（节点/边/邻接查询）
//   - [Retriever]（回退到原向量检索补充关联 chunks，复用现有知识库向量）
type GraphRetriever struct {
	store     GraphStore
	vecIndex  *GraphVectorIndex
	chunkRetr *Retriever // 原知识库向量检索器（用于回退/补充 chunks）
	logger    *zap.Logger
}

// NewGraphRetriever 构造；store/vecIndex 必须非 nil；chunkRetr 可为 nil（global/local 仍可用，但关联 chunks 仅来自图元数据）。
func NewGraphRetriever(
	store GraphStore,
	vecIndex *GraphVectorIndex,
	chunkRetr *Retriever,
	logger *zap.Logger,
) (*GraphRetriever, error) {
	if store == nil {
		return nil, fmt.Errorf("graph retriever: store is nil")
	}
	if vecIndex == nil {
		return nil, fmt.Errorf("graph retriever: vec index is nil")
	}
	return &GraphRetriever{store: store, vecIndex: vecIndex, chunkRetr: chunkRetr, logger: logger}, nil
}

// Search 双层图检索。req.Mode 空 时默认 hybrid。
func (g *GraphRetriever) Search(ctx context.Context, req *GraphSearchRequest) (*GraphSearchResult, error) {
	if g == nil {
		return nil, fmt.Errorf("graph retriever: nil")
	}
	if req == nil || strings.TrimSpace(req.Query) == "" {
		return nil, fmt.Errorf("graph search: empty query")
	}
	mode := req.Mode
	if mode == "" {
		mode = GraphSearchHybrid
	}
	topK := req.TopK
	if topK <= 0 {
		topK = 5
	}

	switch mode {
	case GraphSearchLocal:
		return g.searchLocal(ctx, req, topK)
	case GraphSearchGlobal:
		return g.searchGlobal(ctx, req, topK)
	case GraphSearchHybrid:
		return g.searchHybrid(ctx, req, topK)
	default:
		return nil, fmt.Errorf("unsupported graph search mode: %s", mode)
	}
}

// searchLocal local 模式：实体向量召回 → 节点 → 邻边 → 关联 chunks（对齐 LightRAG _get_node_data）。
func (g *GraphRetriever) searchLocal(ctx context.Context, req *GraphSearchRequest, topK int) (*GraphSearchResult, error) {
	hits, err := g.vecIndex.SearchEntities(ctx, req.Query, topK, 0.2)
	if err != nil {
		return nil, fmt.Errorf("local: search entities: %w", err)
	}
	if len(hits) == 0 {
		return &GraphSearchResult{Mode: GraphSearchLocal}, nil
	}

	names := make([]string, 0, len(hits))
	for _, h := range hits {
		names = append(names, h.Name)
	}

	// 取节点 + 度数
	nodesMap, err := g.store.GetNodesBatch(ctx, names)
	if err != nil {
		return nil, fmt.Errorf("local: get nodes: %w", err)
	}
	degrees, _ := g.store.NodeDegreesBatch(ctx, names)
	_ = degrees

	// 构造命中实体（带 rank=degree）
	entities := make([]*Entity, 0, len(hits))
	for _, h := range hits {
		if n, ok := nodesMap[h.Name]; ok && n != nil {
			n.Description = splitDescription(n.Description, fmt.Sprintf("[相似度 %.3f]", h.Score))
			entities = append(entities, n)
		} else {
			entities = append(entities, &Entity{
				Name: h.Name, Type: "其他", Description: fmt.Sprintf("[相似度 %.3f]", h.Score),
				SourceID: h.SourceID, ChunkIDs: h.ChunkIDs, CreatedAt: time.Now(), UpdatedAt: time.Now(),
			})
		}
	}

	// 邻边（取每节点邻接端点对，去重后批量取边）
	edgesBatch, err := g.store.GetNodeEdgesBatch(ctx, names)
	if err != nil {
		return nil, fmt.Errorf("local: get node edges: %w", err)
	}
	edgePairs := dedupeEdgePairs(edgesBatch, names)
	edgesMap, _ := g.store.GetEdgesBatch(ctx, edgePairs)
	relations := make([]*Relation, 0, len(edgePairs))
	for _, p := range edgePairs {
		if r, ok := edgesMap[edgeKey(p[0], p[1])]; ok && r != nil {
			relations = append(relations, r)
		}
	}

	// 关联 chunks：从节点/边的 chunk_ids 反查
	chunks := g.collectChunks(ctx, req, entities, relations)
	maxScore := 0.0
	if len(hits) > 0 {
		maxScore = hits[0].Score
	}
	return &GraphSearchResult{Mode: GraphSearchLocal, Entities: entities, Relations: relations, Chunks: chunks, Score: maxScore}, nil
}

// searchGlobal global 模式：关系向量召回 → 边 → 实体 → 关联 chunks（对齐 LightRAG _get_edge_data）。
func (g *GraphRetriever) searchGlobal(ctx context.Context, req *GraphSearchRequest, topK int) (*GraphSearchResult, error) {
	hits, err := g.vecIndex.SearchRelations(ctx, req.Query, topK, 0.2)
	if err != nil {
		return nil, fmt.Errorf("global: search relations: %w", err)
	}
	if len(hits) == 0 {
		return &GraphSearchResult{Mode: GraphSearchGlobal}, nil
	}

	// 取边详情
	pairs := make([][2]string, 0, len(hits))
	for _, h := range hits {
		pairs = append(pairs, [2]string{h.SrcName, h.TgtName})
	}
	edgesMap, _ := g.store.GetEdgesBatch(ctx, pairs)
	relations := make([]*Relation, 0, len(hits))
	for _, h := range hits {
		key := edgeKey(h.SrcName, h.TgtName)
		if r, ok := edgesMap[key]; ok && r != nil {
			r.Description = splitDescription(r.Description, fmt.Sprintf("[相似度 %.3f]", h.Score))
			relations = append(relations, r)
		}
	}

	// 反查实体（边的两端）
	entityNames := make([]string, 0, len(hits)*2)
	for _, h := range hits {
		entityNames = append(entityNames, h.SrcName, h.TgtName)
	}
	entityNames = uniqueMatches(entityNames)
	nodesMap, _ := g.store.GetNodesBatch(ctx, entityNames)
	entities := make([]*Entity, 0, len(entityNames))
	for _, n := range entityNames {
		if e, ok := nodesMap[n]; ok && e != nil {
			entities = append(entities, e)
		}
	}

	chunks := g.collectChunks(ctx, req, entities, relations)
	maxScore := 0.0
	if len(hits) > 0 {
		maxScore = hits[0].Score
	}
	return &GraphSearchResult{Mode: GraphSearchGlobal, Entities: entities, Relations: relations, Chunks: chunks, Score: maxScore}, nil
}

// searchHybrid hybrid 模式：local + global 结果合并（round-robin 去重，对齐 LightRAG hybrid_query）。
func (g *GraphRetriever) searchHybrid(ctx context.Context, req *GraphSearchRequest, topK int) (*GraphSearchResult, error) {
	localRes, err := g.searchLocal(ctx, req, topK)
	if err != nil {
		return nil, fmt.Errorf("hybrid local: %w", err)
	}
	globalRes, err := g.searchGlobal(ctx, req, topK)
	if err != nil {
		return nil, fmt.Errorf("hybrid global: %w", err)
	}

	// round-robin 合并实体（按名去重）
	seenEntity := make(map[string]bool)
	mergedEntities := make([]*Entity, 0, len(localRes.Entities)+len(globalRes.Entities))
	maxLen := len(localRes.Entities)
	if len(globalRes.Entities) > maxLen {
		maxLen = len(globalRes.Entities)
	}
	for i := 0; i < maxLen; i++ {
		if i < len(localRes.Entities) && !seenEntity[localRes.Entities[i].Name] {
			seenEntity[localRes.Entities[i].Name] = true
			mergedEntities = append(mergedEntities, localRes.Entities[i])
		}
		if i < len(globalRes.Entities) && !seenEntity[globalRes.Entities[i].Name] {
			seenEntity[globalRes.Entities[i].Name] = true
			mergedEntities = append(mergedEntities, globalRes.Entities[i])
		}
	}

	// round-robin 合并关系（按 edgeKey 去重）
	seenEdge := make(map[string]bool)
	mergedRelations := make([]*Relation, 0, len(localRes.Relations)+len(globalRes.Relations))
	maxLen = len(localRes.Relations)
	if len(globalRes.Relations) > maxLen {
		maxLen = len(globalRes.Relations)
	}
	for i := 0; i < maxLen; i++ {
		if i < len(localRes.Relations) {
			k := edgeKeyString(localRes.Relations[i].SrcID, localRes.Relations[i].TgtID)
			if !seenEdge[k] {
				seenEdge[k] = true
				mergedRelations = append(mergedRelations, localRes.Relations[i])
			}
		}
		if i < len(globalRes.Relations) {
			k := edgeKeyString(globalRes.Relations[i].SrcID, globalRes.Relations[i].TgtID)
			if !seenEdge[k] {
				seenEdge[k] = true
				mergedRelations = append(mergedRelations, globalRes.Relations[i])
			}
		}
	}

	// chunks 合并去重
	mergedChunks := dedupeRetrievalResults(localRes.Chunks, globalRes.Chunks)

	score := localRes.Score
	if globalRes.Score > score {
		score = globalRes.Score
	}
	return &GraphSearchResult{Mode: GraphSearchHybrid, Entities: mergedEntities, Relations: mergedRelations, Chunks: mergedChunks, Score: score}, nil
}

// collectChunks 从实体/关系的 chunk_ids 反查关联 chunks。
// 若 chunkRetr 可用，则回退到原向量检索补充（按 query）。
func (g *GraphRetriever) collectChunks(ctx context.Context, req *GraphSearchRequest, entities []*Entity, relations []*Relation) []*RetrievalResult {
	// 收集 chunk_ids（实体 + 关系）
	idSet := make(map[string]struct{})
	for _, e := range entities {
		for _, c := range e.ChunkIDs {
			if c != "" {
				idSet[c] = struct{}{}
			}
		}
	}
	for _, r := range relations {
		for _, c := range r.ChunkIDs {
			if c != "" {
				idSet[c] = struct{}{}
			}
		}
	}

	// chunk_id 形如 "itemID#N"：解析后回查 chunk_text 与所属 item
	results := make([]*RetrievalResult, 0, len(idSet))
	for cid := range idSet {
		itemID, idx, ok := parseChunkID(cid)
		if !ok {
			continue
		}
		// 从 knowledge_embeddings 表查 chunk_text
		chunk, err := g.loadChunkByID(ctx, cid, itemID, idx)
		if err != nil || chunk == nil {
			continue
		}
		results = append(results, chunk)
	}

	// 若 chunkRetr 可用且 chunks 不足，回退向量检索补充（naive 模式语义）
	if g.chunkRetr != nil && len(results) < 5 && strings.TrimSpace(req.Query) != "" {
		supplement, err := g.chunkRetr.Search(ctx, &SearchRequest{
			Query: req.Query, RiskType: req.RiskType, TopK: 5,
		})
		if err == nil && len(supplement) > 0 {
			results = dedupeRetrievalResults(results, supplement)
		}
	}

	// 截断到 TopK
	if req.TopK > 0 && len(results) > req.TopK {
		results = results[:req.TopK]
	}
	return results
}

// loadChunkByID 从 knowledge_embeddings 表按 chunk ID 加载 chunk_text 与所属 item。
func (g *GraphRetriever) loadChunkByID(ctx context.Context, chunkID, itemID string, _ int) (*RetrievalResult, error) {
	if g.chunkRetr == nil || g.chunkRetr.db == nil {
		return nil, fmt.Errorf("no db")
	}
	var chunkText, category, title string
	err := g.chunkRetr.db.QueryRowContext(ctx,
		`SELECT e.chunk_text, i.category, i.title
		 FROM knowledge_embeddings e
		 JOIN knowledge_base_items i ON e.item_id = i.id
		 WHERE e.id = ?`, chunkID).
		Scan(&chunkText, &category, &title)
	if err != nil {
		return nil, err
	}
	return &RetrievalResult{
		Chunk: &KnowledgeChunk{ID: chunkID, ItemID: itemID, ChunkText: chunkText},
		Item:  &KnowledgeItem{ID: itemID, Category: category, Title: title},
	}, nil
}

// parseChunkID 解析 "itemID#N" 形式。
func parseChunkID(cid string) (itemID string, idx int, ok bool) {
	i := strings.LastIndex(cid, "#")
	if i <= 0 || i >= len(cid)-1 {
		return "", 0, false
	}
	itemID = cid[:i]
	rest := cid[i+1:]
	n := 0
	for _, r := range rest {
		if r < '0' || r > '9' {
			return "", 0, false
		}
		n = n*10 + int(r-'0')
	}
	return itemID, n, true
}

// dedupeEdgePairs 从邻接边端点对集合去重，仅保留与 names 相邻的对。
func dedupeEdgePairs(batch map[string][][2]string, names []string) [][2]string {
	nameSet := make(map[string]bool, len(names))
	for _, n := range names {
		nameSet[n] = true
	}
	seen := make(map[[2]string]bool)
	out := [][2]string{}
	for _, n := range names {
		pairs := batch[n]
		for _, p := range pairs {
			k := edgeKey(p[0], p[1])
			if seen[k] {
				continue
			}
			seen[k] = true
			// 仅保留两端至少一端在 names 中的边
			if nameSet[p[0]] || nameSet[p[1]] {
				out = append(out, p)
			}
		}
	}
	return out
}

// dedupeRetrievalResults 合并两份 RetrievalResult（按 Chunk.ID 去重，保序）。
func dedupeRetrievalResults(a, b []*RetrievalResult) []*RetrievalResult {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]*RetrievalResult, 0, len(a)+len(b))
	for _, r := range a {
		if r == nil || r.Chunk == nil {
			continue
		}
		if seen[r.Chunk.ID] {
			continue
		}
		seen[r.Chunk.ID] = true
		out = append(out, r)
	}
	for _, r := range b {
		if r == nil || r.Chunk == nil {
			continue
		}
		if seen[r.Chunk.ID] {
			continue
		}
		seen[r.Chunk.ID] = true
		out = append(out, r)
	}
	return out
}
