package knowledge

import (
	"context"
	"fmt"
	"strings"

	"cyberstrike-ai/internal/config"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// VectorEinoRetriever implements [retriever.Retriever] on top of SQLite-stored embeddings + cosine similarity.
// It returns prefetch-sized vector candidates only; rerank and post-process run in [knowledgePipelineRetriever].
type VectorEinoRetriever struct {
	inner *Retriever
	// degraded 标记该实例是否用于"pipeline 未装配"的退化路径。退化路径在 Retrieve
	// 末尾兜底 rerank（若已注入 DocumentReranker）；pipeline 内层路径（multiquery/vec
	// 变体检索）**不能**触发兜底，否则 rerank 会被每个查询变体各调一次，再叠加
	// pipeline.Retrieve 的精排，造成外部 rerank 调用量 N+1 倍放大与候选池被提前截断。
	degraded bool
}

// NewVectorEinoRetriever wraps r for Eino compose / tooling.
func NewVectorEinoRetriever(r *Retriever) *VectorEinoRetriever {
	if r == nil {
		return nil
	}
	return &VectorEinoRetriever{inner: r}
}

// NewDegradedVectorEinoRetriever 构造退化路径（pipeline 未装配）专用的裸向量适配器。
// 仅该模式启用末尾兜底 rerank；见 degraded 字段注释。
func NewDegradedVectorEinoRetriever(r *Retriever) *VectorEinoRetriever {
	v := NewVectorEinoRetriever(r)
	if v != nil {
		v.degraded = true
	}
	return v
}

// GetType identifies this retriever for Eino callbacks.
func (h *VectorEinoRetriever) GetType() string {
	return "SQLiteVectorKnowledgeRetriever"
}

// Retrieve runs vector search and returns [schema.Document] rows.
func (h *VectorEinoRetriever) Retrieve(ctx context.Context, query string, opts ...retriever.Option) (out []*schema.Document, err error) {
	if h == nil || h.inner == nil {
		return nil, fmt.Errorf("VectorEinoRetriever: nil retriever")
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, fmt.Errorf("查询不能为空")
	}

	ro := retriever.GetCommonOptions(nil, opts...)
	cfg := h.inner.config

	req := &SearchRequest{Query: q}

	if ro.TopK != nil && *ro.TopK > 0 {
		req.TopK = *ro.TopK
	} else if cfg != nil && cfg.TopK > 0 {
		req.TopK = cfg.TopK
	} else {
		req.TopK = 5
	}

	req.Threshold = 0
	if ro.DSLInfo != nil {
		if rt, ok := ro.DSLInfo[DSLRiskType].(string); ok {
			req.RiskType = strings.TrimSpace(rt)
		}
		if v, ok := ro.DSLInfo[DSLSimilarityThreshold]; ok {
			if f, ok2 := DSLNumeric(v); ok2 && f > 0 {
				req.Threshold = f
			}
		}
		if sf, ok := ro.DSLInfo[DSLSubIndexFilter].(string); ok {
			req.SubIndexFilter = strings.TrimSpace(sf)
		}
	}
	if req.SubIndexFilter == "" && cfg != nil && strings.TrimSpace(cfg.SubIndexFilter) != "" {
		req.SubIndexFilter = strings.TrimSpace(cfg.SubIndexFilter)
	}
	if req.Threshold <= 0 && cfg != nil && cfg.SimilarityThreshold > 0 {
		req.Threshold = cfg.SimilarityThreshold
	}
	if req.Threshold <= 0 {
		req.Threshold = 0.7
	}

	finalTopK := req.TopK
	var postPO *config.PostRetrieveConfig
	if cfg != nil {
		postPO = &cfg.PostRetrieve
	}
	fetchK := EffectivePrefetchTopK(finalTopK, postPO)
	searchReq := *req
	searchReq.TopK = fetchK

	ctx = callbacks.EnsureRunInfo(ctx, h.GetType(), components.ComponentOfRetriever)
	th := req.Threshold
	st := &th
	ctx = callbacks.OnStart(ctx, &retriever.CallbackInput{
		Query:          q,
		TopK:           finalTopK,
		ScoreThreshold: st,
		Extra:          ro.DSLInfo,
	})
	defer func() {
		if err != nil {
			_ = callbacks.OnError(ctx, err)
			return
		}
		_ = callbacks.OnEnd(ctx, &retriever.CallbackOutput{Docs: out})
	}()

	results, err := h.inner.vectorSearch(ctx, &searchReq)
	if err != nil {
		return nil, err
	}
	out = retrievalResultsToDocuments(results)

	// 退化路径兜底：仅当本实例标记为 degraded（activeEinoRetriever 在 pipeline==nil 时构造）
	// 且上层注入了 DocumentReranker 时，在此补一次精排。pipeline 内层路径（multiquery
	// 每个查询变体、或 vec 宽候选）不进入此处——pipeline.Retrieve 已统一 rerank，
	// 若在此重复触发，rerank 外部调用量会放大 N+1 倍且候选池被提前截断到 finalTopK。
	if h.degraded {
		if rr := h.inner.documentReranker(); rr != nil && len(out) > 1 {
			reranked, rerr := rr.Rerank(ctx, q, out)
			if rerr != nil {
				if h.inner.logger != nil {
					h.inner.logger.Warn("退化路径重排失败，已使用向量检索原序", zap.Error(rerr))
				}
			} else if len(reranked) > 0 {
				out = reranked
			}
		}
		if len(out) > finalTopK {
			out = out[:finalTopK]
		}
	}
	return out, nil
}

func retrievalResultsToDocuments(results []*RetrievalResult) []*schema.Document {
	out := make([]*schema.Document, 0, len(results))
	for _, res := range results {
		if res == nil || res.Chunk == nil || res.Item == nil {
			continue
		}
		d := &schema.Document{
			ID:      res.Chunk.ID,
			Content: res.Chunk.ChunkText,
			MetaData: map[string]any{
				metaKBItemID:     res.Item.ID,
				metaKBCategory:   res.Item.Category,
				metaKBTitle:      res.Item.Title,
				metaKBChunkIndex: res.Chunk.ChunkIndex,
				metaSimilarity:   res.Similarity,
			},
		}
		d.WithScore(res.Score)
		out = append(out, d)
	}
	return out
}

func documentsToRetrievalResults(docs []*schema.Document) ([]*RetrievalResult, error) {
	out := make([]*RetrievalResult, 0, len(docs))
	for i, d := range docs {
		if d == nil {
			continue
		}
		itemID, err := RequireMetaString(d.MetaData, metaKBItemID)
		if err != nil {
			return nil, fmt.Errorf("document %d: %w", i, err)
		}
		cat := MetaLookupString(d.MetaData, metaKBCategory)
		title := MetaLookupString(d.MetaData, metaKBTitle)
		chunkIdx, err := RequireMetaInt(d.MetaData, metaKBChunkIndex)
		if err != nil {
			return nil, fmt.Errorf("document %d: %w", i, err)
		}
		sim, _ := MetaFloat64OK(d.MetaData, metaSimilarity)
		item := &KnowledgeItem{ID: itemID, Category: cat, Title: title}
		chunk := &KnowledgeChunk{
			ID:         d.ID,
			ItemID:     itemID,
			ChunkIndex: chunkIdx,
			ChunkText:  d.Content,
		}
		out = append(out, &RetrievalResult{
			Chunk:      chunk,
			Item:       item,
			Similarity: sim,
			Score:      d.Score(),
		})
	}
	return out, nil
}

var _ retriever.Retriever = (*VectorEinoRetriever)(nil)
