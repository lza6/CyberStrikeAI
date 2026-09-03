package knowledge

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

func TestManager_ScanKnowledgeBase(t *testing.T) {
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)

	dir := t.TempDir()
	// Create nested markdown files under a category dir.
	catDir := filepath.Join(dir, "XSS")
	if err := os.MkdirAll(catDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(catDir, "a.md"), []byte("# 标题\n内容"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.md"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	// non-md file ignored
	if err := os.WriteFile(filepath.Join(dir, "c.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	m := NewManager(db, dir, zap.NewNop())
	ids, err := m.ScanKnowledgeBase()
	if err != nil {
		t.Fatalf("ScanKnowledgeBase: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2 items to index, got %d", len(ids))
	}

	// Re-scan: no changes -> no new ids.
	ids2, err := m.ScanKnowledgeBase()
	if err != nil {
		t.Fatalf("rescan: %v", err)
	}
	if len(ids2) != 0 {
		t.Fatalf("expected 0 unchanged items, got %d", len(ids2))
	}

	// Modify a file -> it should appear again.
	if err := os.WriteFile(filepath.Join(catDir, "a.md"), []byte("# 标题\n修改后的内容"), 0644); err != nil {
		t.Fatal(err)
	}
	ids3, err := m.ScanKnowledgeBase()
	if err != nil {
		t.Fatalf("modify rescan: %v", err)
	}
	if len(ids3) != 1 {
		t.Fatalf("expected 1 changed item, got %d", len(ids3))
	}
}

func TestManager_ScanKnowledgeBaseEmptyPath(t *testing.T) {
	m := NewManager(nil, "", nil)
	if _, err := m.ScanKnowledgeBase(); err == nil {
		t.Fatalf("empty base path should error")
	}
}

func TestManager_GetCategories(t *testing.T) {
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	at := fixedTestTime()
	insertKnowledgeItem(t, db, "1", "XSS", "t1", "/p/a.md", "a", at)
	insertKnowledgeItem(t, db, "2", "SQLi", "t2", "/p/b.md", "b", at)
	insertKnowledgeItem(t, db, "3", "XSS", "t3", "/p/c.md", "c", at)

	m := NewManager(db, "", nil)
	cats, err := m.GetCategories()
	if err != nil {
		t.Fatalf("GetCategories: %v", err)
	}
	if len(cats) != 2 {
		t.Fatalf("expected 2 categories, got %v", cats)
	}
	if cats[0] != "SQLi" || cats[1] != "XSS" {
		t.Fatalf("unexpected order: %v", cats)
	}
}

func TestManager_GetStats(t *testing.T) {
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	at := fixedTestTime()
	insertKnowledgeItem(t, db, "1", "XSS", "t1", "/p/a.md", "a", at)
	insertKnowledgeItem(t, db, "2", "XSS", "t2", "/p/b.md", "b", at)

	m := NewManager(db, "", nil)
	cats, items, err := m.GetStats()
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if cats != 1 || items != 2 {
		t.Fatalf("stats = %d cats, %d items", cats, items)
	}
}

func TestManager_GetItemsWithOptions(t *testing.T) {
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	at := fixedTestTime()
	insertKnowledgeItem(t, db, "1", "XSS", "b", "/p/b.md", "bb", at)
	insertKnowledgeItem(t, db, "2", "SQLi", "a", "/p/a.md", "aa", at)
	insertKnowledgeItem(t, db, "3", "XSS", "c", "/p/c.md", "cc", at)

	m := NewManager(db, "", nil)

	// include content, all
	items, err := m.GetItems("")
	if err != nil {
		t.Fatalf("GetItems: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}

	// category filter + no content + limit
	sum, err := m.GetItemsWithOptions("XSS", 2, 0, false)
	if err != nil {
		t.Fatalf("GetItemsWithOptions: %v", err)
	}
	if len(sum) != 2 {
		t.Fatalf("expected 2, got %d", len(sum))
	}
	if sum[0].Content != "" {
		t.Errorf("no-content mode should leave Content empty")
	}
}

func TestManager_GetItemsCount(t *testing.T) {
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	at := fixedTestTime()
	insertKnowledgeItem(t, db, "1", "XSS", "t1", "/p/a.md", "a", at)
	insertKnowledgeItem(t, db, "2", "SQLi", "t2", "/p/b.md", "b", at)

	m := NewManager(db, "", nil)
	n, err := m.GetItemsCount("XSS")
	if err != nil || n != 1 {
		t.Fatalf("count XSS = %d, err %v", n, err)
	}
	n, err = m.GetItemsCount("")
	if err != nil || n != 2 {
		t.Fatalf("count all = %d, err %v", n, err)
	}
}

func TestManager_SearchItemsByKeyword(t *testing.T) {
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	at := fixedTestTime()
	insertKnowledgeItem(t, db, "1", "XSS", "跨站脚本攻击", "/p/a.md", "跨站脚本内容 payload", at)
	insertKnowledgeItem(t, db, "2", "SQLi", "注入", "/p/b.md", "sql injection 内容", at)

	m := NewManager(db, "", nil)
	if _, err := m.SearchItemsByKeyword("", ""); err == nil {
		t.Fatalf("empty keyword should error")
	}
	items, err := m.SearchItemsByKeyword("跨站", "")
	if err != nil {
		t.Fatalf("SearchItemsByKeyword: %v", err)
	}
	if len(items) != 1 || items[0].ID != "1" {
		t.Fatalf("expected item 1, got %v", items)
	}
	// category filter narrows
	items2, err := m.SearchItemsByKeyword("内容", "SQLi")
	if err != nil {
		t.Fatalf("search with category: %v", err)
	}
	if len(items2) != 1 || items2[0].ID != "2" {
		t.Fatalf("category-filtered search got %v", items2)
	}
}

func TestManager_GetItemsSummary(t *testing.T) {
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	at := fixedTestTime()
	insertKnowledgeItem(t, db, "1", "XSS", "t1", "/p/a.md", "a", at)
	insertKnowledgeItem(t, db, "2", "XSS", "t2", "/p/b.md", "b", at)

	m := NewManager(db, "", nil)
	items, total, err := m.GetItemsSummary("XSS", 1, 0)
	if err != nil {
		t.Fatalf("GetItemsSummary: %v", err)
	}
	if total != 2 || len(items) != 1 {
		t.Fatalf("summary = %d items, total %d", len(items), total)
	}
	// offset page
	items2, _, err := m.GetItemsSummary("XSS", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(items2) != 1 || items2[0].ID != "2" {
		t.Fatalf("second page got %v", items2)
	}
}

func TestManager_GetItem(t *testing.T) {
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	at := fixedTestTime()
	insertKnowledgeItem(t, db, "1", "XSS", "t1", "/p/a.md", "content", at)

	m := NewManager(db, "", nil)
	item, err := m.GetItem("1")
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.ID != "1" || item.Content != "content" || item.Category != "XSS" {
		t.Fatalf("item = %+v", item)
	}
	if item.UpdatedAt.IsZero() || item.CreatedAt.IsZero() {
		t.Errorf("expected parsed times, got %+v", item)
	}
	// not found
	if _, err := m.GetItem("nope"); err == nil {
		t.Fatalf("missing item should error")
	}
}

func TestManager_CreateUpdateDeleteItem(t *testing.T) {
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	m := NewManager(db, t.TempDir(), nil)

	// Create
	item, err := m.CreateItem("XSS", "指南", "# 跨站脚本\n内容")
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	if item.ID == "" || item.FilePath == "" {
		t.Fatalf("created item missing fields: %+v", item)
	}

	// Update (same path)
	updated, err := m.UpdateItem(item.ID, "XSS", "指南", "# 跨站脚本\n更新内容")
	if err != nil {
		t.Fatalf("UpdateItem: %v", err)
	}
	if updated.Content != "# 跨站脚本\n更新内容" {
		t.Fatalf("update content = %q", updated.Content)
	}

	// Move path with category change -> delete vectors cascades
	updated2, err := m.UpdateItem(item.ID, "SQLi", "注入指南", "new body")
	if err != nil {
		t.Fatalf("UpdateItem move: %v", err)
	}
	if updated2.Category != "SQLi" {
		t.Fatalf("category not updated: %+v", updated2)
	}

	// Delete
	if err := m.DeleteItem(item.ID); err != nil {
		t.Fatalf("DeleteItem: %v", err)
	}
	if _, err := m.GetItem(item.ID); err == nil {
		t.Fatalf("item should be gone after delete")
	}
}

func TestManager_DeleteItemMissing(t *testing.T) {
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	m := NewManager(db, t.TempDir(), nil)
	if err := m.DeleteItem("missing"); err == nil {
		t.Fatalf("missing item should error")
	}
}

func TestManager_LogRetrievalAndGetLogs(t *testing.T) {
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	m := NewManager(db, "", nil)

	if err := m.LogRetrieval("conv1", "msg1", "query", "XSS", []string{"1", "2"}); err != nil {
		t.Fatalf("LogRetrieval: %v", err)
	}

	// by message id
	logs, err := m.GetRetrievalLogs("", "msg1", 10)
	if err != nil {
		t.Fatalf("GetRetrievalLogs by msg: %v", err)
	}
	if len(logs) != 1 || logs[0].MessageID != "msg1" {
		t.Fatalf("logs by msg = %+v", logs)
	}
	if len(logs[0].RetrievedItems) != 2 {
		t.Fatalf("retrieved items = %v", logs[0].RetrievedItems)
	}

	// by conversation id
	logs2, err := m.GetRetrievalLogs("conv1", "", 10)
	if err != nil {
		t.Fatalf("GetRetrievalLogs by conv: %v", err)
	}
	if len(logs2) != 1 {
		t.Fatalf("logs by conv = %d", len(logs2))
	}

	// all logs
	logs3, err := m.GetRetrievalLogs("", "", 10)
	if err != nil {
		t.Fatalf("GetRetrievalLogs all: %v", err)
	}
	if len(logs3) != 1 {
		t.Fatalf("all logs = %d", len(logs3))
	}

	// delete
	if err := m.DeleteRetrievalLog(logs[0].ID); err != nil {
		t.Fatalf("DeleteRetrievalLog: %v", err)
	}
	logs4, _ := m.GetRetrievalLogs("", "", 10)
	if len(logs4) != 0 {
		t.Fatalf("after delete, logs = %d", len(logs4))
	}
	// delete missing
	if err := m.DeleteRetrievalLog("missing"); err == nil {
		t.Fatalf("delete missing should error")
	}
}

func TestManager_GetIndexStatus(t *testing.T) {
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	at := fixedTestTime()
	insertKnowledgeItem(t, db, "1", "XSS", "t1", "/p/a.md", "a", at)
	insertKnowledgeItem(t, db, "2", "XSS", "t2", "/p/b.md", "b", at)
	// index one item's vector
	_, err := db.Exec(`INSERT INTO knowledge_embeddings (id, item_id, chunk_index, chunk_text, embedding, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"c1", "1", 0, "chunk", "[1,2,3]", at)
	if err != nil {
		t.Fatal(err)
	}

	m := NewManager(db, "", nil)
	st, err := m.GetIndexStatus()
	if err != nil {
		t.Fatalf("GetIndexStatus: %v", err)
	}
	if st["total_items"] != 2 || st["indexed_items"] != 1 {
		t.Fatalf("status = %+v", st)
	}
	if st["is_complete"] != false {
		t.Fatalf("should not be complete: %+v", st)
	}
}

func TestManager_GetIndexStatusEmpty(t *testing.T) {
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	m := NewManager(db, "", nil)
	st, err := m.GetIndexStatus()
	if err != nil {
		t.Fatal(err)
	}
	if st["total_items"] != 0 || st["indexed_items"] != 0 {
		t.Fatalf("empty status = %+v", st)
	}
}

func TestManager_GetCategoriesWithItems(t *testing.T) {
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	at := fixedTestTime()
	insertKnowledgeItem(t, db, "1", "XSS", "t1", "/p/a.md", "a", at)
	insertKnowledgeItem(t, db, "2", "XSS", "t2", "/p/b.md", "b", at)
	insertKnowledgeItem(t, db, "3", "SQLi", "t3", "/p/c.md", "c", at)

	m := NewManager(db, "", nil)
	groups, total, err := m.GetCategoriesWithItems(0, 0)
	if err != nil {
		t.Fatalf("GetCategoriesWithItems: %v", err)
	}
	if total != 2 || len(groups) != 2 {
		t.Fatalf("groups = %d, total = %d", len(groups), total)
	}
	// limit=1 paging
	groups2, total2, err := m.GetCategoriesWithItems(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total2 != 2 || len(groups2) != 1 {
		t.Fatalf("paged groups=%d total=%d", len(groups2), total2)
	}
	// offset beyond
	groups3, _, err := m.GetCategoriesWithItems(1, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups3) != 0 {
		t.Fatalf("offset beyond should be 0, got %d", len(groups3))
	}
}

func TestManager_IsEmptyDir(t *testing.T) {
	dir := t.TempDir()
	empty, err := isEmptyDir(dir)
	if err != nil || !empty {
		t.Fatalf("empty dir = %v, err %v", empty, err)
	}
	// add a hidden file -> still considered empty
	if err := os.WriteFile(filepath.Join(dir, ".hidden"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	empty, err = isEmptyDir(dir)
	if err != nil || !empty {
		t.Fatalf("hidden file dir = %v, err %v", empty, err)
	}
	// add a real file -> not empty
	if err := os.WriteFile(filepath.Join(dir, "real.md"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	empty, err = isEmptyDir(dir)
	if err != nil || empty {
		t.Fatalf("non-empty dir = %v, err %v", empty, err)
	}
	// missing dir -> error
	if _, err := isEmptyDir(filepath.Join(dir, "nope")); err == nil {
		t.Fatalf("missing dir should error")
	}
}

func TestManager_UpdateItemMissing(t *testing.T) {
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	m := NewManager(db, t.TempDir(), nil)
	if _, err := m.UpdateItem("missing", "XSS", "t", "c"); err == nil {
		t.Fatalf("update missing should error")
	}
}

func TestManager_SearchItemsByKeywordNotFound(t *testing.T) {
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	m := NewManager(db, "", nil)
	items, err := m.SearchItemsByKeyword("does-not-exist", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("expected 0, got %d", len(items))
	}
}

func TestManager_GetCategoriesWithItemsLimitError(t *testing.T) {
	// Force an error path by closing the DB.
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	m := NewManager(db, "", nil)
	_ = db.Close()
	if _, _, err := m.GetCategoriesWithItems(0, 0); err == nil {
		t.Fatalf("closed db should error")
	}
}

func TestManager_GetRetrievalLogsInvalidTime(t *testing.T) {
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	// Insert a log with an unparseable time string.
	_, err := db.Exec(`INSERT INTO knowledge_retrieval_logs (id, conversation_id, message_id, query, risk_type, retrieved_items, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"l1", "c", "m", "q", "XSS", `["1"]`, "not-a-date")
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(db, "", zap.NewNop())
	logs, err := m.GetRetrievalLogs("", "", 10)
	if err != nil {
		t.Fatalf("GetRetrievalLogs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("logs = %d", len(logs))
	}
	// zero time falls back to now; assert CreatedAt not zero.
	if logs[0].CreatedAt.IsZero() {
		t.Errorf("invalid time should fallback to now")
	}
}

func TestManager_GetItemTimeParseVariants(t *testing.T) {
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	// RFC3339 and space formats.
	_, err := db.Exec(`INSERT INTO knowledge_base_items (id, category, title, file_path, content, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"1", "XSS", "t", "/p/a.md", "c", "2026-01-02T03:04:05Z", "2026-01-02 03:04:05")
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(db, "", nil)
	item, err := m.GetItem("1")
	if err != nil {
		t.Fatal(err)
	}
	if item.CreatedAt.IsZero() || item.UpdatedAt.IsZero() {
		t.Errorf("time parse failed: %+v", item)
	}
}

func TestManager_GetItemsTimeParseVariants(t *testing.T) {
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	_, err := db.Exec(`INSERT INTO knowledge_base_items (id, category, title, file_path, content, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"1", "XSS", "t", "/p/a.md", "c", "", "")
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(db, "", nil)
	items, err := m.GetItems("")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d", len(items))
	}
	// both times empty -> UpdatedAt falls back to CreatedAt (zero), stays zero.
	if !items[0].CreatedAt.IsZero() {
		t.Errorf("empty created should stay zero: %+v", items[0])
	}
}

func TestManager_ScanKnowledgeBaseWalkError(t *testing.T) {
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	// basePath is a file, WalkDir will error.
	f := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(f, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	m2 := NewManager(db, f, nil)
	if _, err := m2.ScanKnowledgeBase(); err == nil {
		t.Fatalf("walk error should surface at least once")
	}
}

func TestManager_GetStatsClosed(t *testing.T) {
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	m := NewManager(db, "", nil)
	_ = db.Close()
	if _, _, err := m.GetStats(); err == nil {
		t.Fatalf("closed db should error")
	}
}

func TestManager_GetCategoriesClosed(t *testing.T) {
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	m := NewManager(db, "", nil)
	_ = db.Close()
	if _, err := m.GetCategories(); err == nil {
		t.Fatalf("closed db should error")
	}
}

func TestManager_GetItemsCountClosed(t *testing.T) {
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	m := NewManager(db, "", nil)
	_ = db.Close()
	if _, err := m.GetItemsCount(""); err == nil {
		t.Fatalf("closed db should error")
	}
}

func TestRetrievalLog_MarshalJSONFormat(t *testing.T) {
	log := &RetrievalLog{ID: "l1", Query: "q", CreatedAt: fixedTestTime()}
	b, err := log.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if s == "" {
		t.Fatalf("empty json")
	}
}

func TestKnowledgeItem_TimeFallback(t *testing.T) {
	// Directly test UpdateItem time-fallback through GetItem path is covered;
	// here verify CreatedAt parse from RFC3339Nano.
	db := newTestMemoryDB(t)
	applyKnowledgeSchema(t, db)
	_, err := db.Exec(`INSERT INTO knowledge_base_items (id, category, title, file_path, content, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"1", "XSS", "t", "/p/a.md", "c", "2026-01-02T03:04:05.999999999Z", "2026-01-02T03:04:05Z")
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(db, "", nil)
	item, err := m.GetItem("1")
	if err != nil {
		t.Fatal(err)
	}
	if item.CreatedAt.Year() != 2026 {
		t.Fatalf("nano parse failed: %+v", item.CreatedAt)
	}
}

var _ = context.Background
