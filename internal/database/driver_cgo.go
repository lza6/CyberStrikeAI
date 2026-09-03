//go:build !sqlite_pure_go

// CGO 构建（生产）：使用 mattn/go-sqlite3。
// Dockerfile 与本地带 mingw 的构建走此路径；行为与历史版本一致。

package database

import _ "github.com/mattn/go-sqlite3"

const sqliteDriver = "sqlite3"
