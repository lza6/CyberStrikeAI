package knowledge

import (
	"context"
	"testing"

	"cyberstrike-ai/internal/config"
)

// TestGraphStoreBackendSwitch 验收点 1：图存储抽象后端可换（SQLite ↔ memory）。
// 两后端必须行为一致：upsert 合并、删除幂等、度数正确。
//
// 注意：SQLite 后端依赖 cgo（go-sqlite3）。无 cgo 环境（CI/沙箱）时自动跳过 sqlite 子用例，
// memory 子用例始终运行，保证抽象层契约至少有一份实现被验证。
func TestGraphStoreBackendSwitch(t *testing.T) {
	ctx := context.Background()

	// memory 后端
	mem := NewMemoryGraphStore()
	if err := mem.Init(ctx); err != nil {
		t.Fatalf("memory init: %v", err)
	}
	t.Run("memory_backend_upsert_merge_and_degree", func(t *testing.T) {
		verifyGraphStoreContract(t, mem)
	})

	// SQLite 后端（内存 DB）—— cgo 不可用时跳过
	sqlite, cleanup := newSQLiteGraphStoreForTest(t)
	defer cleanup()
	if err := sqlite.Init(ctx); err != nil {
		t.Logf("sqlite init: %v（无 cgo 环境，跳过 sqlite 后端用例）", err)
		return
	}
	t.Run("sqlite_backend_upsert_merge_and_degree", func(t *testing.T) {
		verifyGraphStoreContract(t, sqlite)
	})

	if mem.Backend() != "memory" {
		t.Errorf("memory backend name = %q, want %q", mem.Backend(), "memory")
	}
	if sqlite.Backend() != "sqlite" {
		t.Errorf("sqlite backend name = %q, want %q", sqlite.Backend(), "sqlite")
	}
}

// verifyGraphStoreContract 验证 GraphStore 契约行为一致性。
// 各后端实现必须通过此契约，方可视为可互换。
func verifyGraphStoreContract(t *testing.T, s GraphStore) {
	t.Helper()
	ctx := context.Background()

	// 清场
	if err := s.Drop(ctx); err != nil {
		t.Fatalf("drop: %v", err)
	}

	// upsert 节点（首次）
	e1 := &Entity{
		Name: "SQL注入", Type: "漏洞",
		Description: "通过拼接 SQL 造成注入", SourceID: "item-1", ChunkIDs: []string{"c1"},
	}
	if err := s.UpsertNode(ctx, e1); err != nil {
		t.Fatalf("upsert node e1: %v", err)
	}
	// upsert 同名节点（合并 description + chunk_ids）
	e2 := &Entity{
		Name: "SQL注入", Type: "漏洞",
		Description: "二次注入也是注入", SourceID: "item-2", ChunkIDs: []string{"c2"},
	}
	if err := s.UpsertNode(ctx, e2); err != nil {
		t.Fatalf("upsert node e2: %v", err)
	}
	node, err := s.GetNode(ctx, "SQL注入")
	if err != nil || node == nil {
		t.Fatalf("get node after merge: %v %v", node, err)
	}
	if len(node.ChunkIDs) != 2 {
		t.Errorf("merged chunk_ids len = %d, want 2", len(node.ChunkIDs))
	}
	if node.Description == "" {
		t.Errorf("merged description empty")
	}

	// upsert 边（首次）
	r1 := &Relation{
		SrcID: "SQL注入", TgtID: "认证绕过", Keywords: "导致",
		Description: "SQL注入可导致认证绕过", Weight: 1.0, SourceID: "item-1", ChunkIDs: []string{"c1"},
	}
	if err := s.UpsertEdge(ctx, r1); err != nil {
		t.Fatalf("upsert edge r1: %v", err)
	}
	// upsert 同边（累加 weight + 合并 chunk_ids）
	r2 := &Relation{
		SrcID: "SQL注入", TgtID: "认证绕过", Keywords: "导致",
		Description: "另一处也提到", Weight: 1.5, SourceID: "item-2", ChunkIDs: []string{"c2"},
	}
	if err := s.UpsertEdge(ctx, r2); err != nil {
		t.Fatalf("upsert edge r2: %v", err)
	}
	edge, err := s.GetEdge(ctx, "SQL注入", "认证绕过")
	if err != nil || edge == nil {
		t.Fatalf("get edge after merge: %v %v", edge, err)
	}
	if edge.Weight != 2.5 {
		t.Errorf("merged weight = %v, want 2.5", edge.Weight)
	}
	if len(edge.ChunkIDs) != 2 {
		t.Errorf("merged edge chunk_ids len = %d, want 2", len(edge.ChunkIDs))
	}

	// HasNode / HasEdge
	has, err := s.HasNode(ctx, "SQL注入")
	if err != nil || !has {
		t.Errorf("HasNode SQL注入 = %v %v", has, err)
	}
	has, err = s.HasEdge(ctx, "SQL注入", "认证绕过")
	if err != nil || !has {
		t.Errorf("HasEdge = %v %v", has, err)
	}
	has, err = s.HasNode(ctx, "不存在")
	if err != nil || has {
		t.Errorf("HasNode 不存在 should be false, got %v %v", has, err)
	}

	// NodeDegree（SQL注入 与 认证绕过 均度数 1）
	d, err := s.NodeDegree(ctx, "SQL注入")
	if err != nil || d != 1 {
		t.Errorf("NodeDegree SQL注入 = %d %v, want 1", d, err)
	}

	// RemoveByItem（item-1 后，节点/边应不再含 item-1 的 chunk，但合并后节点仍存在）
	if err := s.RemoveByItem(ctx, "item-1"); err != nil {
		t.Fatalf("remove by item-1: %v", err)
	}
	// Drop 全清
	if err := s.Drop(ctx); err != nil {
		t.Fatalf("drop: %v", err)
	}
	has, _ = s.HasNode(ctx, "SQL注入")
	if has {
		t.Errorf("after drop, HasNode should be false")
	}
}

// TestGraphStoreBatchOps 验收：批量接口 GetNodesBatch/GetEdgesBatch/NodeDegreesBatch 行为一致。
// 无 cgo 环境时仅验证 memory 后端（sqlite 自动跳过）。
func TestGraphStoreBatchOps(t *testing.T) {
	ctx := context.Background()
	mem := NewMemoryGraphStore()
	if err := mem.Init(ctx); err != nil {
		t.Fatalf("memory init: %v", err)
	}
	backends := []GraphStore{mem}
	if sqlite, cleanup := newSQLiteGraphStoreForTest(t); sqlite != nil {
		if err := sqlite.Init(ctx); err == nil {
			backends = append(backends, sqlite)
			t.Cleanup(func() { cleanup() })
		} else {
			t.Logf("sqlite init: %v（无 cgo 环境，仅验证 memory）", err)
		}
	}
	for _, s := range backends {
		_ = s.Drop(ctx)
		_ = s.UpsertNode(ctx, &Entity{Name: "A", Type: "漏洞", SourceID: "i1"})
		_ = s.UpsertNode(ctx, &Entity{Name: "B", Type: "漏洞", SourceID: "i1"})
		_ = s.UpsertNode(ctx, &Entity{Name: "C", Type: "漏洞", SourceID: "i1"})
		_ = s.UpsertEdge(ctx, &Relation{SrcID: "A", TgtID: "B", Weight: 1.0})
		_ = s.UpsertEdge(ctx, &Relation{SrcID: "B", TgtID: "C", Weight: 1.0})
		_ = s.UpsertEdge(ctx, &Relation{SrcID: "C", TgtID: "A", Weight: 1.0})

		nodes, err := s.GetNodesBatch(ctx, []string{"A", "B", "missing"})
		if err != nil {
			t.Fatalf("GetNodesBatch: %v", err)
		}
		if len(nodes) != 2 {
			t.Errorf("GetNodesBatch len = %d, want 2 (missing excluded)", len(nodes))
		}
		edges, err := s.GetEdgesBatch(ctx, [][2]string{{"A", "B"}, {"B", "C"}, {"X", "Y"}})
		if err != nil {
			t.Fatalf("GetEdgesBatch: %v", err)
		}
		if len(edges) != 2 {
			t.Errorf("GetEdgesBatch len = %d, want 2", len(edges))
		}
		deg, err := s.NodeDegreesBatch(ctx, []string{"A", "B", "C"})
		if err != nil {
			t.Fatalf("NodeDegreesBatch: %v", err)
		}
		// 每个节点都连两条边（A-B, B-C, C-A），度数应均为 2
		for _, n := range []string{"A", "B", "C"} {
			if deg[n] != 2 {
				t.Errorf("NodeDegreesBatch[%s] = %d, want 2 (backend=%s)", n, deg[n], s.Backend())
			}
		}
		_ = s.Drop(ctx)
	}
}

// mustSQLiteGraphStore 构造一个基于内存 SQLite 的 SQLiteGraphStore（测试用）。
func mustSQLiteGraphStore(t *testing.T) GraphStore {
	s, cleanup := newSQLiteGraphStoreForTest(t)
	t.Cleanup(func() { cleanup() })
	return s
}

// newSQLiteGraphStoreForTest 构造一个基于内存 SQLite 的 SQLiteGraphStore + cleanup。
func newSQLiteGraphStoreForTest(t *testing.T) (*SQLiteGraphStore, func()) {
	t.Helper()
	db := newMemoryDB(t)
	s := NewSQLiteGraphStore(db)
	return s, func() { _ = db.Close() }
}

// TestGraphConfigEffective 验收：GraphConfig 归一化方法行为。
func TestGraphConfigEffective(t *testing.T) {
	c := config.GraphConfig{}
	if c.EffectiveBackend() != "sqlite" {
		t.Errorf("default backend = %q, want sqlite", c.EffectiveBackend())
	}
	if c.EffectiveDefaultSearchMode() != "hybrid" {
		t.Errorf("default mode = %q, want hybrid", c.EffectiveDefaultSearchMode())
	}
	if c.EffectiveTopK(7) != 7 {
		t.Errorf("fallback topk = %d, want 7", c.EffectiveTopK(7))
	}
	if c.EffectiveSimilarityThreshold() != 0.2 {
		t.Errorf("default threshold = %v, want 0.2", c.EffectiveSimilarityThreshold())
	}
	c2 := config.GraphConfig{Backend: "memory", DefaultSearchMode: "local", TopK: 10, SimilarityThreshold: 0.5}
	if c2.EffectiveBackend() != "memory" {
		t.Errorf("backend = %q, want memory", c2.EffectiveBackend())
	}
	if c2.EffectiveDefaultSearchMode() != "local" {
		t.Errorf("mode = %q, want local", c2.EffectiveDefaultSearchMode())
	}
	if c2.EffectiveTopK(0) != 10 {
		t.Errorf("topk = %d, want 10", c2.EffectiveTopK(0))
	}
	if c2.EffectiveSimilarityThreshold() != 0.5 {
		t.Errorf("threshold = %v, want 0.5", c2.EffectiveSimilarityThreshold())
	}
}
