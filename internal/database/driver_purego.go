//go:build sqlite_pure_go

// 纯 Go 构建（测试/无 CGO 工具链）：使用 modernc.org/sqlite，无需 gcc/mingw。
// 启用：go test -tags sqlite_pure_go ./...
// modernc 的 DSN 不识别 mattn 的 _journal_mode 等 key；经 sqliteDSN() 转 _pragma=...。
// PRAGMA 语义与 mattn 一致（journal_mode=WAL / foreign_keys=1 / busy_timeout=5000 / synchronous=NORMAL）。

package database

import _ "modernc.org/sqlite"

const sqliteDriver = "sqlite"
