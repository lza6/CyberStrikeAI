# Spec: K0b — 黑板 SQLite 持久化（blackboard persistence）

> 回溯 spec（spec-driven-development 规范）。本批次已落地（done），spec 用于后续改代码前判断是否过时。

## Objective

把多 agent 涌现式协作的 blackboard 从内存版升级为 SQLite 持久化：findings 落 SQLite（WAL 模式），**进程重启不丢 findings**，Board interface 不变（NewMemoryBoard / NewSQLiteBoard 二选一），与 internal/database 共用双驱动适配（CGO mattn / pure-go modernc）。

**奠基阶段约束**：只做持久化 + Subscribe 语义对齐 MemoryBoard；跨进程持久订阅（cursor 持久化）留给后续扩展。Subscribe 接口当前为 ctx-bound，应用层广播 + DB 重放。

## Tech Stack

- Go 1.25 + `database/sql` + `google/uuid` + `go.uber.org/zap`
- SQLite 双驱动：CGO `mattn/go-sqlite3` / `-tags sqlite_pure_go` `modernc.org/sqlite`，经 `internal/database` 的 `sqliteDriverName()/sqliteDSN()` 适配
- FTS5 全文索引：尝试建虚拟表，失败降级（fts5=false），核心 Publish/Get/List/Subscribe/Supersede 不依赖 FTS5
- WAL 模式：`journal_mode=WAL`，并发写不锁库

## Commands

```bash
go vet ./internal/blackboard/
go test ./internal/blackboard/ -count=1                                        # pure-go 路径
CGO_ENABLED=1 CC=/c/mingw64/bin/gcc.exe go test ./internal/blackboard/ -count=1 # CGO 路径
go build ./...
```

## Project Structure

```
internal/blackboard/board.go         → Board interface（Publish/Get/List/Subscribe/Supersede + Finding 结构）+ MemoryBoard
internal/blackboard/memory_board.go   → 内存版实现（向后兼容，默认）
internal/blackboard/sqlite_board.go   → K0b：SQLiteBoard（*sql.DB + WAL + FTS5 降级 + 应用层广播）
internal/blackboard/sqlite_board_test.go → K0b：重启持久化 + 双驱动一致性 + FTS5 降级 + Subscribe 重放
internal/blackboard/board_test.go     → Board interface 契约测试
internal/blackboard/errors.go         → ErrFindingNotFound 等错误
internal/config/config.go            → Database.BlackboardDriver 字段（memory|sqlite，默认 memory 向后兼容）
internal/app/app.go                  → 按 cfg.Database.BlackboardDriver 选 MemoryBoard/SQLiteBoard
internal/app/app_lifecycle.go         → Shutdown 时关闭 SQLiteBoard 的 *sql.DB
config.example.yaml                   → blackboard_driver: memory 注释段（K0b 说明）
```

## Code Style

```go
// 包注释 + 移植来源 + 双驱动适配说明（匹配 internal/database 风格）
// Package blackboard 的 SQLite 持久化实现（K0b 硬奠基）。
//   - 进程重启不丢 findings：全部 finding 落 SQLite（WAL 模式）。
//   - 与 internal/database 共用驱动适配（sqliteDriverName/sqliteDSN）：
//     CGO 构建走 mattn，-tags sqlite_pure_go 走 modernc。不要直接 sql.Open("sqlite3", ...)。
//   - FTS5 失败降级，核心方法不依赖 FTS5，两条驱动路径下行为一致。
type SQLiteBoard struct { ... }
```

## Testing Strategy

- `sqlite_board_test.go`：核心验收点 `TestSQLiteRestartFindingsPersist`（关闭 *sql.DB 重开，findings 不丢）；WAL 并发写无 "database is locked"；FTS5 不可用时降级 + LIKE 搜索仍工作；Subscribe cursor 重放
- 双驱动一致性：CGO mattn 与 pure-go modernc 下 Publish/Get/List/Subscribe/Supersede 行为一致（FTS5 可用性差异不影响核心方法）
- 回归底线：`go test ./internal/blackboard/` 双路径全绿；全仓不新增 FAIL

## Boundaries

- **Always**：经 `sqliteDriverName()/sqliteDSN()` 适配，不直接 `sql.Open("sqlite3")`；WAL 模式；FTS5 失败降级；Board interface 不变；Shutdown 关闭 *sql.DB
- **Ask first**：改 Board interface 签名；改 SQLite schema 迁移（虽 L2 但持久化数据）；跨进程持久订阅
- **Never**：直接 `sql.Open("sqlite3")` 绕过适配层；删除 MemoryBoard（向后兼容）；FTS5 不可用时 panic（必须降级）；改 WAL 为其他 journal_mode

## Success Criteria

1. `SQLiteBoard` 实现 Board interface 全方法（Publish/Get/List/Subscribe/Supersede）✅ done
2. 进程重启 findings 不丢（`TestSQLiteRestartFindingsPersist` 双路径 PASS）✅ done
3. WAL 模式并发写无 "database is locked" ✅ done
4. FTS5 不可用时降级，LIKE 搜索仍工作（pure-go modernc 路径）✅ done
5. Board interface 不变，NewMemoryBoard/NewSQLiteBoard 二选一 ✅ done
6. config.BlackboardDriver 默认 memory（向后兼容），显式 sqlite 启用持久化 ✅ done
7. 双驱动路径行为一致 ✅ done

## Open Questions

- 跨进程持久订阅（cursor 持久化表已建 `blackboard_subscriber_cursors`，当前 Subscribe 为 ctx-bound 留后续扩展）
- 单机部署是否需要 SQLite blackboard（结果计划指南评估：单机无需额外 DB 后端，memory 版可维持；K0b 已落地为可选）
