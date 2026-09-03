// Package blackboard 的 SQLite 持久化实现（K0b 硬奠基）。
//
// 设计目标（移植自 Pentest-Swarm-AI blackboard + CyberStrikeAI 双驱动适配）：
//   - 进程重启不丢 findings：全部 finding 落 SQLite（WAL 模式）。
//   - 与 internal/database 共用驱动适配（sqliteDriverName/sqliteDSN）：
//     CGO 构建走 mattn/go-sqlite3，-tags sqlite_pure_go 走 modernc.org/sqlite。
//     不要直接 sql.Open("sqlite3", ...)，统一经 sqliteDriverName() 适配。
//   - FTS5 全文索引：modernc 默认带 fts5 模块；mattn 默认构建不含 fts5（需
//     -tags sqlite_fts5）。本实现尝试建 FTS5 external-content 虚拟表 + 触发器
//     同步，失败则降级（fts5=false），核心 Publish/Get/List/Subscribe/Supersede
//     不依赖 FTS5，故两条驱动路径下行为一致。
//   - Subscribe 用应用层广播（subscribers map + buffered channel）+ DB 重放
//     cursor 之后的已存在 finding。cursor 表 blackboard_subscriber_cursors
//     记录持久化游标（奠基；当前 Subscribe 接口为 ctx-bound，跨进程持久订阅
//     留给后续扩展）。
//   - Board interface 不变：NewMemoryBoard / NewSQLiteBoard 二选一。
//
// 并发安全要点（Blocking 1/3 修复，与 memory_board.go 共用 subscriber.go）：
//   - subscriber 封装（subscriber.go）：done chan + closed 标志位 + closeOnce。
//     对已关闭 channel 发送会 panic，trySend 用 closed 快速路径 + recover 兜底，
//     确保 Publish/Supersede 广播对并发 Close/ctx 取消安全。
//   - Subscribe 拆分「锁内只注册 subscriber → 锁外重放」，避免持 b.mu 做
//     QueryContext/rows.Next() 全量重放阻塞所有 Publish/Supersede。重放与
//     后续广播并发可能重复投递，由 at-least-once + cursor 去重兜底。
package blackboard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"cyberstrike-ai/internal/database"
)

// sqliteBoardSubscriberBufferSize 订阅 channel 缓冲大小（与 MemoryBoard 对齐，
// 兼顾吞吐与背压；满了丢旧一条保 at-least-once）。
const sqliteBoardSubscriberBufferSize = 128

// SQLiteBoard 基于 *sql.DB 的黑板实现。findings 落 SQLite（WAL），重启不丢；
// Subscribe 用应用层广播 + DB 重放，语义对齐 MemoryBoard。
//
// 不导出 Close 之外的额外方法；Board interface 不变。
type SQLiteBoard struct {
	mu          sync.Mutex
	db          *sql.DB
	logger      *zap.Logger
	subscribers map[int64]*subscriber
	nextSubID   int64
	fts5        bool // FTS5 虚拟表是否可用（不可用时降级，搜索走 LIKE）
	closeOnce   sync.Once
	closeErr    error
	closed      bool
}

// NewSQLiteBoard 打开/创建 blackboard SQLite 库并初始化 schema。
// dbPath 是 .db 文件路径（建议从 storage.HomeDir() 派生，与 knowledge 共库管理方式）。
// 驱动适配走 internal/database 的 sqliteDriverName()/sqliteDSN()（CGO mattn /
// pure-go modernc），不要直接 sql.Open("sqlite3", ...)。
func NewSQLiteBoard(dbPath string, logger *zap.Logger) (*SQLiteBoard, error) {
	if strings.TrimSpace(dbPath) == "" {
		return nil, errors.New("blackboard: sqlite db path is empty")
	}
	// 确保目录存在（与 NewKnowledgeDB 一致）。
	if dir := filepath.Dir(dbPath); dir != "" && dir != "." {
		_ = osMkdirAll(dir, 0o755)
	}
	db, err := sql.Open(database.SqliteDriverName(), database.SqliteDSN(dbPath))
	if err != nil {
		return nil, fmt.Errorf("blackboard: 打开 sqlite 失败: %w", err)
	}
	// SQLite 单写者：保守连接池，降低锁竞争（与 database.configureDBPool 同源）。
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("blackboard: ping sqlite 失败: %w", err)
	}
	// PRAGMA 与 database.configureSQLitePragmas 等价（WAL/foreign_keys/busy_timeout/
	// synchronous）。这里 db 是独立连接，需单独设。
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=1",
		"PRAGMA busy_timeout=5000",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA wal_autocheckpoint=1000",
		fmt.Sprintf("PRAGMA journal_size_limit=%d", 256*1024*1024),
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("blackboard: 设置 PRAGMA 失败 (%s): %w", pragma, err)
		}
	}

	b := &SQLiteBoard{
		db:          db,
		logger:      logger,
		subscribers: make(map[int64]*subscriber),
	}
	if err := b.initSchema(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("blackboard: 初始化 schema 失败: %w", err)
	}
	return b, nil
}

// initSchema 建主表 + 索引 + cursor 表 + FTS5（可选降级）。幂等。
func (b *SQLiteBoard) initSchema() error {
	// 1. 主表：findings。created_at 用 TEXT 存 RFC3339Nano（双驱动一致，避免
	// DATETIME 列在 mattn/modernc 间的时间解析差异）。rowid 是 SQLite 自增隐藏列，
	// 作为 Subscribe 的 cursor 序号（单调递增，blackboard 不 DELETE，故 rowid 稳定）。
	const createFindings = `CREATE TABLE IF NOT EXISTS blackboard_findings (
		id TEXT PRIMARY KEY,
		type TEXT NOT NULL DEFAULT '',
		title TEXT NOT NULL DEFAULT '',
		detail TEXT NOT NULL DEFAULT '',
		severity TEXT NOT NULL DEFAULT '',
		source TEXT NOT NULL DEFAULT '',
		project_id TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		superseded_by TEXT NOT NULL DEFAULT ''
	)`
	if _, err := b.db.Exec(createFindings); err != nil {
		return fmt.Errorf("建 blackboard_findings 失败: %w", err)
	}

	// 2. 索引：project_id 过滤 + created_at 升序（List 与 Subscribe 重放用）。
	for _, idx := range []string{
		`CREATE INDEX IF NOT EXISTS idx_blackboard_findings_project_id ON blackboard_findings(project_id)`,
		`CREATE INDEX IF NOT EXISTS idx_blackboard_findings_created_at ON blackboard_findings(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_blackboard_findings_superseded_by ON blackboard_findings(superseded_by)`,
	} {
		if _, err := b.db.Exec(idx); err != nil {
			return fmt.Errorf("建索引失败: %w", err)
		}
	}

	// 3. 订阅游标表：记录持久化游标（奠基；当前 Subscribe 为 ctx-bound 不写此表，
	// 留给未来跨进程持久订阅扩展）。
	const createCursors = `CREATE TABLE IF NOT EXISTS blackboard_subscriber_cursors (
		subscriber_id TEXT PRIMARY KEY,
		last_consumed_id TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL
	)`
	if _, err := b.db.Exec(createCursors); err != nil {
		return fmt.Errorf("建 blackboard_subscriber_cursors 失败: %w", err)
	}

	// 4. FTS5 external-content 虚拟表 + 触发器同步（可选；失败降级，不阻断）。
	// modernc 默认带 fts5 模块；mattn 默认构建不含（需 -tags sqlite_fts5）。
	// 降级后 List/Get 等核心功能不受影响（FTS5 仅用于未来全文搜索扩展）。
	if err := b.tryInitFTS5(); err != nil {
		if b.logger != nil {
			b.logger.Warn("blackboard FTS5 不可用，降级到无全文索引（核心功能不受影响）",
				zap.Error(err))
		}
		b.fts5 = false
	} else {
		b.fts5 = true
	}
	return nil
}

// tryInitFTS5 建 FTS5 external-content 虚拟表 + 触发器 + 初始填充。
// 任一步失败返回 error（调用方降级）。用 tx 保证 FTS5 表与触发器原子创建。
func (b *SQLiteBoard) tryInitFTS5() error {
	tx, err := b.db.Begin()
	if err != nil {
		return fmt.Errorf("开启 FTS5 tx 失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// FTS5 external-content：content 指向 blackboard_findings，content_rowid=rowid。
	// 这样 FTS5 索引 title/detail，数据本体仍在主表，避免重复存储。
	const createFTS = `CREATE VIRTUAL TABLE IF NOT EXISTS blackboard_findings_fts USING fts5(
		title, detail, content=blackboard_findings, content_rowid=rowid
	)`
	if _, err := tx.Exec(createFTS); err != nil {
		return fmt.Errorf("建 FTS5 虚拟表失败: %w", err)
	}

	// 触发器：INSERT/DELETE/UPDATE 同步 FTS5 索引。
	// fts5_findings_fts 是 FTS5 的 special command function（<table>_fts5），
	// 'delete' 命令从索引移除指定 rowid。
	triggers := []string{
		`CREATE TRIGGER IF NOT EXISTS blackboard_findings_fts_ai AFTER INSERT ON blackboard_findings BEGIN
			INSERT INTO blackboard_findings_fts(rowid, title, detail) VALUES (new.rowid, new.title, new.detail);
		END`,
		`CREATE TRIGGER IF NOT EXISTS blackboard_findings_fts_ad AFTER DELETE ON blackboard_findings BEGIN
			INSERT INTO blackboard_findings_fts(fts5_findings_fts, rowid, title, detail) VALUES ('delete', old.rowid, old.title, old.detail);
		END`,
		`CREATE TRIGGER IF NOT EXISTS blackboard_findings_fts_au AFTER UPDATE ON blackboard_findings BEGIN
			INSERT INTO blackboard_findings_fts(fts5_findings_fts, rowid, title, detail) VALUES ('delete', old.rowid, old.title, old.detail);
			INSERT INTO blackboard_findings_fts(rowid, title, detail) VALUES (new.rowid, new.title, new.detail);
		END`,
	}
	for _, tr := range triggers {
		if _, err := tx.Exec(tr); err != nil {
			return fmt.Errorf("建 FTS5 触发器失败: %w", err)
		}
	}

	// 初始填充已有数据（external-content 表不会自动索引已存在行）。
	// 'rebuild' 命令重建整个 FTS5 索引（从 content 表读取全部行）。
	if _, err := tx.Exec(`INSERT INTO blackboard_findings_fts(fts5_findings_fts) VALUES('rebuild')`); err != nil {
		// rebuild 在空表上可能返回 "no such rowid" 类错误；降级不阻断。
		return fmt.Errorf("FTS5 rebuild 失败: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交 FTS5 tx 失败: %w", err)
	}
	return nil
}

// Publish 追加一条 finding 并向所有订阅者广播。落盘 SQLite，重启不丢。
func (b *SQLiteBoard) Publish(ctx context.Context, finding Finding) (string, error) {
	if finding.ID == "" {
		finding.ID = uuid.New().String()
	}
	if finding.CreatedAt.IsZero() {
		finding.CreatedAt = time.Now().UTC()
	}
	createdAt := finding.CreatedAt.UTC().Format(time.RFC3339Nano)

	// 单语句 INSERT，WAL + busy_timeout 防并发写锁。
	_, err := b.db.ExecContext(ctx,
		`INSERT INTO blackboard_findings (id, type, title, detail, severity, source, project_id, created_at, superseded_by)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, '')`,
		finding.ID, finding.Type, finding.Title, finding.Detail,
		finding.Severity, finding.Source, finding.ProjectID, createdAt)
	if err != nil {
		return "", fmt.Errorf("blackboard: 插入 finding 失败: %w", err)
	}

	// 广播：snapshot subscribers，非阻塞发送（满则丢旧一条，保 at-least-once）。
	// trySend 对已关闭 channel 安全（closed 快速路径 + recover 兜底），覆盖
	// Publish 与 Close/ctx 取消并发的 send-on-closed-channel 场景。
	b.mu.Lock()
	subs := make([]*subscriber, 0, len(b.subscribers))
	for _, s := range b.subscribers {
		subs = append(subs, s)
	}
	b.mu.Unlock()

	for _, s := range subs {
		s.trySend(finding, b.logger)
	}

	// ctx 已取消不阻塞 Publish 本身（finding 已落盘 DB）。
	_ = ctx
	return finding.ID, nil
}

// Get 按 ID 获取单条 finding；不存在返回 (zero, false, nil)。
func (b *SQLiteBoard) Get(ctx context.Context, id string) (Finding, bool, error) {
	if id == "" {
		return Finding{}, false, nil
	}
	row := b.db.QueryRowContext(ctx,
		`SELECT id, type, title, detail, severity, source, project_id, created_at, superseded_by
		 FROM blackboard_findings WHERE id = ?`, id)
	f, err := scanFinding(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Finding{}, false, nil
		}
		return Finding{}, false, fmt.Errorf("blackboard: 查询 finding 失败: %w", err)
	}
	return f, true, nil
}

// List 列出某项目下的全部 finding（升序，按 rowid 即插入顺序，等价 CreatedAt 升序）。
// projectID 为空返回全部。
func (b *SQLiteBoard) List(ctx context.Context, projectID string) ([]Finding, error) {
	var rows *sql.Rows
	var err error
	if strings.TrimSpace(projectID) == "" {
		rows, err = b.db.QueryContext(ctx,
			`SELECT id, type, title, detail, severity, source, project_id, created_at, superseded_by
			 FROM blackboard_findings ORDER BY rowid ASC`)
	} else {
		rows, err = b.db.QueryContext(ctx,
			`SELECT id, type, title, detail, severity, source, project_id, created_at, superseded_by
			 FROM blackboard_findings WHERE project_id = ? ORDER BY rowid ASC`, projectID)
	}
	if err != nil {
		return nil, fmt.Errorf("blackboard: 查询 findings 失败: %w", err)
	}
	defer rows.Close()

	out := make([]Finding, 0)
	for rows.Next() {
		f, scanErr := scanFinding(rows.Scan)
		if scanErr != nil {
			return nil, fmt.Errorf("blackboard: 扫描 finding 行失败: %w", scanErr)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("blackboard: 遍历 findings 失败: %w", err)
	}
	return out, nil
}

// ListRecent 列出某项目下最近 limit 条 finding（升序，按 rowid 即插入顺序）。
// limit <= 0 视为无限制（等价于 List）。projectID 为空返回全部项目的最近 limit 条。
//
// RC10 修复：reactions 引擎 deriveSessionStatus 每秒调一次 List 全量扫描，千级
// finding 浪费 CPU。本方法用 `ORDER BY rowid DESC LIMIT ?` 只拉最近 N 条，
// 再反转为升序返回。引擎侧用类型断言探测本方法（不改 Board interface）。
func (b *SQLiteBoard) ListRecent(ctx context.Context, projectID string, limit int) ([]Finding, error) {
	if limit <= 0 {
		return b.List(ctx, projectID)
	}
	var rows *sql.Rows
	var err error
	if strings.TrimSpace(projectID) == "" {
		rows, err = b.db.QueryContext(ctx,
			`SELECT id, type, title, detail, severity, source, project_id, created_at, superseded_by
			 FROM blackboard_findings ORDER BY rowid DESC LIMIT ?`, limit)
	} else {
		rows, err = b.db.QueryContext(ctx,
			`SELECT id, type, title, detail, severity, source, project_id, created_at, superseded_by
			 FROM blackboard_findings WHERE project_id = ? ORDER BY rowid DESC LIMIT ?`, projectID, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("blackboard: 查询最近 findings 失败: %w", err)
	}
	defer rows.Close()
	desc := make([]Finding, 0, limit)
	for rows.Next() {
		f, scanErr := scanFinding(rows.Scan)
		if scanErr != nil {
			return nil, fmt.Errorf("blackboard: 扫描 finding 行失败: %w", scanErr)
		}
		desc = append(desc, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("blackboard: 遍历最近 findings 失败: %w", err)
	}
	// 反转为升序（与 List 语义一致），方便调用方按时间正序遍历。
	out := make([]Finding, len(desc))
	for i, f := range desc {
		out[len(desc)-1-i] = f
	}
	return out, nil
}

// Subscribe 从 cursor 开始订阅。cursor 是已处理的最后一条 finding 序号（0-based，
// 与 MemoryBoard slice 索引语义一致）。SQLiteBoard 用 rowid 映射：cursor=N 表示已
// 收到 rowid 1..N，从 rowid N+1 开始投递。返回的 channel 收到的 finding 对应
// rowid cursor+1..M。ctx 取消时关闭 channel。
//
// 投递语义：at-least-once；先从 DB 重放 cursor 之后的已存在 finding（非阻塞，
// channel 满则丢旧一条保新），再注册为订阅者接收后续 Publish。
//
// Blocking 3 修复：原实现持 b.mu 做 QueryContext + rows.Next() 全量重放，阻塞所有
// Publish/Supersede。现拆分为「锁内只注册 subscriber → 锁外重放」：锁内仅 append
// 到 subs + 分配 subID，锁外做 DB 重放。重放与后续 Publish 广播可能并发投递
// 同一 finding（重复），由 at-least-once + cursor 去重兜底。
//
// Blocking 1 修复：ctx 取消 goroutine 与 Close 在锁内 sub.close()，trySend 对
// 已关闭 channel 安全（closed 快速路径 + recover 兜底）。
func (b *SQLiteBoard) Subscribe(ctx context.Context, cursor int64) <-chan Finding {
	sub := newSubscriber(sqliteBoardSubscriberBufferSize)
	if cursor < 0 {
		cursor = 0
	}
	// P1-5：派生带 cancel 的 ctx，把 cancel 存入 subscriber。Close 时对每个
	// subscriber 调 cancel，唤醒下面阻塞在 <-ctx.Done() 的 goroutine（否则
	// Close 后每个订阅者泄漏一个常驻 goroutine）。Subscribe 传入的原始 ctx
	// 取消时同样级联取消 derived ctx，原语义不变。
	ctx, cancel := context.WithCancel(ctx)
	sub.bindCancel(cancel)

	// 锁内：仅注册 subscriber（不做 DB I/O），避免阻塞 Publish/Supersede。
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		// 已 Close：返回已关闭 channel，重放无意义。
		sub.close()
		return sub.ch
	}
	b.nextSubID++
	subID := b.nextSubID
	b.subscribers[subID] = sub
	b.mu.Unlock()

	// 锁外：重放 cursor 之后的已存在 finding。与并发 Publish 广播可能重复投递，
	// 由 at-least-once + cursor 去重兜底（订阅者侧维护已处理 cursor）。
	b.replayFindings(ctx, cursor, sub)

	// ctx 取消时移除订阅者并关闭 channel。
	go func() {
		<-ctx.Done()
		b.mu.Lock()
		if existing, ok := b.subscribers[subID]; ok {
			delete(b.subscribers, subID)
			existing.close()
		}
		b.mu.Unlock()
	}()

	return sub.ch
}

// replayFindings 从 DB 重放 rowid > cursor 的已存在 finding 到订阅者 channel。
// 锁外执行（不持 b.mu），避免阻塞 Publish/Supersede。非阻塞投递：channel 满
// 则丢旧一条保新；订阅者已关闭则 trySend 内部安全返回。
func (b *SQLiteBoard) replayFindings(ctx context.Context, cursor int64, sub *subscriber) {
	rows, err := b.db.QueryContext(ctx,
		`SELECT id, type, title, detail, severity, source, project_id, created_at, superseded_by
		 FROM blackboard_findings WHERE rowid > ? ORDER BY rowid ASC`, cursor)
	if err != nil {
		if b.logger != nil {
			b.logger.Warn("blackboard Subscribe 查询已存在 findings 失败，跳过重放",
				zap.Error(err))
		}
		return
	}
	defer rows.Close()
	for rows.Next() {
		f, scanErr := scanFinding(rows.Scan)
		if scanErr != nil {
			// 单行扫描失败不阻断；记日志继续。
			if b.logger != nil {
				b.logger.Warn("blackboard Subscribe 扫描行失败，跳过",
					zap.Error(scanErr))
			}
			continue
		}
		sub.trySend(f, b.logger)
	}
	if err := rows.Err(); err != nil && b.logger != nil {
		b.logger.Warn("blackboard Subscribe 遍历 rows 失败", zap.Error(err))
	}
}

// Supersede 标记 oldID 被 newFinding 取代，返回新 finding 的 ID。
// oldID 不存在时返回 ErrOldNotFound；oldID 为空返回 ErrEmptyOldID。
// 用 tx 保证 "发布新 finding + 标记 old" 原子性；广播在 tx 外。
func (b *SQLiteBoard) Supersede(ctx context.Context, oldID string, newFinding Finding) (string, error) {
	if oldID == "" {
		return "", errEmptyOldID
	}

	if newFinding.ID == "" {
		newFinding.ID = uuid.New().String()
	}
	if newFinding.CreatedAt.IsZero() {
		newFinding.CreatedAt = time.Now().UTC()
	}
	createdAt := newFinding.CreatedAt.UTC().Format(time.RFC3339Nano)

	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("blackboard: 开启 supersede tx 失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// 先确认 oldID 存在（SELECT FOR UPDATE 等价：SQLite tx 内查询）。
	var existing string
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM blackboard_findings WHERE id = ?`, oldID).Scan(&existing)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", errOldNotFound
		}
		return "", fmt.Errorf("blackboard: 查询 old finding 失败: %w", err)
	}

	// 发布新 finding。
	_, err = tx.ExecContext(ctx,
		`INSERT INTO blackboard_findings (id, type, title, detail, severity, source, project_id, created_at, superseded_by)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, '')`,
		newFinding.ID, newFinding.Type, newFinding.Title, newFinding.Detail,
		newFinding.Severity, newFinding.Source, newFinding.ProjectID, createdAt)
	if err != nil {
		return "", fmt.Errorf("blackboard: 插入新 finding 失败: %w", err)
	}

	// 标记 old.SupersededBy = newID。
	_, err = tx.ExecContext(ctx,
		`UPDATE blackboard_findings SET superseded_by = ? WHERE id = ?`,
		newFinding.ID, oldID)
	if err != nil {
		return "", fmt.Errorf("blackboard: 标记 supersede 失败: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("blackboard: 提交 supersede tx 失败: %w", err)
	}

	// 广播新 finding（与 Publish 广播语义一致，trySend 对已关闭 channel 安全）。
	b.mu.Lock()
	subs := make([]*subscriber, 0, len(b.subscribers))
	for _, s := range b.subscribers {
		subs = append(subs, s)
	}
	b.mu.Unlock()

	for _, s := range subs {
		s.trySend(newFinding, b.logger)
	}

	return newFinding.ID, nil
}

// Len 返回当前 finding 总数（主要供测试用；非 Board interface 方法）。
// 返回 (int, error)：表不存在或查询失败时返回错误，不吞错（RC6 修复）。
func (b *SQLiteBoard) Len() (int, error) {
	var n int
	if err := b.db.QueryRow(`SELECT COUNT(*) FROM blackboard_findings`).Scan(&n); err != nil {
		return 0, fmt.Errorf("blackboard: 查询 finding 总数失败: %w", err)
	}
	return n, nil
}

// Close 关闭数据库连接（非 Board interface 方法；app.Shutdown 时 type assertion 调用）。
// 幂等：多次调用只关闭一次。先关闭所有 subscriber channel（sub.close 幂等）并
// 调 sub.cancelCtx() 取消各自订阅 ctx——Subscribe 注册的 ctx.Done goroutine
// 由此退出，避免 Close 后泄漏（P1-5）——再关闭 *sql.DB。
func (b *SQLiteBoard) Close() error {
	b.closeOnce.Do(func() {
		b.mu.Lock()
		b.closed = true
		// 关闭所有订阅 channel + 取消订阅 ctx（唤醒 ctx.Done goroutine）。
		for _, sub := range b.subscribers {
			sub.close()
			sub.cancelCtx()
		}
		b.subscribers = make(map[int64]*subscriber)
		b.mu.Unlock()
		if b.db != nil {
			b.closeErr = b.db.Close()
		}
	})
	return b.closeErr
}

// FTS5Available 返回 FTS5 全文索引是否可用（测试用）。
func (b *SQLiteBoard) FTS5Available() bool {
	return b.fts5
}

// scanFinding 是 rows.Scan / row.Scan 的共享适配器，把列按固定顺序映射到 Finding。
// 顺序：id, type, title, detail, severity, source, project_id, created_at, superseded_by。
type findingScanFunc func(dest ...interface{}) error

func scanFinding(scan findingScanFunc) (Finding, error) {
	var f Finding
	var createdAtStr string
	err := scan(&f.ID, &f.Type, &f.Title, &f.Detail, &f.Severity,
		&f.Source, &f.ProjectID, &createdAtStr, &f.SupersededBy)
	if err != nil {
		return f, err
	}
	// 解析 RFC3339Nano（双驱动一致；解析失败降级为 zero，不阻断查询）。
	if createdAtStr != "" {
		if t, perr := time.Parse(time.RFC3339Nano, createdAtStr); perr == nil {
			f.CreatedAt = t
		} else if t, perr2 := time.Parse(time.RFC3339, createdAtStr); perr2 == nil {
			f.CreatedAt = t
		}
	}
	return f, nil
}

// osMkdirAll 被 NewSQLiteBoard 用来确保 db 目录存在。包成变量便于测试替换。
var osMkdirAll = func(path string, perm uint32) error {
	return os.MkdirAll(path, os.FileMode(perm))
}
