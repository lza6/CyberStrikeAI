package knowledge

import (
	"database/sql"
	"testing"
	"time"

	"cyberstrike-ai/internal/config"

	_ "modernc.org/sqlite"
)

// newTestMemoryDB opens an in-memory SQLite database via the pure-Go modernc
// driver (driver name "sqlite"). Requires running tests with -tags sqlite_pure_go.
// 使用 shared-cache 命名内存库并限制单连接：modernc 的裸 ":memory:" 每个连接一个
// 独立库，多连接（如 MultiQuery 并发检索）会看不到已建的表。
func newTestMemoryDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:knowledge_test_mem?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open memory sqlite: %v", err)
	}
	db.SetMaxOpenConns(1) // 串行化访问，避免 shared-cache 并发问题
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// applyKnowledgeSchema creates the three knowledge tables used by Manager,
// Retriever, Indexer and SQLiteIndexer in the test DB.
func applyKnowledgeSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	ddls := []string{
		`CREATE TABLE IF NOT EXISTS knowledge_base_items (
			id TEXT PRIMARY KEY, category TEXT NOT NULL, title TEXT NOT NULL,
			file_path TEXT NOT NULL, content TEXT,
			created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS knowledge_embeddings (
			id TEXT PRIMARY KEY, item_id TEXT NOT NULL, chunk_index INTEGER NOT NULL,
			chunk_text TEXT NOT NULL, embedding TEXT NOT NULL, sub_indexes TEXT NOT NULL DEFAULT '',
			embedding_model TEXT NOT NULL DEFAULT '', embedding_dim INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL,
			FOREIGN KEY (item_id) REFERENCES knowledge_base_items(id) ON DELETE CASCADE)`,
		`CREATE TABLE IF NOT EXISTS knowledge_retrieval_logs (
			id TEXT PRIMARY KEY, conversation_id TEXT, message_id TEXT,
			query TEXT NOT NULL, risk_type TEXT, retrieved_items TEXT, created_at DATETIME NOT NULL)`,
	}
	for _, ddl := range ddls {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("apply schema: %v", err)
		}
	}
}

// insertKnowledgeItem inserts one knowledge_base_items row with the given time.
func insertKnowledgeItem(t *testing.T, db *sql.DB, id, category, title, filePath, content string, at time.Time) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO knowledge_base_items (id, category, title, file_path, content, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, category, title, filePath, content, at, at,
	)
	if err != nil {
		t.Fatalf("insert knowledge item %s: %v", id, err)
	}
}

// zeroTime returns the zero time value (for formatTime tests).
func zeroTime() time.Time { return time.Time{} }

// fixedTime returns a fixed non-zero time (for MarshalJSON tests).
func fixedTime() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }

// yamlRetrievalConfig returns a config.RetrievalConfig for mapping tests.
func yamlRetrievalConfig() config.RetrievalConfig {
	return config.RetrievalConfig{
		TopK:                3,
		SimilarityThreshold: 0.5,
		SubIndexFilter:      "x",
	}
}

// fixedTestTime returns a deterministic non-zero time for DB writes.
func fixedTestTime() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }
