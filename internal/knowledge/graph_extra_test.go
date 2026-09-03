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

	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// graphTestEmbedderServer spins up a mock embeddings server returning fixed
// 3-dim vectors for every batch.
func graphTestEmbedderServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		_ = jsonDecodeBody(r, &req)
		data := make([]map[string]any, len(req.Input))
		for i := range data {
			data[i] = map[string]any{"embedding": []float64{0.6, 0.6, 0.6}, "index": i}
		}
		_ = jsonEncode(w, map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newTestGraphEmbedder builds an Embedder for graph vector index tests.
func newTestGraphEmbedder(t *testing.T, baseURL string) *Embedder {
	t.Helper()
	cfg := &config.KnowledgeConfig{
		Embedding: config.EmbeddingConfig{Provider: "openai", Model: "text-embedding-3-small", BaseURL: baseURL, APIKey: "k"},
		Indexing:  config.IndexingConfig{MaxRetries: 1, RetryDelayMs: 1},
	}
	e, err := NewEmbedder(t.Context(), cfg, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}
	return e
}

// ---- graph_store_sqlite.go (pure-Go in-memory sqlite) ----

func TestSQLiteGraphStore_Contract(t *testing.T) {
	db := newTestMemoryDB(t)
	s := NewSQLiteGraphStore(db)
	if s.Backend() != "sqlite" {
		t.Fatalf("backend = %q", s.Backend())
	}
	ctx := t.Context()
	if err := s.Init(ctx); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := s.IndexDoneCallback(ctx); err != nil {
		t.Fatalf("index done callback: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := s.Drop(ctx); err != nil {
		t.Fatalf("drop: %v", err)
	}

	// Upsert node.
	if err := s.UpsertNode(ctx, &Entity{Name: "A", Type: "漏洞", Description: "d1", SourceID: "i1", ChunkIDs: []string{"c1"}}); err != nil {
		t.Fatalf("upsert node: %v", err)
	}
	// Merge.
	if err := s.UpsertNode(ctx, &Entity{Name: "A", Type: "漏洞", Description: "d2", ChunkIDs: []string{"c2"}}); err != nil {
		t.Fatalf("upsert node 2: %v", err)
	}
	n, err := s.GetNode(ctx, "A")
	if err != nil || n == nil {
		t.Fatalf("get node: %v %v", n, err)
	}
	if n.Description != "d1\n\nd2" {
		t.Errorf("merged desc = %q", n.Description)
	}
	if len(n.ChunkIDs) != 2 {
		t.Errorf("merged chunks = %v", n.ChunkIDs)
	}

	// Upsert edge.
	if err := s.UpsertEdge(ctx, &Relation{SrcID: "A", TgtID: "B", Keywords: "k1", Description: "r1", Weight: 1.0, SourceID: "i1", ChunkIDs: []string{"c1"}}); err != nil {
		t.Fatalf("upsert edge: %v", err)
	}
	if err := s.UpsertEdge(ctx, &Relation{SrcID: "A", TgtID: "B", Description: "r2", Weight: 1.5, SourceID: "i2", ChunkIDs: []string{"c3"}}); err != nil {
		t.Fatalf("upsert edge 2: %v", err)
	}
	e, err := s.GetEdge(ctx, "A", "B")
	if err != nil || e == nil {
		t.Fatalf("get edge: %v %v", e, err)
	}
	if e.Weight != 2.5 {
		t.Errorf("weight = %v, want 2.5", e.Weight)
	}

	// HasNode/HasEdge/NodeDegree.
	if has, _ := s.HasNode(ctx, "A"); !has {
		t.Errorf("HasNode A")
	}
	if has, _ := s.HasEdge(ctx, "A", "B"); !has {
		t.Errorf("HasEdge A-B")
	}
	if has, _ := s.HasNode(ctx, "Z"); has {
		t.Errorf("HasNode Z should be false")
	}
	if d, _ := s.NodeDegree(ctx, "A"); d != 1 {
		t.Errorf("degree = %d", d)
	}

	// GetNodeEdges
	edges, err := s.GetNodeEdges(ctx, "A")
	if err != nil || len(edges) != 1 {
		t.Fatalf("node edges = %v %v", edges, err)
	}

	// Batches
	nodes, err := s.GetNodesBatch(ctx, []string{"A", "Z"})
	if err != nil || len(nodes) != 1 {
		t.Fatalf("nodes batch = %d %v", len(nodes), err)
	}
	edgesMap, err := s.GetEdgesBatch(ctx, [][2]string{{"A", "B"}, {"X", "Y"}})
	if err != nil || len(edgesMap) != 1 {
		t.Fatalf("edges batch = %d %v", len(edgesMap), err)
	}
	batch, err := s.GetNodeEdgesBatch(ctx, []string{"A", "Z"})
	if err != nil {
		t.Fatalf("node edges batch: %v", err)
	}
	if len(batch["A"]) != 1 {
		t.Fatalf("batch A edges = %v", batch["A"])
	}
	degs, err := s.NodeDegreesBatch(ctx, []string{"A", "B", "Z"})
	if err != nil {
		t.Fatal(err)
	}
	if degs["A"] != 1 || degs["B"] != 1 {
		t.Fatalf("degrees = %v", degs)
	}

	// RemoveByItem
	if err := s.RemoveByItem(ctx, "i1"); err != nil {
		t.Fatalf("remove by item: %v", err)
	}

	// edgeKeyString ordering
	if edgeKeyString("b", "a") != edgeKeyString("a", "b") {
		t.Errorf("edgeKeyString not canonical")
	}

	// parseGraphTime
	if parseGraphTime("2026-01-02 03:04:05").IsZero() {
		t.Errorf("space format should parse")
	}
	if parseGraphTime("2026-01-02T03:04:05Z").IsZero() {
		t.Errorf("RFC3339 should parse")
	}
	if !parseGraphTime("").IsZero() {
		t.Errorf("empty should be zero")
	}
	if !parseGraphTime("garbage").IsZero() {
		t.Errorf("garbage should be zero")
	}
}

func TestSQLiteGraphStore_ValidationAndNil(t *testing.T) {
	var s *SQLiteGraphStore
	ctx := t.Context()
	if err := s.Init(ctx); err == nil {
		t.Errorf("nil store init should error")
	}
	db := newTestMemoryDB(t)
	if err := EnsureKnowledgeGraphSchema(db); err != nil {
		t.Fatal(err)
	}
	s2 := NewSQLiteGraphStore(db)
	if err := s2.UpsertNode(ctx, nil); err == nil {
		t.Errorf("nil entity should error")
	}
	if err := s2.UpsertNode(ctx, &Entity{Name: "  "}); err == nil {
		t.Errorf("empty name should error")
	}
	if err := s2.UpsertEdge(ctx, nil); err == nil {
		t.Errorf("nil relation should error")
	}
	if err := s2.UpsertEdge(ctx, &Relation{SrcID: "A"}); err == nil {
		t.Errorf("missing tgt should error")
	}
	if _, err := s2.GetNode(ctx, "missing"); err != nil {
		t.Errorf("missing node = %v", err)
	}
	if _, err := s2.GetEdge(ctx, "missing", "node"); err != nil {
		t.Errorf("missing edge = %v", err)
	}

	// EnsureKnowledgeGraphSchema nil db
	if err := EnsureKnowledgeGraphSchema(nil); err == nil {
		t.Errorf("nil db schema should error")
	}
}

func TestMergeChunkIDs(t *testing.T) {
	out, err := mergeChunkIDs(`["c1","c2"]`, []string{"c2", "c3", ""})
	if err != nil {
		t.Fatal(err)
	}
	if out != `["c1","c2","c3"]` {
		t.Fatalf("merge = %s", out)
	}
	// invalid existing json tolerated
	out2, err := mergeChunkIDs("not-json", []string{"c1"})
	if err != nil || out2 != `["c1"]` {
		t.Fatalf("invalid json merge = %s %v", out2, err)
	}
	// empty existing
	out3, err := mergeChunkIDs("", []string{"c1"})
	if err != nil || out3 != `["c1"]` {
		t.Fatalf("empty existing = %s %v", out3, err)
	}
}

func TestSortedUniqueStrings(t *testing.T) {
	got := sortedUniqueStrings([]string{"b", "a", "b", "", "a"})
	// 实现不去除空串：["", "a", "b"]。
	if len(got) != 3 || got[0] != "" || got[1] != "a" || got[2] != "b" {
		t.Fatalf("got %v, want [ a b]", got)
	}
}

func TestNormalizeBackendName(t *testing.T) {
	if normalizeBackendName("") != "sqlite" {
		t.Errorf("empty -> sqlite")
	}
	if normalizeBackendName("memory") != "memory" {
		t.Errorf("passthrough")
	}
}

// ---- graph_store_memory.go remaining branches ----

func TestMemoryGraphStore_EdgeExtras(t *testing.T) {
	ctx := t.Context()
	m := NewMemoryGraphStore()
	if m.Backend() != "memory" {
		t.Fatalf("backend")
	}
	if err := m.IndexDoneCallback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if err := m.UpsertEdge(ctx, &Relation{SrcID: "A", TgtID: "B", Weight: -2.0}); err != nil {
		t.Fatal(err)
	}
	e, _ := m.GetEdge(ctx, "A", "B")
	if e == nil || e.ID == "" {
		t.Fatalf("edge id should be auto-filled: %+v", e)
	}
	// negative weight clamped on merge
	if err := m.UpsertEdge(ctx, &Relation{SrcID: "A", TgtID: "B", Weight: -5.0}); err != nil {
		t.Fatal(err)
	}
	e2, _ := m.GetEdge(ctx, "A", "B")
	if e2.Weight < 0 {
		t.Fatalf("weight should clamp to 0, got %v", e2.Weight)
	}
	if err := m.UpsertNode(ctx, nil); err == nil {
		t.Fatalf("nil node should error")
	}
	if err := m.UpsertEdge(ctx, &Relation{SrcID: " "}); err == nil {
		t.Fatalf("empty src should error")
	}
	// mergeStringSlicesUnique with empty entries
	got := mergeStringSlicesUnique([]string{"a", "", "b"}, []string{"", "b", "c"})
	if len(got) != 3 {
		t.Fatalf("merge = %v", got)
	}
}

// ---- graph_vector_index.go ----

func TestGraphVectorIndex_UpsertAndSearch(t *testing.T) {
	db := newTestMemoryDB(t)
	if err := EnsureKnowledgeGraphSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := EnsureGraphVectorSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := EnsureGraphVectorSchema(nil); err == nil {
		t.Fatalf("nil db should error")
	}
	if _, err := NewGraphVectorIndex(nil, nil, nil); err == nil {
		t.Fatalf("nil db should error")
	}
	if _, err := NewGraphVectorIndex(db, nil, nil); err == nil {
		t.Fatalf("nil embedder should error")
	}

	srv := graphTestEmbedderServer(t)
	e := newTestGraphEmbedder(t, srv.URL)
	g, err := NewGraphVectorIndex(db, e, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()

	ent := &Entity{Name: "SQL注入", Type: "漏洞", Description: "注入攻击", SourceID: "item-1", ChunkIDs: []string{"item-1#0"}}
	if err := g.UpsertEntity(ctx, ent); err != nil {
		t.Fatalf("upsert entity: %v", err)
	}
	if err := g.UpsertEntity(ctx, &Entity{Name: " "}); err == nil {
		t.Fatalf("empty name should error")
	}

	rel := &Relation{SrcID: "SQL注入", TgtID: "认证绕过", Keywords: "导致", Description: "desc", SourceID: "item-1", ChunkIDs: []string{"item-1#0"}}
	if err := g.UpsertRelation(ctx, rel); err != nil {
		t.Fatalf("upsert relation: %v", err)
	}
	if err := g.UpsertRelation(ctx, &Relation{SrcID: " "}); err == nil {
		t.Fatalf("empty src should error")
	}

	// idempotent upsert (delete-then-insert)
	if err := g.UpsertEntity(ctx, ent); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}

	hits, err := g.SearchEntities(ctx, "SQL注入", 5, 0.0)
	if err != nil {
		t.Fatalf("search entities: %v", err)
	}
	if len(hits) != 1 || hits[0].Name != "SQL注入" {
		t.Fatalf("hits = %+v", hits)
	}
	if hits[0].SourceID != "item-1" || len(hits[0].ChunkIDs) != 1 {
		t.Fatalf("hit metadata = %+v", hits[0])
	}

	rhits, err := g.SearchRelations(ctx, "导致", 5, 0.0)
	if err != nil {
		t.Fatalf("search relations: %v", err)
	}
	if len(rhits) != 1 || rhits[0].SrcName != "SQL注入" {
		t.Fatalf("relation hits = %+v", rhits)
	}

	// validation errors
	if _, err := g.SearchEntities(ctx, "", 5, 0); err == nil {
		t.Fatalf("empty query should error")
	}
	if _, err := g.SearchRelations(ctx, "", 5, 0); err == nil {
		t.Fatalf("empty keywords should error")
	}
	if err := g.RemoveByItem(ctx, "item-1"); err != nil {
		t.Fatalf("remove by item: %v", err)
	}
	hits2, err := g.SearchEntities(ctx, "SQL注入", 5, 0.0)
	if err != nil || len(hits2) != 0 {
		t.Fatalf("after remove: %d %v", len(hits2), err)
	}

	// format helpers
	if formatEntityEmbeddingInput(ent) == "" {
		t.Errorf("entity embed input empty")
	}
	if formatRelationEmbeddingInput(rel) == "" {
		t.Errorf("relation embed input empty")
	}
}

func TestGraphVectorIndex_SearchErrors(t *testing.T) {
	db := newTestMemoryDB(t)
	srv := graphTestEmbedderServer(t)
	e := newTestGraphEmbedder(t, srv.URL)
	g, err := NewGraphVectorIndex(db, e, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsureGraphVectorSchema(db); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := g.UpsertEntity(ctx, &Entity{Name: "X", Description: "y"}); err != nil {
		t.Fatal(err)
	}
	// Embedder failure -> search error.
	srvDead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400) }))
	srvDead.Close()
	eBad := newTestGraphEmbedder(t, srvDead.URL)
	gBad, err := NewGraphVectorIndex(db, eBad, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gBad.SearchEntities(ctx, "q", 5, 0); err == nil {
		t.Fatalf("embedder failure should error")
	}
	if _, err := gBad.SearchRelations(ctx, "q", 5, 0); err == nil {
		t.Fatalf("embedder failure should error")
	}
}

// ---- graph_indexer.go ----

func TestGraphIndexer_ExtractAndIndex(t *testing.T) {
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	if err := EnsureGraphVectorSchema(db); err != nil {
		t.Fatal(err)
	}
	at := fixedTestTime()
	insertKnowledgeItem(t, db, "item-1", "XSS", "注入防御", "/p/a.md", "# SQL注入\n\nSQL注入 影响认证系统。\n参数化查询 缓解 SQL注入。CVE-2021-1234 是严重漏洞。", at)

	store := NewMemoryGraphStore()
	if err := store.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	srv := graphTestEmbedderServer(t)
	e := newTestGraphEmbedder(t, srv.URL)
	vecIndex, err := NewGraphVectorIndex(db, e, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	extractor := NewGraphExtractor(nil, zap.NewNop())

	gi, err := NewGraphIndexer(db, store, vecIndex, extractor, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("NewGraphIndexer: %v", err)
	}
	if err := gi.EnsureSchema(t.Context()); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}

	snap, err := gi.ExtractAndIndex(t.Context(), "item-1")
	if err != nil {
		t.Fatalf("ExtractAndIndex: %v", err)
	}
	if len(snap.Entities) == 0 || len(snap.Relations) == 0 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if snap.ChunkCount == 0 {
		t.Fatalf("chunk count = 0")
	}

	// idempotent: second run clears old and rewrites.
	snap2, err := gi.ExtractAndIndex(t.Context(), "item-1")
	if err != nil {
		t.Fatalf("re-extract: %v", err)
	}
	if len(snap2.Entities) != len(snap.Entities) {
		t.Fatalf("not idempotent: %d vs %d", len(snap2.Entities), len(snap.Entities))
	}

	// validation errors
	if _, err := gi.ExtractAndIndex(t.Context(), "  "); err == nil {
		t.Fatalf("empty itemID should error")
	}
	if _, err := gi.ExtractAndIndex(t.Context(), "missing"); err == nil {
		t.Fatalf("missing item should error")
	}
	_, err = NewGraphIndexer(nil, store, vecIndex, extractor, nil, nil)
	if err == nil {
		t.Fatalf("nil db should error")
	}
	_, err = NewGraphIndexer(db, nil, vecIndex, extractor, nil, nil)
	if err == nil {
		t.Fatalf("nil store should error")
	}
	_, err = NewGraphIndexer(db, store, nil, extractor, nil, nil)
	if err == nil {
		t.Fatalf("nil vec index should error")
	}
	_, err = NewGraphIndexer(db, store, vecIndex, nil, nil, nil)
	if err == nil {
		t.Fatalf("nil extractor should error")
	}

	// status
	running, _, _, _, start, lastItem := gi.GetStatus()
	if running {
		t.Fatalf("status should not be running after done")
	}
	if lastItem != "item-1" {
		t.Fatalf("lastItem = %q", lastItem)
	}
	_ = start
}

func TestGraphIndexer_IndexMissingAndRebuild(t *testing.T) {
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	if err := EnsureGraphVectorSchema(db); err != nil {
		t.Fatal(err)
	}
	at := fixedTestTime()
	insertKnowledgeItem(t, db, "item-1", "XSS", "t", "/p", "# SQL注入\n\nSQL注入 影响 认证系统。", at)
	insertKnowledgeItem(t, db, "item-2", "SQLi", "t2", "/p/b.md", "# SQL注入2\n\nSQL注入2 影响 认证系统2。", at)

	store := NewMemoryGraphStore()
	srv := graphTestEmbedderServer(t)
	e := newTestGraphEmbedder(t, srv.URL)
	vecIndex, err := NewGraphVectorIndex(db, e, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	gi, err := NewGraphIndexer(db, store, vecIndex, NewGraphExtractor(nil, zap.NewNop()), nil, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}

	if err := gi.IndexMissing(t.Context()); err != nil {
		t.Fatalf("IndexMissing: %v", err)
	}
	if err := gi.RebuildIndex(t.Context()); err != nil {
		t.Fatalf("RebuildIndex: %v", err)
	}
	// concurrency guard
	running, _, _, _, _, _ := gi.GetStatus()
	_ = running
	if err := gi.IndexMissing(t.Context()); err != nil {
		t.Fatalf("second IndexMissing: %v", err)
	}
}

// ---- graph_retriever.go (with real embedder mock) ----

func TestGraphRetriever_SearchPaths(t *testing.T) {
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	if err := EnsureGraphVectorSchema(db); err != nil {
		t.Fatal(err)
	}
	at := fixedTestTime()
	insertKnowledgeItem(t, db, "item-1", "XSS", "t", "/p", "正文", at)
	// chunk row for collectChunks/loadChunkByID
	_, err := db.Exec(`INSERT INTO knowledge_embeddings (id, item_id, chunk_index, chunk_text, embedding, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"item-1#0", "item-1", 0, "chunk text", "[1,2,3]", at)
	if err != nil {
		t.Fatal(err)
	}

	store := NewMemoryGraphStore()
	srv := graphTestEmbedderServer(t)
	e := newTestGraphEmbedder(t, srv.URL)
	vecIndex, err := NewGraphVectorIndex(db, e, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if err := vecIndex.UpsertEntity(t.Context(), &Entity{Name: "SQL注入", Type: "漏洞", Description: "d", SourceID: "item-1", ChunkIDs: []string{"item-1#0"}}); err != nil {
		t.Fatal(err)
	}
	if err := vecIndex.UpsertRelation(t.Context(), &Relation{SrcID: "SQL注入", TgtID: "认证绕过", Keywords: "导致", Description: "d", SourceID: "item-1", ChunkIDs: []string{"item-1#0"}}); err != nil {
		t.Fatal(err)
	}

	// base retriever for chunk fallback (vector search on knowledge_embeddings)
	baseSrv := graphTestEmbedderServer(t)
	baseEmb := newTestGraphEmbedder(t, baseSrv.URL)
	// seed knowledge_embeddings vector rows with matching dim
	embJSON, _ := json.Marshal([]float32{0.6, 0.6, 0.6})
	_, err = db.Exec(`UPDATE knowledge_embeddings SET embedding=?, embedding_dim=3 WHERE id='item-1#0'`, string(embJSON))
	if err != nil {
		t.Fatal(err)
	}
	baseRetriever := NewRetriever(db, baseEmb, &RetrievalConfig{TopK: 5, SimilarityThreshold: 0.0}, zap.NewNop())

	gr, err := NewGraphRetriever(store, vecIndex, baseRetriever, zap.NewNop())
	if err != nil {
		t.Fatalf("NewGraphRetriever: %v", err)
	}

	for _, mode := range []GraphSearchMode{GraphSearchLocal, GraphSearchHybrid} {
		res, err := gr.Search(t.Context(), &GraphSearchRequest{Query: "SQL注入", Mode: mode, TopK: 5, RiskType: "XSS"})
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if len(res.Entities) == 0 && len(res.Relations) == 0 {
			t.Errorf("%s: no hits", mode)
		}
	}
	// global 模式走关系向量召回，需要边在 GraphStore 中存在才返回 relations；
	// 先写图边再检索。
	if err := store.UpsertEdge(t.Context(), &Relation{SrcID: "SQL注入", TgtID: "认证绕过", Keywords: "导致", Description: "d", SourceID: "item-1", ChunkIDs: []string{"item-1#0"}}); err != nil {
		t.Fatal(err)
	}
	resGlobal, err := gr.Search(t.Context(), &GraphSearchRequest{Query: "导致", Mode: GraphSearchGlobal, TopK: 5, RiskType: "XSS"})
	if err != nil {
		t.Fatalf("global: %v", err)
	}
	if len(resGlobal.Relations) == 0 {
		t.Errorf("global: no relation hits")
	}

	// unsupported mode
	if _, err := gr.Search(t.Context(), &GraphSearchRequest{Query: "q", Mode: "nope"}); err == nil {
		t.Fatalf("unsupported mode should error")
	}
	// empty query
	if _, err := gr.Search(t.Context(), &GraphSearchRequest{Query: " "}); err == nil {
		t.Fatalf("empty query should error")
	}
	// nil request
	if _, err := gr.Search(t.Context(), nil); err == nil {
		t.Fatalf("nil request should error")
	}
	// nil receiver
	var grNil *GraphRetriever
	if _, err := grNil.Search(t.Context(), &GraphSearchRequest{Query: "q"}); err == nil {
		t.Fatalf("nil receiver should error")
	}
	// constructor validation
	if _, err := NewGraphRetriever(nil, vecIndex, nil, nil); err == nil {
		t.Fatalf("nil store should error")
	}
	if _, err := NewGraphRetriever(store, nil, nil, nil); err == nil {
		t.Fatalf("nil vec index should error")
	}
}

func TestGraphRetriever_DedupeHelpers(t *testing.T) {
	// dedupeEdgePairs
	batch := map[string][][2]string{
		"A": {{"A", "B"}, {"A", "C"}},
		"B": {{"B", "A"}},
	}
	pairs := dedupeEdgePairs(batch, []string{"A", "B"})
	if len(pairs) != 2 {
		t.Fatalf("pairs = %v", pairs)
	}

	// dedupeRetrievalResults
	r1 := &RetrievalResult{Chunk: &KnowledgeChunk{ID: "c1"}}
	r2 := &RetrievalResult{Chunk: &KnowledgeChunk{ID: "c2"}}
	r3 := &RetrievalResult{Chunk: &KnowledgeChunk{ID: "c1"}}
	out := dedupeRetrievalResults([]*RetrievalResult{r1, nil, r2}, []*RetrievalResult{r3})
	if len(out) != 2 {
		t.Fatalf("dedupe = %d", len(out))
	}
}

func TestGraphRetriever_LocalEntityMissingInStore(t *testing.T) {
	// Hit from vector index but missing in store -> synthesized entity.
	db := newTestMemoryDB(t)
	if err := EnsureGraphVectorSchema(db); err != nil {
		t.Fatal(err)
	}
	srv := graphTestEmbedderServer(t)
	e := newTestGraphEmbedder(t, srv.URL)
	vecIndex, err := NewGraphVectorIndex(db, e, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if err := vecIndex.UpsertEntity(t.Context(), &Entity{Name: "孤儿实体", Description: "d", SourceID: "i9", ChunkIDs: []string{"i9#0"}}); err != nil {
		t.Fatal(err)
	}
	// store is empty -> node not found branch.
	store := NewMemoryGraphStore()
	gr, err := NewGraphRetriever(store, vecIndex, nil, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	res, err := gr.Search(t.Context(), &GraphSearchRequest{Query: "孤儿实体", Mode: GraphSearchLocal, TopK: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entities) != 1 || res.Entities[0].Name != "孤儿实体" {
		t.Fatalf("entities = %+v", res.Entities)
	}
}

// ---- graph_service.go ----

func TestGraphService_ConstructAndDelegate(t *testing.T) {
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	if err := EnsureGraphVectorSchema(db); err != nil {
		t.Fatal(err)
	}
	at := fixedTestTime()
	insertKnowledgeItem(t, db, "item-1", "XSS", "t", "/p", "# SQL注入\n\nSQL注入 影响 认证系统。", at)

	srv := graphTestEmbedderServer(t)
	e := newTestGraphEmbedder(t, srv.URL)

	gs, err := NewGraphService(t.Context(), db, config.GraphConfig{Backend: "memory"}, e, nil, nil, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("NewGraphService: %v", err)
	}
	if gs.Backend() != "memory" {
		t.Fatalf("backend = %q", gs.Backend())
	}
	if gs.Store() == nil {
		t.Fatalf("store nil")
	}
	snap, err := gs.IndexItem(t.Context(), "item-1")
	if err != nil {
		t.Fatalf("IndexItem: %v", err)
	}
	if len(snap.Entities) == 0 {
		t.Fatalf("snapshot empty")
	}
	if err := gs.IndexMissing(t.Context()); err != nil {
		t.Fatalf("IndexMissing: %v", err)
	}
	if err := gs.RebuildIndex(t.Context()); err != nil {
		t.Fatalf("RebuildIndex: %v", err)
	}
	res, err := gs.Search(t.Context(), &GraphSearchRequest{Query: "SQL注入"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res == nil {
		t.Fatalf("search nil")
	}
	// default mode via nil req would fail (empty query); assert that behavior.
	if _, err := gs.Search(t.Context(), &GraphSearchRequest{Query: ""}); err == nil {
		t.Fatalf("empty query should error")
	}
	// nil req is treated as empty query too.
	if _, err := gs.Search(t.Context(), nil); err == nil {
		t.Fatalf("nil req should error (empty query)")
	}
	running, total, current, failed, lastItem := gs.GetStatus()
	_, _, _, _ = running, total, current, failed
	_ = lastItem

	// sqlite backend variant
	gs2, err := NewGraphService(t.Context(), db, config.GraphConfig{}, e, nil, nil, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("sqlite backend: %v", err)
	}
	if gs2.Backend() != "sqlite" {
		t.Fatalf("backend = %q", gs2.Backend())
	}

	// validation
	if _, err := NewGraphService(t.Context(), nil, config.GraphConfig{}, e, nil, nil, nil, nil); err == nil {
		t.Fatalf("nil db should error")
	}
	if _, err := NewGraphService(t.Context(), db, config.GraphConfig{}, nil, nil, nil, nil, nil); err == nil {
		t.Fatalf("nil embedder should error")
	}
	// llm factory injects extractor
	gs3, err := NewGraphService(t.Context(), db, config.GraphConfig{Backend: "memory", UseLLMExtractor: true}, e, nil, nil, func() LLMGraphExtractor {
		return &stubLLMExtractor{}
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("llm factory: %v", err)
	}
	_ = gs3
}

func TestGraphSearchModeString(t *testing.T) {
	if GraphSearchHybrid.String() != "hybrid" {
		t.Fatalf("String = %q", GraphSearchHybrid.String())
	}
}

func TestNormalizeGraphEntityTypes(t *testing.T) {
	if len(normalizeGraphEntityTypes(nil)) == 0 {
		t.Fatalf("default types expected")
	}
	if got := normalizeGraphEntityTypes([]string{"", "  ", "X"}); len(got) != 1 || got[0] != "X" {
		t.Fatalf("custom = %v", got)
	}
}

// scanStringColumn via indexer helper
func TestScanStringColumn(t *testing.T) {
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	at := fixedTestTime()
	insertKnowledgeItem(t, db, "1", "XSS", "t", "/p", "c", at)
	rows, err := db.Query(`SELECT id FROM knowledge_base_items`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	ids, err := scanStringColumn(rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "1" {
		t.Fatalf("ids = %v", ids)
	}
	_ = fmt.Sprint
	_ = schema.Document{}
	_ = context.Background
	_ = sql.ErrNoRows
}
