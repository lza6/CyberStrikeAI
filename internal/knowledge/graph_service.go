package knowledge

import (
	"context"
	"database/sql"
	"fmt"

	"cyberstrike-ai/internal/config"

	"go.uber.org/zap"
)

// GraphService 聚合图存储/向量索引/抽取器/索引器/检索器，对齐 LightRAG 的 LightRAG 类门面。
// 由 app.go 在知识库初始化阶段构造，注入到 KnowledgeHandler 与 MCP 工具。
//
// 生命周期：
//   - NewGraphService 构造 + InitSchema 建表；
//   - IndexItem 增量索引单个知识项（与 Indexer.IndexItem 对接，在知识项更新后调用）；
//   - Search 双层检索（local/global/hybrid）；
//   - GetStatus 查询索引进度。
type GraphService struct {
	store     GraphStore
	vecIndex  *GraphVectorIndex
	extractor *GraphExtractor
	indexer   *GraphIndexer
	retriever *GraphRetriever
	cfg       config.GraphConfig
	logger    *zap.Logger
}

// NewGraphService 构造图服务（含 store/vecIndex/extractor/indexer/retriever 全套）。
// db 必须非 nil（与知识库共库）；embedder 必须非 nil；chunkRetr 可为 nil。
// cfg.Backend 决定 store 实现；cfg.UseLLMExtractor 决定抽取器是否启用 LLM（由 llmFactory 注入）。
func NewGraphService(
	ctx context.Context,
	db *sql.DB,
	cfg config.GraphConfig,
	embedder *Embedder,
	chunkIndexer *Indexer,
	chunkRetr *Retriever,
	llmFactory func() LLMGraphExtractor,
	logger *zap.Logger,
) (*GraphService, error) {
	if db == nil {
		return nil, fmt.Errorf("graph service: db is nil")
	}
	if embedder == nil {
		return nil, fmt.Errorf("graph service: embedder is nil")
	}

	var store GraphStore
	switch cfg.EffectiveBackend() {
	case "memory":
		store = NewMemoryGraphStore()
	default:
		store = NewSQLiteGraphStore(db)
	}
	if err := store.Init(ctx); err != nil {
		return nil, fmt.Errorf("graph store init: %w", err)
	}

	vecIndex, err := NewGraphVectorIndex(db, embedder, logger)
	if err != nil {
		return nil, fmt.Errorf("graph vector index: %w", err)
	}
	if err := EnsureGraphVectorSchema(db); err != nil {
		return nil, fmt.Errorf("graph vector schema: %w", err)
	}

	extractor := NewGraphExtractor(cfg.EntityTypes, logger)
	if cfg.UseLLMExtractor && llmFactory != nil {
		if impl := llmFactory(); impl != nil {
			extractor.SetLLMExtractor(impl)
		}
	}

	gIndexer, err := NewGraphIndexer(db, store, vecIndex, extractor, chunkIndexer, logger)
	if err != nil {
		return nil, fmt.Errorf("graph indexer: %w", err)
	}
	if err := gIndexer.EnsureSchema(ctx); err != nil {
		return nil, fmt.Errorf("graph indexer schema: %w", err)
	}

	gRetriever, err := NewGraphRetriever(store, vecIndex, chunkRetr, logger)
	if err != nil {
		return nil, fmt.Errorf("graph retriever: %w", err)
	}

	return &GraphService{
		store:     store,
		vecIndex:  vecIndex,
		extractor: extractor,
		indexer:   gIndexer,
		retriever: gRetriever,
		cfg:       cfg,
		logger:    logger,
	}, nil
}

// IndexItem 增量索引单个知识项（抽取实体/关系→清旧→写图→写向量）。
// 与 [knowledge.Indexer.IndexItem] 配合：向量索引完成后调用本方法补建图索引。
func (s *GraphService) IndexItem(ctx context.Context, itemID string) (*GraphSnapshot, error) {
	if s == nil || s.indexer == nil {
		return nil, fmt.Errorf("graph service: nil indexer")
	}
	return s.indexer.ExtractAndIndex(ctx, itemID)
}

// IndexMissing 补齐尚无图向量的知识项（冷启动/中断续跑）。
func (s *GraphService) IndexMissing(ctx context.Context) error {
	if s == nil || s.indexer == nil {
		return fmt.Errorf("graph service: nil indexer")
	}
	return s.indexer.IndexMissing(ctx)
}

// RebuildIndex 全量重建图索引（显式 opt-in，成本更高）。
func (s *GraphService) RebuildIndex(ctx context.Context) error {
	if s == nil || s.indexer == nil {
		return fmt.Errorf("graph service: nil indexer")
	}
	return s.indexer.RebuildIndex(ctx)
}

// Search 双层图检索（local/global/hybrid）。
func (s *GraphService) Search(ctx context.Context, req *GraphSearchRequest) (*GraphSearchResult, error) {
	if s == nil || s.retriever == nil {
		return nil, fmt.Errorf("graph service: nil retriever")
	}
	if req == nil {
		req = &GraphSearchRequest{}
	}
	if req.Mode == "" {
		req.Mode = GraphSearchMode(s.cfg.EffectiveDefaultSearchMode())
	}
	if req.TopK <= 0 {
		req.TopK = s.cfg.EffectiveTopK(0)
	}
	return s.retriever.Search(ctx, req)
}

// GetStatus 返回图索引运行状态。
func (s *GraphService) GetStatus() (bool, int, int, int, string) {
	if s == nil || s.indexer == nil {
		return false, 0, 0, 0, ""
	}
	running, total, current, failed, _, lastItem := s.indexer.GetStatus()
	return running, total, current, failed, lastItem
}

// Backend 返回图存储后端标识。
func (s *GraphService) Backend() string {
	if s == nil || s.store == nil {
		return ""
	}
	return s.store.Backend()
}

// Store 返回底层 GraphStore（供高级用法/测试）。
func (s *GraphService) Store() GraphStore {
	if s == nil {
		return nil
	}
	return s.store
}
