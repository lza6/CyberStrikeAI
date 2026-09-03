package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"cyberstrike-ai/internal/config"

	einoembedding "github.com/cloudwego/eino/components/embedding"
	"github.com/cloudwego/eino/components/indexer"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// einoMockEmbedder is a fake eino embedding.Embedder with controllable behavior.
type einoMockEmbedder struct {
	dim        int
	extraVecs  int // return extra vectors to trigger count mismatch
	err        error
	varyingDim bool // vary vector length per row to trigger dim inconsistency
}

func (m *einoMockEmbedder) EmbedStrings(ctx context.Context, texts []string, opts ...einoembedding.Option) ([][]float64, error) {
	if m.err != nil {
		return nil, m.err
	}
	n := len(texts) + m.extraVecs
	out := make([][]float64, n)
	for i := range out {
		dim := m.dim
		if m.varyingDim && i > 0 {
			dim = m.dim + 1
		}
		vec := make([]float64, dim)
		for j := range vec {
			vec[j] = 0.5
		}
		out[i] = vec
	}
	return out, nil
}

// ---- SQLiteIndexer ----

func TestSQLiteIndexer_StoreBasic(t *testing.T) {
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	at := fixedTestTime()
	insertKnowledgeItem(t, db, "item-1", "XSS", "标题", "/p/a.md", "正文", at)

	si := NewSQLiteIndexer(db, 64, "text-embedding-3-small")
	if si.GetType() != "SQLiteKnowledgeIndexer" {
		t.Fatalf("GetType = %q", si.GetType())
	}
	docs := []*schema.Document{
		{
			ID: "d0", Content: "chunk zero",
			MetaData: map[string]any{metaKBItemID: "item-1", metaKBCategory: "XSS", metaKBTitle: "标题", metaKBChunkIndex: 0},
		},
		{
			ID: "d1", Content: "chunk one",
			MetaData: map[string]any{metaKBItemID: "item-1", metaKBCategory: "XSS", metaKBTitle: "标题", metaKBChunkIndex: 1},
		},
	}
	emb := &einoMockEmbedder{dim: 3}
	ids, err := si.Store(t.Context(), docs, indexer.WithEmbedding(emb))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("ids = %d", len(ids))
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM knowledge_embeddings WHERE item_id='item-1'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("rows = %d err=%v", n, err)
	}
	var model, subIdx string
	var dim int
	var embJSON string
	if err := db.QueryRow(`SELECT embedding_model, sub_indexes, embedding_dim, embedding FROM knowledge_embeddings WHERE id=?`, ids[0]).
		Scan(&model, &subIdx, &dim, &embJSON); err != nil {
		t.Fatal(err)
	}
	if model != "text-embedding-3-small" || dim != 3 {
		t.Fatalf("model=%q dim=%d", model, dim)
	}
	var vec []float32
	if err := json.Unmarshal([]byte(embJSON), &vec); err != nil {
		t.Fatalf("embedding json: %v", err)
	}
	if len(vec) != 3 {
		t.Fatalf("vec dim = %d", len(vec))
	}
}

func TestSQLiteIndexer_StoreWithSubIndexes(t *testing.T) {
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	at := fixedTestTime()
	insertKnowledgeItem(t, db, "item-1", "XSS", "t", "/p", "c", at)

	si := NewSQLiteIndexer(db, 64, "m")
	docs := []*schema.Document{{
		Content:  "text",
		MetaData: map[string]any{metaKBItemID: "item-1", metaKBCategory: "XSS", metaKBTitle: "t", metaKBChunkIndex: 0},
	}}
	_, err := si.Store(t.Context(), docs, indexer.WithEmbedding(&einoMockEmbedder{dim: 2}), indexer.WithSubIndexes([]string{"web", "xss"}))
	if err != nil {
		t.Fatal(err)
	}
	var subIdx string
	if err := db.QueryRow(`SELECT sub_indexes FROM knowledge_embeddings LIMIT 1`).Scan(&subIdx); err != nil {
		t.Fatal(err)
	}
	if subIdx != "web,xss" {
		t.Fatalf("sub_indexes = %q", subIdx)
	}
}

func TestSQLiteIndexer_StoreErrors(t *testing.T) {
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	si := NewSQLiteIndexer(db, 0, "")

	// no embedding option
	docs := []*schema.Document{{Content: "x", MetaData: map[string]any{}}}
	if _, err := si.Store(t.Context(), docs); err == nil {
		t.Fatalf("missing embedding should error")
	}

	// empty docs
	emb := &einoMockEmbedder{dim: 2}
	ids, err := si.Store(t.Context(), nil, indexer.WithEmbedding(emb))
	if err != nil || ids != nil {
		t.Fatalf("empty docs: ids=%v err=%v", ids, err)
	}

	// nil doc
	if _, err := si.Store(t.Context(), []*schema.Document{nil}, indexer.WithEmbedding(emb)); err == nil {
		t.Fatalf("nil doc should error")
	}

	// missing kb_item_id metadata
	if _, err := si.Store(t.Context(), []*schema.Document{{Content: "x"}}, indexer.WithEmbedding(emb)); err == nil {
		t.Fatalf("missing item id should error")
	}

	// missing chunk index
	if _, err := si.Store(t.Context(), []*schema.Document{{Content: "x", MetaData: map[string]any{metaKBItemID: "i"}}}, indexer.WithEmbedding(emb)); err == nil {
		t.Fatalf("missing chunk index should error")
	}

	// embedding count mismatch
	badEmb := &einoMockEmbedder{dim: 2, extraVecs: 1}
	if _, err := si.Store(t.Context(), []*schema.Document{{Content: "x", MetaData: map[string]any{metaKBItemID: "i", metaKBChunkIndex: 0}}}, indexer.WithEmbedding(badEmb)); err == nil {
		t.Fatalf("count mismatch should error")
	}

	// embedder failure
	failEmb := &einoMockEmbedder{err: fmt.Errorf("api down")}
	if _, err := si.Store(t.Context(), []*schema.Document{{Content: "x", MetaData: map[string]any{metaKBItemID: "i", metaKBChunkIndex: 0}}}, indexer.WithEmbedding(failEmb)); err == nil {
		t.Fatalf("embedder failure should error")
	}

	// inconsistent dim across docs
	dimEmb := &einoMockEmbedder{dim: 2, varyingDim: true}
	if _, err := si.Store(t.Context(), []*schema.Document{
		{Content: "a", MetaData: map[string]any{metaKBItemID: "i", metaKBChunkIndex: 0}},
		{Content: "b", MetaData: map[string]any{metaKBItemID: "i", metaKBChunkIndex: 1}},
	}, indexer.WithEmbedding(dimEmb)); err == nil {
		t.Fatalf("inconsistent dims should error")
	}

	// invalid db for tx path: closed DB surfaces an error from BeginTx.
	dbClosed := newTestMemoryDB(t)
	applyKnowledgeSchema(t, dbClosed)
	siNil := NewSQLiteIndexer(dbClosed, 64, "")
	_ = dbClosed.Close()
	if _, err := siNil.Store(t.Context(), []*schema.Document{{Content: "x", MetaData: map[string]any{metaKBItemID: "i", metaKBChunkIndex: 0}}}, indexer.WithEmbedding(emb)); err == nil {
		t.Fatalf("invalid db should error")
	}
}

// ---- schema_migrate.go ----

func TestEnsureKnowledgeEmbeddingsSchema(t *testing.T) {
	db := newTestMemoryDB(t)
	if _, err := db.Exec(`CREATE TABLE knowledge_embeddings (id TEXT PRIMARY KEY, item_id TEXT NOT NULL, chunk_index INTEGER NOT NULL, chunk_text TEXT NOT NULL, embedding TEXT NOT NULL, created_at DATETIME NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if err := EnsureKnowledgeEmbeddingsSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, col := range []string{"sub_indexes", "embedding_model", "embedding_dim"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('knowledge_embeddings') WHERE name=?`, col).Scan(&n); err != nil || n != 1 {
			t.Fatalf("column %s missing: %d %v", col, n, err)
		}
	}
	if err := EnsureKnowledgeEmbeddingsSchema(db); err != nil {
		t.Fatalf("idempotent: %v", err)
	}
	if err := ensureKnowledgeEmbeddingsSubIndexesColumn(db); err != nil {
		t.Fatalf("compat wrapper: %v", err)
	}
	if err := EnsureKnowledgeEmbeddingsSchema(nil); err == nil {
		t.Fatalf("nil db should error")
	}
}

func TestEnsureKnowledgeEmbeddingsSchema_NoTable(t *testing.T) {
	db := newTestMemoryDB(t)
	if err := EnsureKnowledgeEmbeddingsSchema(db); err != nil {
		t.Fatalf("no table: %v", err)
	}
}

// ---- index_pipeline.go ----

func TestNewChunkEnrichLambda(t *testing.T) {
	empty := []*schema.Document{{}, {Content: "  "}}
	docs := []*schema.Document{
		{Content: "a"},
		{Content: "b"},
		{Content: "c"},
		{Content: "d"},
	}
	in := append(empty, docs...)

	// maxChunks=2: filters empties and caps at 2, assigning chunk indexes.
	ch := compose.NewChain[[]*schema.Document, []*schema.Document]()
	ch.AppendLambda(newChunkEnrichLambda(2))
	r, err := ch.Compile(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.Invoke(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("out = %d, want 2", len(out))
	}
	for i, d := range out {
		if v, _ := RequireMetaInt(d.MetaData, metaKBChunkIndex); v != i {
			t.Fatalf("chunk index = %d, want %d", v, i)
		}
	}

	// maxChunks=0: no cap.
	ch0 := compose.NewChain[[]*schema.Document, []*schema.Document]()
	ch0.AppendLambda(newChunkEnrichLambda(0))
	r0, err := ch0.Compile(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	out0, err := r0.Invoke(t.Context(), docs)
	if err != nil {
		t.Fatal(err)
	}
	if len(out0) != 4 {
		t.Fatalf("uncapped = %d", len(out0))
	}
}

func TestBuildKnowledgeIndexChain_Errors(t *testing.T) {
	db := newTestMemoryDB(t)
	if _, err := buildKnowledgeIndexChain(t.Context(), nil, db, nil, ""); err == nil {
		t.Fatalf("nil transformer should error")
	}
	sp, err := newKnowledgeSplitter(32, 4, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := buildKnowledgeIndexChain(t.Context(), nil, nil, sp, ""); err == nil {
		t.Fatalf("nil db should error")
	}
}

func TestBuildKnowledgeIndexChain_Strategies(t *testing.T) {
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	sp, err := newKnowledgeSplitter(32, 4, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := buildKnowledgeIndexChain(t.Context(), &config.IndexingConfig{ChunkStrategy: "recursive", BatchSize: 8}, db, sp, ""); err != nil {
		t.Fatalf("recursive chain: %v", err)
	}
	if _, err := buildKnowledgeIndexChain(t.Context(), &config.IndexingConfig{}, db, sp, ""); err != nil {
		t.Fatalf("markdown chain: %v", err)
	}
}

// ---- indexer.go ----

func newIndexerForTest(t *testing.T, db *sql.DB) *Indexer {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		_ = jsonDecodeBody(r, &req)
		data := make([]map[string]any, len(req.Input))
		for i := range data {
			data[i] = map[string]any{"embedding": []float64{0.5, 0.5, 0.5}, "index": i}
		}
		_ = jsonEncode(w, map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)

	cfg := &config.KnowledgeConfig{
		Embedding: config.EmbeddingConfig{Provider: "openai", Model: "text-embedding-3-small", BaseURL: srv.URL, APIKey: "k"},
		Indexing:  config.IndexingConfig{ChunkSize: 32, ChunkOverlap: 4, MaxRetries: 1, RetryDelayMs: 1, BatchSize: 8},
	}
	e, err := NewEmbedder(t.Context(), cfg, nil, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	idx, err := NewIndexer(t.Context(), db, e, zap.NewNop(), cfg)
	if err != nil {
		t.Fatalf("NewIndexer: %v", err)
	}
	return idx
}

func TestIndexer_IndexItemAndHasIndex(t *testing.T) {
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	at := fixedTestTime()
	insertKnowledgeItem(t, db, "item-1", "XSS", "标题", "/p/a.md", "# 跨站脚本\n\n这是第一段。\n\n这是第二段内容。", at)

	has, err := (&Indexer{db: db}).HasIndex()
	if err != nil || has {
		t.Fatalf("empty index = %v %v", has, err)
	}

	idx := newIndexerForTest(t, db)
	if err := idx.IndexItem(t.Context(), "item-1"); err != nil {
		t.Fatalf("IndexItem: %v", err)
	}

	has2, err := idx.HasIndex()
	if err != nil || !has2 {
		t.Fatalf("HasIndex after index = %v %v", has2, err)
	}

	if err := idx.IndexItem(t.Context(), "missing"); err == nil {
		t.Fatalf("missing item should error")
	}

	lastErr, lastErrTime := idx.GetLastError()
	if lastErr != "" || !lastErrTime.IsZero() {
		t.Fatalf("last error = %q %v", lastErr, lastErrTime)
	}

	// IndexItem sets rebuildLastItemID/rebuildLastChunks.
	_, _, _, _, lastItemID, lastChunks, _ := idx.GetRebuildStatus()
	if lastItemID != "item-1" || lastChunks == 0 {
		t.Fatalf("rebuild status: lastItem=%q chunks=%d", lastItemID, lastChunks)
	}

	if err := idx.RecompileIndexChain(t.Context()); err != nil {
		t.Fatalf("RecompileIndexChain: %v", err)
	}

	if err := idx.TryBeginIndexRun(); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := idx.TryBeginIndexRun(); err == nil {
		t.Fatalf("second begin should fail")
	}
	idx.FinishIndexRun()
}

func TestIndexer_NewIndexerValidation(t *testing.T) {
	db := newTestMemoryDB(t)
	if _, err := NewIndexer(t.Context(), nil, &Embedder{}, nil, nil); err == nil {
		t.Fatalf("nil db should error")
	}
	if _, err := NewIndexer(t.Context(), db, nil, nil, nil); err == nil {
		t.Fatalf("nil embedder should error")
	}
}

func TestIndexer_RunIndexMissing_Rebuild(t *testing.T) {
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	at := fixedTestTime()
	insertKnowledgeItem(t, db, "item-1", "XSS", "t", "/p", "# 跨站脚本\n\n第一段内容。\n\n第二段内容。", at)
	idx := newIndexerForTest(t, db)

	if err := idx.IndexMissing(t.Context()); err != nil {
		t.Fatalf("IndexMissing: %v", err)
	}
	has, err := idx.HasIndex()
	if err != nil || !has {
		t.Fatalf("HasIndex = %v %v", has, err)
	}

	if err := idx.RunIndexMissing(t.Context()); err != nil {
		t.Fatalf("RunIndexMissing: %v", err)
	}

	if err := idx.RebuildIndex(t.Context()); err != nil {
		t.Fatalf("RebuildIndex: %v", err)
	}

	if err := idx.RunRebuildIndex(t.Context()); err != nil {
		t.Fatalf("RunRebuildIndex: %v", err)
	}

	// concurrent run rejected
	if err := idx.TryBeginIndexRun(); err != nil {
		t.Fatal(err)
	}
	if err := idx.IndexMissing(t.Context()); err == nil {
		t.Fatalf("concurrent IndexMissing should be rejected")
	}
	idx.FinishIndexRun()
}

func TestIndexer_SplitTextForGraph(t *testing.T) {
	db := newTestMemoryDB(t)
	idx := newIndexerForTest(t, db)

	out, err := idx.SplitTextForGraph(t.Context(), "  ")
	if err != nil || out != nil {
		t.Fatalf("empty: out=%v err=%v", out, err)
	}
	out2, err := idx.SplitTextForGraph(t.Context(), "# 标题\n\n一些内容。")
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if len(out2) < 1 {
		t.Fatalf("chunks = %d", len(out2))
	}
	var nilIdx *Indexer
	if _, err := nilIdx.SplitTextForGraph(t.Context(), "x"); err == nil {
		t.Fatalf("nil indexer should error")
	}
}

func TestIndexer_ChunkTextByIndexerFallback(t *testing.T) {
	out, err := chunkTextByIndexer(t.Context(), nil, "body")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("rune fallback = %d", len(out))
	}
}

func TestChunkByRunes(t *testing.T) {
	if chunkByRunes("", 512, 50) != nil {
		t.Fatalf("empty body should return nil")
	}
	body := "单"
	if got := chunkByRunes(body, 10, 2); len(got) != 1 {
		t.Fatalf("short body = %d", len(got))
	}
	long := ""
	for i := 0; i < 3000; i++ {
		long += "字"
	}
	got := chunkByRunes(long, 512, 50)
	if len(got) < 5 {
		t.Fatalf("long body chunks = %d", len(got))
	}
	got2 := chunkByRunes(long, 100, 500)
	if len(got2) == 0 {
		t.Fatalf("clamped overlap produced nothing")
	}
	got3 := chunkByRunes(long, 0, 0)
	if len(got3) == 0 {
		t.Fatalf("default size produced nothing")
	}
}

// ---- graph_extractor.go LLM path ----

func TestGraphExtractor_SetLLMExtractorAndAdapt(t *testing.T) {
	g := NewGraphExtractor(nil, zap.NewNop())
	g.SetLLMExtractor(&stubLLMExtractor{})
	g.SetLLMExtractor(nil) // reset to heuristic

	g2 := NewGraphExtractor(nil, zap.NewNop())
	g2.SetLLMExtractor(&stubLLMExtractor{
		ents: []llmEntity{{Name: "SQL注入", Type: "漏洞", Description: "desc"}, {Name: "  ", Type: "x"}},
		rels: []llmRelation{
			{Src: "SQL注入", Tgt: "认证绕过", Keywords: "导致", Description: "d"},
			{Src: "", Tgt: "b"},
			{Src: "same", Tgt: "same"},
		},
	})
	ents, rels, err := g2.Extract(t.Context(), "item-1", "item-1#0", "text")
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 || ents[0].Name != "SQL注入" || ents[0].SourceID != "item-1" {
		t.Fatalf("ents = %+v", ents)
	}
	if len(rels) != 1 || rels[0].SrcID != "SQL注入" || rels[0].Weight != 1.0 {
		t.Fatalf("rels = %+v", rels)
	}

	// LLM error -> heuristic fallback
	g3 := NewGraphExtractor(nil, zap.NewNop())
	g3.SetLLMExtractor(&stubLLMExtractor{err: fmt.Errorf("llm down")})
	ents3, _, err := g3.Extract(t.Context(), "item-1", "item-1#0", "CVE-2021-1234 影响 SQL注入")
	if err != nil {
		t.Fatal(err)
	}
	if len(ents3) == 0 {
		t.Fatalf("heuristic fallback should produce entities")
	}

	if EncodeEntities(ents) == "" {
		t.Fatalf("EncodeEntities empty")
	}

	var g4 *GraphExtractor
	if _, _, err := g4.Extract(t.Context(), "a", "b", "c"); err == nil {
		t.Fatalf("nil extractor should error")
	}
}

func TestGraphExtractor_EmptyText(t *testing.T) {
	g := NewGraphExtractor(nil, nil)
	ents, rels, err := g.Extract(t.Context(), "a", "b", "   ")
	if err != nil || ents != nil || rels != nil {
		t.Fatalf("empty text: %v %v %v", ents, rels, err)
	}
}

// stubLLMExtractor is a canned LLMGraphExtractor.
type stubLLMExtractor struct {
	ents []llmEntity
	rels []llmRelation
	err  error
}

func (s *stubLLMExtractor) Extract(ctx context.Context, text string, entityTypes []string) (llmExtraction, error) {
	if s.err != nil {
		return llmExtraction{}, s.err
	}
	return llmExtraction{Entities: s.ents, Relations: s.rels}, nil
}
