package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	"cyberstrike-ai/internal/config"

	"github.com/cloudwego/eino/components/embedding"
	_ "modernc.org/sqlite"
	"go.uber.org/zap"
)

// 本文件为检索质量测试：用 in-memory SQLite + 伪造 embedding（固定向量，绕过真实 LLM）
// 塞入 N 条文档与 ground-truth query→expected 映射，断言 Search TopK 命中率。
// 付费 API 红线：不调真实 OpenAI，向量全部由 fakeEinoEmbedder 本地生成。

const testEmbeddingModel = "test-embed-model"

// fakeEinoEmbedder 实现 eino embedding.Embedder，按文本映射返回固定向量，绕过真实 LLM 调用。
type fakeEinoEmbedder struct {
	vecOf func(text string) []float32
}

func (f *fakeEinoEmbedder) EmbedStrings(_ context.Context, texts []string, _ ...embedding.Option) ([][]float64, error) {
	if f == nil || f.vecOf == nil {
		return nil, fmt.Errorf("fake embedder not configured")
	}
	out := make([][]float64, len(texts))
	for i, t := range texts {
		v := f.vecOf(t)
		row := make([]float64, len(v))
		for j, x := range v {
			row[j] = float64(x)
		}
		out[i] = row
	}
	return out, nil
}

// newTestRetriever 构造一个走裸向量检索路径的 Retriever（不 wire pipeline），
// 使用 :memory: SQLite + 伪造 embedder，不接触真实 LLM。
// cfg 为 nil 时使用默认 TopK=5 / SimilarityThreshold=0.7。
func newTestRetriever(t *testing.T, cfg *RetrievalConfig, vecOf func(text string) []float32) (*Retriever, *sql.DB) {
	t.Helper()
	// file::memory:?cache=shared 使多连接共享同一内存库；SetMaxOpenConns(1) 避免并发连接看到不同库。
	db, err := sql.Open("sqlite", "file::memory:?cache=shared&_foreign_keys=1")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	if err := initTestSchema(db); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	if cfg == nil {
		cfg = &RetrievalConfig{TopK: 5, SimilarityThreshold: 0.7}
	}
	emb := &Embedder{
		eino:       &fakeEinoEmbedder{vecOf: vecOf},
		config:     &config.KnowledgeConfig{Embedding: config.EmbeddingConfig{Model: testEmbeddingModel}},
		logger:     zap.NewNop(),
		maxRetries: 1,
	}
	r := NewRetriever(db, emb, cfg, zap.NewNop())
	return r, db
}

func initTestSchema(db *sql.DB) error {
	schema := `
	CREATE TABLE IF NOT EXISTS knowledge_base_items (
		id TEXT PRIMARY KEY,
		category TEXT NOT NULL,
		title TEXT NOT NULL,
		file_path TEXT NOT NULL,
		content TEXT,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	);
	CREATE TABLE IF NOT EXISTS knowledge_embeddings (
		id TEXT PRIMARY KEY,
		item_id TEXT NOT NULL,
		chunk_index INTEGER NOT NULL,
		chunk_text TEXT NOT NULL,
		embedding TEXT NOT NULL,
		sub_indexes TEXT NOT NULL DEFAULT '',
		embedding_model TEXT NOT NULL DEFAULT '',
		embedding_dim INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL
	);`
	_, err := db.Exec(schema)
	return err
}

// insertDoc 插入一条知识项 + 对应向量行（向量以 JSON 序列化存入 embedding 列，与生产 vectorSearch 解析一致）。
func insertDoc(t *testing.T, db *sql.DB, itemID, category, title, chunkID, chunkText string, vec []float32) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO knowledge_base_items (id, category, title, file_path, content, created_at, updated_at) VALUES (?, ?, ?, ?, ?, datetime('now'), datetime('now'))`,
		itemID, category, title, "path/"+itemID, chunkText)
	if err != nil {
		t.Fatalf("insert item %s: %v", itemID, err)
	}
	raw, _ := json.Marshal(vec)
	_, err = db.Exec(`INSERT INTO knowledge_embeddings (id, item_id, chunk_index, chunk_text, embedding, sub_indexes, embedding_model, embedding_dim, created_at) VALUES (?, ?, ?, ?, ?, '', ?, ?, datetime('now'))`,
		chunkID, itemID, 0, chunkText, string(raw), testEmbeddingModel, len(vec))
	if err != nil {
		t.Fatalf("insert embedding %s: %v", chunkID, err)
	}
}

// insertDocWithModel 同 insertDoc，但允许指定 embedding_model，用于测试模型不一致跳过逻辑。
func insertDocWithModel(t *testing.T, db *sql.DB, itemID, category, title, chunkID, chunkText, model string, vec []float32) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO knowledge_base_items (id, category, title, file_path, content, created_at, updated_at) VALUES (?, ?, ?, ?, ?, datetime('now'), datetime('now'))`,
		itemID, category, title, "path/"+itemID, chunkText)
	if err != nil {
		t.Fatalf("insert item %s: %v", itemID, err)
	}
	raw, _ := json.Marshal(vec)
	_, err = db.Exec(`INSERT INTO knowledge_embeddings (id, item_id, chunk_index, chunk_text, embedding, sub_indexes, embedding_model, embedding_dim, created_at) VALUES (?, ?, ?, ?, ?, '', ?, ?, datetime('now'))`,
		chunkID, itemID, 0, chunkText, string(raw), model, len(vec))
	if err != nil {
		t.Fatalf("insert embedding %s: %v", chunkID, err)
	}
}

// recallAt 返回 expected 在结果列表中的排名（1-based）；未命中返回 -1。
func rankOf(results []*RetrievalResult, expectedChunkID string) int {
	for i, r := range results {
		if r != nil && r.Chunk != nil && r.Chunk.ID == expectedChunkID {
			return i + 1
		}
	}
	return -1
}

// TestRecallAtK_PerfectMatch 查询向量与某 doc 完全一致时，Top1 必须命中该 doc。
func TestRecallAtK_PerfectMatch(t *testing.T) {
	r, db := newTestRetriever(t, nil, func(string) []float32 {
		return []float32{1, 0, 0}
	})
	defer db.Close()

	insertDoc(t, db, "item-1", "cat", "title-1", "chunk-1", "alpha content", []float32{1, 0, 0})
	insertDoc(t, db, "item-2", "cat", "title-2", "chunk-2", "beta content", []float32{0, 1, 0})
	insertDoc(t, db, "item-3", "cat", "title-3", "chunk-3", "gamma content", []float32{0, 0, 1})

	res, err := r.Search(context.Background(), &SearchRequest{Query: "q", TopK: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("expect only 1 above threshold, got %d", len(res))
	}
	if rank := rankOf(res, "chunk-1"); rank != 1 {
		t.Fatalf("expect chunk-1 at rank 1, got rank=%d results=%#v", rank, res)
	}
}

// TestRecallAtK_PartialMatch 查询向量接近某 doc 时，该 doc 必须落在 Top3 内（recall@3=1）。
func TestRecallAtK_PartialMatch(t *testing.T) {
	r, db := newTestRetriever(t, nil, func(string) []float32 {
		// 接近 doc1（[1,0,0]）但带一定 doc2 分量
		return []float32{0.8, 0.6, 0}
	})
	defer db.Close()

	insertDoc(t, db, "item-1", "cat", "title-1", "chunk-1", "alpha content", []float32{1, 0, 0})
	insertDoc(t, db, "item-2", "cat", "title-2", "chunk-2", "beta content", []float32{0, 1, 0})
	insertDoc(t, db, "item-3", "cat", "title-3", "chunk-3", "gamma content", []float32{0, 0, 1})

	// 阈值降到 0.5 使 doc2（cosine=0.6）也返回；排序应为 doc1 > doc2。
	res, err := r.Search(context.Background(), &SearchRequest{Query: "q", TopK: 3, Threshold: 0.5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) < 1 {
		t.Fatalf("expect non-empty results, got 0")
	}
	if rank := rankOf(res, "chunk-1"); rank < 1 || rank > 3 {
		t.Fatalf("expect chunk-1 in Top3, got rank=%d", rank)
	}
	if len(res) >= 2 {
		if rankOf(res, "chunk-2") < 1 || rankOf(res, "chunk-2") > 3 {
			t.Fatalf("expect chunk-2 in Top3, got rank=%d", rankOf(res, "chunk-2"))
		}
		// doc1 必须排在 doc2 前面（相似度更高）
		if rankOf(res, "chunk-1") > rankOf(res, "chunk-2") {
			t.Fatalf("doc1 should rank higher than doc2")
		}
	}
}

// TestThresholdFilter 相似度低于阈值的不返回（默认 0.7 阈值下，弱匹配被过滤）。
func TestThresholdFilter(t *testing.T) {
	r, db := newTestRetriever(t, nil, func(string) []float32 {
		// 与三轴均弱相关：与 doc1 cosine = 0.1/sqrt(0.03) ≈ 0.577 < 0.7
		return []float32{0.1, 0.1, 0.1}
	})
	defer db.Close()

	insertDoc(t, db, "item-1", "cat", "title-1", "chunk-1", "alpha content", []float32{1, 0, 0})
	insertDoc(t, db, "item-2", "cat", "title-2", "chunk-2", "beta content", []float32{0, 1, 0})
	insertDoc(t, db, "item-3", "cat", "title-3", "chunk-3", "gamma content", []float32{0, 0, 1})

	// 默认阈值 0.7，所有 doc cosine≈0.577 都被过滤
	res, err := r.Search(context.Background(), &SearchRequest{Query: "q", TopK: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 0 {
		t.Fatalf("expect 0 results below threshold, got %d (%#v)", len(res), res)
	}

	// 阈值降到 0.5 时应返回 3 条
	res2, err := r.Search(context.Background(), &SearchRequest{Query: "q", TopK: 5, Threshold: 0.5})
	if err != nil {
		t.Fatalf("Search low threshold: %v", err)
	}
	if len(res2) != 3 {
		t.Fatalf("expect 3 results at threshold 0.5, got %d", len(res2))
	}
}

// TestTopKTruncation 返回数不超过 TopK（即便高于阈值的候选多于 TopK）。
func TestTopKTruncation(t *testing.T) {
	r, db := newTestRetriever(t, nil, func(string) []float32 {
		// 与三轴等距：cosine ≈ 0.577
		return []float32{0.9, 0.9, 0.9}
	})
	defer db.Close()

	insertDoc(t, db, "item-1", "cat", "title-1", "chunk-1", "alpha content", []float32{1, 0, 0})
	insertDoc(t, db, "item-2", "cat", "title-2", "chunk-2", "beta content", []float32{0, 1, 0})
	insertDoc(t, db, "item-3", "cat", "title-3", "chunk-3", "gamma content", []float32{0, 0, 1})

	// TopK=1，阈值 0.5 使 3 条都高于阈值，但只能返回 1 条
	res, err := r.Search(context.Background(), &SearchRequest{Query: "q", TopK: 1, Threshold: 0.5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("expect TopK=1 truncation to 1 result, got %d", len(res))
	}
}
