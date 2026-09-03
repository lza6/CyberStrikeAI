package knowledge

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cyberstrike-ai/internal/config"

	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// newVectorTestSetup builds an in-memory DB with schema + one item, and an
// Embedder pointed at a mock embeddings endpoint returning the given vector
// for every request.
func newVectorTestSetup(t *testing.T, queryVec []float32) (*sql.DB, *Embedder) {
	t.Helper()
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)

	at := fixedTestTime()
	insertKnowledgeItem(t, db, "item-1", "XSS", "注入防御", "/kb/xss/a.md", "跨站脚本攻击的防御方式", at)
	insertKnowledgeItem(t, db, "item-2", "SQLi", "SQL注入", "/kb/sqli/a.md", "SQL 注入的成因与修复", at)

	embJSON, _ := json.Marshal([]float32{1.0, 0.0, 0.0})
	rows := []struct {
		id, itemID string
		idx        int
		text       string
	}{
		{"chunk-1", "item-1", 0, "跨站脚本概述"},
		{"chunk-2", "item-1", 1, "输入过滤"},
		{"chunk-3", "item-2", 0, "参数化查询"},
	}
	for _, row := range rows {
		_, err := db.Exec(
			`INSERT INTO knowledge_embeddings (id, item_id, chunk_index, chunk_text, embedding, sub_indexes, embedding_model, embedding_dim, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			row.id, row.itemID, row.idx, row.text, string(embJSON), "", "text-embedding-3-small", 3, at,
		)
		if err != nil {
			t.Fatalf("insert embedding %s: %v", row.id, err)
		}
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data := []map[string]any{{"embedding": float64Slice(queryVec), "index": 0}}
		_ = jsonEncode(w, map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)

	e := newTestEmbedder(t, srv.URL, config.IndexingConfig{MaxRetries: 1, RetryDelayMs: 1})
	return db, e
}

func float64Slice(in []float32) []float64 {
	out := make([]float64, len(in))
	for i, v := range in {
		out[i] = float64(v)
	}
	return out
}

func TestVectorSearch_BasicOrdering(t *testing.T) {
	db, e := newVectorTestSetup(t, []float32{1, 0, 0})
	r := NewRetriever(db, e, &RetrievalConfig{TopK: 5, SimilarityThreshold: 0.5}, zap.NewNop())

	res, err := r.vectorSearch(t.Context(), &SearchRequest{Query: "xss", TopK: 5, Threshold: 0.5})
	if err != nil {
		t.Fatalf("vectorSearch: %v", err)
	}
	if len(res) != 3 {
		t.Fatalf("expected 3 results, got %d", len(res))
	}
	// identical vectors -> similarity 1, descending order.
	for i := 1; i < len(res); i++ {
		if res[i].Similarity > res[i-1].Similarity {
			t.Fatalf("results not sorted: %v", res)
		}
	}
	if res[0].Chunk.ID != "chunk-1" {
		t.Fatalf("first = %s", res[0].Chunk.ID)
	}
	if res[0].Item.Category != "XSS" {
		t.Fatalf("category = %s", res[0].Item.Category)
	}
}

func TestVectorSearch_TopKAndThreshold(t *testing.T) {
	db, e := newVectorTestSetup(t, []float32{1, 0, 0})
	r := NewRetriever(db, e, &RetrievalConfig{TopK: 2, SimilarityThreshold: 0.5}, nil)

	res, err := r.vectorSearch(t.Context(), &SearchRequest{Query: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("topK 2: got %d", len(res))
	}

	// high threshold filters everything out.
	res2, err := r.vectorSearch(t.Context(), &SearchRequest{Query: "q", TopK: 5, Threshold: 1.5})
	if err != nil {
		t.Fatal(err)
	}
	if len(res2) != 0 {
		t.Fatalf("threshold 1.5 should filter all, got %d", len(res2))
	}
}

func TestVectorSearch_EmbedModelFilter(t *testing.T) {
	db, e := newVectorTestSetup(t, []float32{1, 0, 0})
	// One row with a mismatching model name must be skipped.
	_, err := db.Exec(`UPDATE knowledge_embeddings SET embedding_model='other-model' WHERE id='chunk-1'`)
	if err != nil {
		t.Fatal(err)
	}
	r := NewRetriever(db, e, &RetrievalConfig{TopK: 5}, zap.NewNop())
	res, err := r.vectorSearch(t.Context(), &SearchRequest{Query: "q", TopK: 5, Threshold: 0.1})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range res {
		if row.Chunk.ID == "chunk-1" {
			t.Fatalf("mismatched model row should be filtered")
		}
	}
}

func TestVectorSearch_DimMismatchFilter(t *testing.T) {
	db, e := newVectorTestSetup(t, []float32{1, 0, 0})
	// Row with a 2-dim vector and rowDim=3 -> len(embedding) != rowDim, skipped.
	_, err := db.Exec(`UPDATE knowledge_embeddings SET embedding='[1,0]', embedding_dim=3 WHERE id='chunk-1'`)
	if err != nil {
		t.Fatal(err)
	}
	r := NewRetriever(db, e, &RetrievalConfig{TopK: 5}, zap.NewNop())
	res, err := r.vectorSearch(t.Context(), &SearchRequest{Query: "q", TopK: 5, Threshold: -1})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range res {
		if row.Chunk.ID == "chunk-1" {
			t.Fatalf("dim mismatch row should be filtered")
		}
	}
}

func TestVectorSearch_SubIndexFilter(t *testing.T) {
	db, e := newVectorTestSetup(t, []float32{1, 0, 0})
	// tag two rows, leave one untagged (legacy rows always match).
	_, err := db.Exec(`UPDATE knowledge_embeddings SET sub_indexes='web, xss' WHERE id='chunk-1'`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`UPDATE knowledge_embeddings SET sub_indexes='network' WHERE id='chunk-2'`)
	if err != nil {
		t.Fatal(err)
	}
	r := NewRetriever(db, e, &RetrievalConfig{TopK: 5}, nil)
	res, err := r.vectorSearch(t.Context(), &SearchRequest{Query: "q", TopK: 5, Threshold: 0.1, SubIndexFilter: "WEB"})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, row := range res {
		ids[row.Chunk.ID] = true
	}
	if !ids["chunk-1"] {
		t.Errorf("tagged row chunk-1 should match 'web'")
	}
	if ids["chunk-2"] {
		t.Errorf("network-tagged row should not match 'web'")
	}
	if !ids["chunk-3"] {
		t.Errorf("legacy untagged row should always match")
	}
}

func TestVectorSearch_RiskTypeFilter(t *testing.T) {
	db, e := newVectorTestSetup(t, []float32{1, 0, 0})
	r := NewRetriever(db, e, &RetrievalConfig{TopK: 5}, nil)
	res, err := r.vectorSearch(t.Context(), &SearchRequest{Query: "q", TopK: 5, Threshold: 0.1, RiskType: "XSS"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("risk type XSS should return 2 rows, got %d", len(res))
	}
	for _, row := range res {
		if row.Item.Category != "XSS" {
			t.Fatalf("non-XSS row leaked: %s", row.Item.Category)
		}
	}
	// non-matching risk type returns nothing.
	res2, err := r.vectorSearch(t.Context(), &SearchRequest{Query: "q", TopK: 5, Threshold: 0.1, RiskType: "Nope"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res2) != 0 {
		t.Fatalf("nope filter should return 0, got %d", len(res2))
	}
}

func TestVectorSearch_EmptyQuery(t *testing.T) {
	db, e := newVectorTestSetup(t, []float32{1, 0, 0})
	r := NewRetriever(db, e, &RetrievalConfig{TopK: 5}, nil)
	if _, err := r.vectorSearch(t.Context(), &SearchRequest{Query: ""}); err == nil {
		t.Fatalf("empty query should error")
	}
}

func TestVectorSearch_EmbedderFails(t *testing.T) {
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	at := fixedTestTime()
	insertKnowledgeItem(t, db, "item-1", "XSS", "t", "/p", "c", at)

	// server always 500s.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
	}))
	t.Cleanup(srv.Close)
	e := newTestEmbedder(t, srv.URL, config.IndexingConfig{MaxRetries: 1})

	r := NewRetriever(db, e, &RetrievalConfig{TopK: 5}, nil)
	if _, err := r.vectorSearch(t.Context(), &SearchRequest{Query: "q"}); err == nil {
		t.Fatalf("embedder failure should propagate")
	}
}

func TestRetriever_Search_NilAndEmpty(t *testing.T) {
	db, e := newVectorTestSetup(t, []float32{1, 0, 0})
	r := NewRetriever(db, e, &RetrievalConfig{TopK: 5}, nil)
	if _, err := r.Search(t.Context(), nil); err == nil {
		t.Fatalf("nil request should error")
	}
	if _, err := r.Search(t.Context(), &SearchRequest{Query: "  "}); err == nil {
		t.Fatalf("blank query should error")
	}
}

func TestRetriever_Search_WithOptions(t *testing.T) {
	db, e := newVectorTestSetup(t, []float32{1, 0, 0})
	r := NewRetriever(db, e, &RetrievalConfig{TopK: 5, SimilarityThreshold: 0.1}, nil)

	res, err := r.Search(t.Context(), &SearchRequest{
		Query:          "q",
		RiskType:       "SQLi",
		SubIndexFilter: "",
		TopK:           1,
		Threshold:      0.1,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("topK 1: got %d", len(res))
	}
	if res[0].Item.Category != "SQLi" {
		t.Fatalf("category = %s", res[0].Item.Category)
	}
}

func TestRetriever_EinoRetrieve_ViaPipeline(t *testing.T) {
	db, e := newVectorTestSetup(t, []float32{1, 0, 0})
	r := NewRetriever(db, e, &RetrievalConfig{TopK: 2, SimilarityThreshold: 0.1}, zap.NewNop())

	docs, err := r.EinoRetrieve(t.Context(), "注入", retriever.WithTopK(2))
	if err != nil {
		t.Fatalf("EinoRetrieve: %v", err)
	}
	if len(docs) == 0 {
		t.Fatalf("expected docs")
	}
	d := docs[0]
	if MetaLookupString(d.MetaData, metaKBItemID) == "" {
		t.Fatalf("metadata missing item id: %+v", d.MetaData)
	}
	if _, err := RequireMetaInt(d.MetaData, metaKBChunkIndex); err != nil {
		t.Fatalf("chunk index metadata: %v", err)
	}
}

func TestRetriever_EinoRetrieve_EmptyQuery(t *testing.T) {
	db, e := newVectorTestSetup(t, []float32{1, 0, 0})
	r := NewRetriever(db, e, &RetrievalConfig{TopK: 2}, nil)
	if _, err := r.EinoRetrieve(t.Context(), "  "); err == nil {
		t.Fatalf("empty query should error")
	}
}

func TestRetriever_EinoRetrieve_WithDSL(t *testing.T) {
	db, e := newVectorTestSetup(t, []float32{1, 0, 0})
	r := NewRetriever(db, e, &RetrievalConfig{TopK: 3, SimilarityThreshold: 0.1}, nil)
	docs, err := r.EinoRetrieve(t.Context(), "q",
		retriever.WithDSLInfo(map[string]any{
			DSLRiskType:            "XSS",
			DSLSimilarityThreshold: 0.1,
			DSLSubIndexFilter:      "",
		}),
	)
	if err != nil {
		t.Fatalf("DSL retrieve: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("expected 2 XSS docs, got %d", len(docs))
	}
}

func TestNewVectorEinoRetriever_Nil(t *testing.T) {
	if NewVectorEinoRetriever(nil) != nil {
		t.Fatalf("nil base should return nil")
	}
}

func TestVectorEinoRetriever_NilInner(t *testing.T) {
	var h *VectorEinoRetriever
	if _, err := h.Retrieve(t.Context(), "q"); err == nil {
		t.Fatalf("nil inner should error")
	}
}

func TestRetriever_SetDocumentReranker(t *testing.T) {
	db, e := newVectorTestSetup(t, []float32{1, 0, 0})
	r := NewRetriever(db, e, &RetrievalConfig{TopK: 5}, nil)
	if r.documentReranker() != nil {
		t.Fatalf("default reranker should be nil")
	}
	rr := NopDocumentReranker{}
	r.SetDocumentReranker(rr)
	if r.documentReranker() == nil {
		t.Fatalf("reranker should be set")
	}
	// nil receiver safe
	var r2 *Retriever
	r2.SetDocumentReranker(rr)
}

func TestKnowledgePipelineRetriever_Basic(t *testing.T) {
	db, e := newVectorTestSetup(t, []float32{1, 0, 0})
	r := NewRetriever(db, e, &RetrievalConfig{TopK: 2, SimilarityThreshold: 0.1}, zap.NewNop())
	inner := NewVectorEinoRetriever(r)
	p := newKnowledgePipelineRetriever(inner, r)
	if p == nil {
		t.Fatalf("pipeline nil")
	}
	if p.GetType() != "KnowledgeRAGPipeline" {
		t.Fatalf("GetType = %q", p.GetType())
	}
	out, err := p.Retrieve(t.Context(), "注入", retriever.WithTopK(2))
	if err != nil {
		t.Fatalf("pipeline retrieve: %v", err)
	}
	if len(out) == 0 || len(out) > 2 {
		t.Fatalf("out = %d", len(out))
	}
}

func TestKnowledgePipelineRetriever_NilAndEmpty(t *testing.T) {
	if newKnowledgePipelineRetriever(nil, nil) != nil {
		t.Fatalf("nil args should return nil")
	}
	db, e := newVectorTestSetup(t, []float32{1, 0, 0})
	r := NewRetriever(db, e, &RetrievalConfig{TopK: 2}, nil)
	p := newKnowledgePipelineRetriever(NewVectorEinoRetriever(r), r)
	if _, err := p.Retrieve(t.Context(), ""); err == nil {
		t.Fatalf("empty query should error")
	}
}

func TestKnowledgePipelineRetriever_RerankErrorFallsBack(t *testing.T) {
	db, e := newVectorTestSetup(t, []float32{1, 0, 0})
	r := NewRetriever(db, e, &RetrievalConfig{TopK: 5, SimilarityThreshold: 0.1}, zap.NewNop())
	// reranker pointing at a dead server -> rerank error -> fusion order kept.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {}))
	srv.Close()
	rr, err := NewHTTPReranker(&config.RerankConfig{Provider: "cohere", BaseURL: srv.URL, APIKey: "k"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.SetDocumentReranker(rr)

	p := newKnowledgePipelineRetriever(NewVectorEinoRetriever(r), r)
	out, err := p.Retrieve(t.Context(), "注入", retriever.WithTopK(3))
	if err != nil {
		t.Fatalf("rerank failure should fall back: %v", err)
	}
	if len(out) == 0 {
		t.Fatalf("expected docs after fallback")
	}
}

func TestKnowledgePipelineRetriever_RerankReorders(t *testing.T) {
	db, e := newVectorTestSetup(t, []float32{1, 0, 0})
	r := NewRetriever(db, e, &RetrievalConfig{TopK: 5, SimilarityThreshold: 0.1}, zap.NewNop())
	// rerank server puts the last doc first.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_ = jsonEncode(w, map[string]any{"results": []map[string]any{
			{"index": 2},
			{"index": 0},
		}})
	}))
	t.Cleanup(srv.Close)
	rr, err := NewHTTPReranker(&config.RerankConfig{Provider: "cohere", BaseURL: srv.URL, APIKey: "k"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.SetDocumentReranker(rr)

	p := newKnowledgePipelineRetriever(NewVectorEinoRetriever(r), r)
	out, err := p.Retrieve(t.Context(), "注入", retriever.WithTopK(3))
	if err != nil {
		t.Fatal(err)
	}
	// First returned doc should be the one rerank promoted (chunk-3 content).
	if len(out) < 2 || out[0].Content != "参数化查询" {
		t.Fatalf("rerank order not applied: %#v", out)
	}
}

func TestDocumentsToRetrievalResults_MissingMeta(t *testing.T) {
	docs := []*schema.Document{{ID: "x", Content: "c"}}
	if _, err := documentsToRetrievalResults(docs); err == nil {
		t.Fatalf("missing item id should error")
	}
	docs2 := []*schema.Document{{
		ID: "x", Content: "c",
		MetaData: map[string]any{metaKBItemID: "i1"},
	}}
	if _, err := documentsToRetrievalResults(docs2); err == nil {
		t.Fatalf("missing chunk index should error")
	}
	docs3 := []*schema.Document{nil}
	out, err := documentsToRetrievalResults(docs3)
	if err != nil {
		t.Fatalf("nil doc: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("nil doc should be skipped, got %d", len(out))
	}
}

func TestRetrievalResultsToDocuments_Basic(t *testing.T) {
	results := []*RetrievalResult{
		{
			Chunk:      &KnowledgeChunk{ID: "c1", ItemID: "i1", ChunkIndex: 0, ChunkText: "t"},
			Item:       &KnowledgeItem{ID: "i1", Category: "XSS", Title: "T"},
			Similarity: 0.9,
			Score:      0.9,
		},
		nil,
		{Chunk: nil, Item: &KnowledgeItem{}},
	}
	docs := retrievalResultsToDocuments(results)
	if len(docs) != 1 {
		t.Fatalf("docs = %d, want 1", len(docs))
	}
	if docs[0].ID != "c1" || docs[0].Score() != 0.9 {
		t.Fatalf("doc = %+v", docs[0])
	}
}

func TestRetriever_UpdateConfig(t *testing.T) {
	db, e := newVectorTestSetup(t, []float32{1, 0, 0})
	r := NewRetriever(db, e, &RetrievalConfig{TopK: 2, SimilarityThreshold: 0.1}, zap.NewNop())
	// Without wireOpenAI, UpdateConfig just replaces config.
	r.UpdateConfig(&RetrievalConfig{TopK: 3, SimilarityThreshold: 0.2})
	if r.config.TopK != 3 {
		t.Fatalf("config not updated: %d", r.config.TopK)
	}
	// nil config is ignored.
	r.UpdateConfig(nil)
	if r.config.TopK != 3 {
		t.Fatalf("nil config should be ignored")
	}
}

func TestKnowledgeEmbeddingSelectSQL(t *testing.T) {
	r := &Retriever{}
	q, args := r.knowledgeEmbeddingSelectSQL("XSS", "web tag")
	if !strings.Contains(q, "TRIM(i.category)") {
		t.Errorf("risk filter missing: %s", q)
	}
	if !strings.Contains(q, "INSTR") {
		t.Errorf("sub index filter missing: %s", q)
	}
	if len(args) != 2 {
		t.Fatalf("args = %v", args)
	}
	// no filters -> no args
	q2, args2 := r.knowledgeEmbeddingSelectSQL("", "")
	if len(args2) != 0 || strings.Contains(q2, "TRIM(i.category)") {
		t.Fatalf("no-filter query wrong: %s %v", q2, args2)
	}
}

func TestRetriever_SearchEmbedderError(t *testing.T) {
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	at := fixedTestTime()
	insertKnowledgeItem(t, db, "item-1", "XSS", "t", "/p", "c", at)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
	}))
	t.Cleanup(srv.Close)
	e := newTestEmbedder(t, srv.URL, config.IndexingConfig{MaxRetries: 1})

	r := NewRetriever(db, e, &RetrievalConfig{TopK: 5}, nil)
	if _, err := r.Search(t.Context(), &SearchRequest{Query: "q"}); err == nil {
		t.Fatalf("embed error should propagate through Search")
	}
}

func TestRetriever_AsEinoRetriever(t *testing.T) {
	db, e := newVectorTestSetup(t, []float32{1, 0, 0})
	r := NewRetriever(db, e, &RetrievalConfig{TopK: 2}, nil)
	rr := r.AsEinoRetriever()
	if rr == nil {
		t.Fatalf("nil retriever")
	}
	docs, err := rr.Retrieve(t.Context(), "注入")
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(docs) == 0 {
		t.Fatalf("expected docs")
	}
}

func TestVectorSearch_ZeroVectorQuery(t *testing.T) {
	// Zero query vector -> cosine similarity 0 for all rows; threshold 0.5 filters all.
	db, e := newVectorTestSetup(t, []float32{0, 0, 0})
	r := NewRetriever(db, e, &RetrievalConfig{TopK: 5}, nil)
	res, err := r.vectorSearch(t.Context(), &SearchRequest{Query: "q", TopK: 5, Threshold: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 0 {
		t.Fatalf("zero vector should produce 0 similarities, got %d", len(res))
	}
}

func TestRetriever_SearchEmbedderNilModelName(t *testing.T) {
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	at := fixedTestTime()
	insertKnowledgeItem(t, db, "item-1", "XSS", "t", "/p", "c", at)
	// Row with empty model name should still match (expectedModel = "" -> skip check).
	embJSON, _ := json.Marshal([]float32{1, 0, 0})
	_, err := db.Exec(`INSERT INTO knowledge_embeddings (id, item_id, chunk_index, chunk_text, embedding, sub_indexes, embedding_model, embedding_dim, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"c1", "item-1", 0, "text", string(embJSON), "", "", 3, at)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = jsonEncode(w, map[string]any{"data": []map[string]any{{"embedding": float64Slice([]float32{1, 0, 0}), "index": 0}}})
	}))
	t.Cleanup(srv.Close)
	e := newTestEmbedder(t, srv.URL, config.IndexingConfig{MaxRetries: 1})

	r := NewRetriever(db, e, &RetrievalConfig{TopK: 5}, nil)
	res, err := r.vectorSearch(t.Context(), &SearchRequest{Query: "q", TopK: 5, Threshold: 0.1})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("empty model row should match, got %d", len(res))
	}
}

var _ = time.Now
