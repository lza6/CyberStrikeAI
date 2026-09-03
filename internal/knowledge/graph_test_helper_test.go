package knowledge

import (
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// newMemoryDB 构造一个内存 SQLite 数据库（:memory:），供 knowledge 包测试使用。
// 使用 sqlite3 驱动（与生产一致），cleanup 由调用方 t.Cleanup 注册。
func newMemoryDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:?_foreign_keys=1")
	if err != nil {
		t.Fatalf("open memory sqlite: %v", err)
	}
	return db
}
