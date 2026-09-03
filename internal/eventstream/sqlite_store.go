package eventstream

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// SQLiteStore 是 Store 的 SQLite 持久化实现（注入 *sql.DB，零 sqlite 驱动依赖）。
//
// 迁移自参考项目 agent-orchestrator/backend 的 change_log 表 + CDC 设计。
// 核心设计（与参考项目一致）：
//   - change_log 是 append-only 的有序事实源（seq 单调递增 + 幂等键）。
//   - CDC 事件由调用方显式 Append（应用层 emit）；客户端靠 seq cursor 做 durable catch-up。
//   - Poller + Broadcaster 只是 live push on top（见 statusboard/cdc.go）。
//
// 与参考项目的差异（Go 重写，适配 CyberStrikeAI）：
//   - 参考项目用 sqlc 生成 query；本实现手写 database/sql（与主项目范式一致）。
//   - 参考项目 change_log 由 AFTER INSERT/UPDATE 触发器写入（应用层零 emit）；
//     本实现由 EventStream.AddEvent→Append 显式写入（CyberStrikeAI 的 status 事实
//     分散在 workflow_runs/batch_tasks/c2_sessions 多表，触发器方案需为每表定制，
//     迁移成本高；显式 Append 更灵活，且 cause 链是一等公民）。
//   - 参考项目在 cdc 包内直接 Open modernc；本实现接受注入的 *sql.DB，
//     由调用方（app 或测试 helper）负责 Open + 驱动选择（mattn CGO / modernc 纯 Go），
//     保持 eventstream 为 leaf 包（零 sqlite 驱动 import，不反向导入 database）。
//
// 线程安全：sql.DB 自带连接池；Append/GetEvent/LatestEventID/SearchEvents 互不阻塞。
// nil-safe：所有方法在 db=nil 时返回零值不报错（供测试注入 nil 跳过持久化）。
type SQLiteStore struct {
	mu sync.Mutex
	db *sql.DB
}

// NewSQLiteStore 用已打开的 *sql.DB 构造 Store。调用方负责 Open（含驱动选择/PRAGMA/连接池）+ Close。
// db 为 nil 时返回 nil-safe store（所有方法 no-op，供纯内存测试）。
// 表不存在时自动创建（幂等）。
func NewSQLiteStore(db *sql.DB) (*SQLiteStore, error) {
	s := &SQLiteStore{db: db}
	if db == nil {
		return s, nil
	}
	if err := s.initTables(); err != nil {
		return nil, fmt.Errorf("sqlite store: init tables: %w", err)
	}
	return s, nil
}

// initTables 创建 change_log 表（幂等）。移植自参考项目 0001_init.sql:105-114。
//
// seq 单调自增、project_id/session_id（可空）、event_type、payload CHECK(json_valid)、
// created_at 默认 datetime('now')。CHECK(json_valid(payload)) 在 mattn 和 modernc 都支持。
func (s *SQLiteStore) initTables() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS change_log (
    seq        INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id TEXT NOT NULL DEFAULT '',
    session_id TEXT NOT NULL DEFAULT '',
    event_type TEXT NOT NULL,
    payload    TEXT NOT NULL CHECK (json_valid(payload)),
    created_at TIMESTAMP NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_change_log_seq ON change_log (seq);
CREATE INDEX IF NOT EXISTS idx_change_log_project ON change_log (project_id, seq);
CREATE INDEX IF NOT EXISTS idx_change_log_session ON change_log (session_id, seq);
`
	_, err := s.db.Exec(ddl)
	return err
}

// Close 不关闭 db（db 由调用方管理生命周期）。保留方法供 Store 接口约定扩展。
func (s *SQLiteStore) Close() error { return nil }

// DB 暴露底层 *sql.DB 供高级用法（如 statusboard 聚合查询）。调用方不得 Close。
func (s *SQLiteStore) DB() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

// Append 实现 Store。持久化一条已分配 ID 的 Event。
// 事件 ID/Timestamp/Source/Cause 已由 EventStream.AddEvent 分配；本方法只序列化 payload + 落库。
// 返回 nil 表示持久化成功（EventStream 据此决定是否降级分发）。
func (s *SQLiteStore) Append(ev Event) error {
	if s == nil || s.db == nil || ev == nil {
		return nil
	}
	payload, err := encodeEventPayload(ev)
	if err != nil {
		return fmt.Errorf("sqlite store: encode payload: %w", err)
	}
	_, err = s.db.Exec(
		`INSERT INTO change_log (seq, project_id, session_id, event_type, payload, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		ev.ID(), extractProjectID(ev), extractSessionID(ev), ev.EventType(), payload, ev.Timestamp().UTC(),
	)
	if err != nil {
		return fmt.Errorf("sqlite store: append event %d: %w", ev.ID(), err)
	}
	return nil
}

// GetEvent 实现 Store。按 ID 取单条事件；不存在返回 (nil, false)。
func (s *SQLiteStore) GetEvent(id int64) (Event, bool) {
	if s == nil || s.db == nil || id <= 0 {
		return nil, false
	}
	row := s.db.QueryRow(
		`SELECT seq, project_id, session_id, event_type, payload, created_at FROM change_log WHERE seq = ?`, id,
	)
	er, err := scanEventRow(row)
	if err != nil {
		return nil, false
	}
	ev, err := decodeEventPayload(er)
	if err != nil {
		return nil, false
	}
	return ev, true
}

// LatestEventID 实现 Store。返回最大已持久化 seq（用于 EventStream 启动时恢复 curID）。
func (s *SQLiteStore) LatestEventID() int64 {
	if s == nil || s.db == nil {
		return 0
	}
	var seq int64
	// COALESCE 保证空表返回 0 而非 NULL。
	row := s.db.QueryRow(`SELECT CAST(COALESCE(MAX(seq), 0) AS INTEGER) FROM change_log`)
	if err := row.Scan(&seq); err != nil {
		return 0
	}
	return seq
}

// EventsAfter 从 after（不含）按 seq 升序读最多 limit 条事件，返回 channel 流式消费。
// 移植自参考项目 queries/changelog.sql ReadChangeLogAfter。
// limit<=0 用默认 512。channel 在读完或 ctx 取消时关闭。
func (s *SQLiteStore) EventsAfter(ctx context.Context, after int64, limit int) (<-chan Event, error) {
	if s == nil || s.db == nil {
		ch := make(chan Event)
		close(ch)
		return ch, nil
	}
	if limit <= 0 {
		limit = 512
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT seq, project_id, session_id, event_type, payload, created_at FROM change_log WHERE seq > ? ORDER BY seq LIMIT ?`,
		after, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("sqlite store: events after %d: %w", after, err)
	}
	ch := make(chan Event, 64)
	go func() {
		defer close(ch)
		defer rows.Close()
		for rows.Next() {
			if ctx.Err() != nil {
				return
			}
			er, err := scanEventRow(rows)
			if err != nil {
				return
			}
			ev, err := decodeEventPayload(er)
			if err != nil {
				return
			}
			select {
			case <-ctx.Done():
				return
			case ch <- ev:
			}
		}
	}()
	return ch, nil
}

// SearchEvents 实现 Store。从 startID（含）按过滤器检索，返回 channel 流式消费。
// 移植自参考项目 EventStore.search（按 IncludeTypes/ExcludeTypes/Source 过滤）。
//
// 注意（审计发现 2 披露）：Store 接口无 ctx 参数，本方法内部用 context.Background()
// ——消费方不读完 channel 则底层 goroutine 会阻塞在 ch<-（连接占用直到读毕）。
// 需要取消语义的调用方请用 SearchEventsCtx（本实现扩展，不在 Store 接口内）。
func (s *SQLiteStore) SearchEvents(startID int64, f Filter) <-chan Event {
	return s.SearchEventsCtx(context.Background(), startID, f)
}

// SearchEventsCtx 是 SearchEvents 的带取消变体（接口外扩展）。
// ctx 取消时 goroutine 尽早退出并关闭 channel，不泄漏连接。
func (s *SQLiteStore) SearchEventsCtx(ctx context.Context, startID int64, f Filter) <-chan Event {
	if s == nil || s.db == nil {
		ch := make(chan Event)
		close(ch)
		return ch
	}
	// 动态构造 WHERE：startID + IncludeTypes。参数化防注入。
	args := []interface{}{startID}
	where := "seq >= ?"
	if len(f.IncludeTypes) > 0 {
		ph := make([]string, len(f.IncludeTypes))
		for i, t := range f.IncludeTypes {
			ph[i] = "?"
			args = append(args, t)
		}
		where += " AND event_type IN (" + strings.Join(ph, ",") + ")"
	}
	// ExcludeTypes + Source 在内存 match（量级小；参考项目亦在内存 match）。
	q := "SELECT seq, project_id, session_id, event_type, payload, created_at FROM change_log WHERE " + where + " ORDER BY seq"
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		ch := make(chan Event)
		close(ch)
		return ch
	}
	ch := make(chan Event, 64)
	go func() {
		defer close(ch)
		defer rows.Close()
		for rows.Next() {
			er, err := scanEventRow(rows)
			if err != nil {
				return
			}
			ev, err := decodeEventPayload(er)
			if err != nil {
				return
			}
			if !f.match(ev) {
				continue
			}
			select {
			case <-ctx.Done():
				return
			case ch <- ev:
			}
		}
	}()
	return ch
}

// eventRow 是 change_log 一行的中间表示。
type eventRow struct {
	Seq       int64
	ProjectID string
	SessionID string
	EventType string
	Payload   json.RawMessage
	CreatedAt time.Time
}

// scanner 抽象 *sql.Row 和 *sql.Rows 的 Scan。
type scanner interface {
	Scan(dest ...interface{}) error
}

func scanEventRow(sc scanner) (eventRow, error) {
	var er eventRow
	var createdAtStr string
	if err := sc.Scan(&er.Seq, &er.ProjectID, &er.SessionID, &er.EventType, &er.Payload, &createdAtStr); err != nil {
		return er, err
	}
	// SQLite datetime('now') 返回 "YYYY-MM-DD HH:MM:SS"（UTC），解析为 time.Time。
	if t, err := time.Parse("2006-01-02 15:04:05", createdAtStr); err == nil {
		er.CreatedAt = t.UTC()
	} else if t2, err2 := time.Parse(time.RFC3339, createdAtStr); err2 == nil {
		er.CreatedAt = t2.UTC()
	}
	return er, nil
}

// encodeEventPayload 把 Event 序列化为 JSON RawMessage（payload 列）。
// 含 id/timestamp/source/cause/event_type + 事件特有字段。
func encodeEventPayload(ev Event) (json.RawMessage, error) {
	if ev == nil {
		return json.RawMessage("{}"), nil
	}
	// 用通用 envelope 序列化，保证 GetEvent/SearchEvents 能还原。
	envelope := map[string]interface{}{
		"id":         ev.ID(),
		"timestamp":  ev.Timestamp().UTC().Format(time.RFC3339Nano),
		"source":     string(ev.Source()),
		"cause":      ev.Cause(),
		"event_type": ev.EventType(),
		"payload":    ev, // 事件本身（嵌入 BaseEvent + 特有字段）
	}
	b, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}

// decodeEventPayload 从 payload JSON 还原 Event。
// 因 Event 接口的实现类型（RecallAction 等）在 eventstream 包内，
// 这里按 event_type 分发到已知实现；未知类型还原为 genericEvent。
func decodeEventPayload(er eventRow) (Event, error) {
	var env struct {
		ID        int64       `json:"id"`
		Timestamp string      `json:"timestamp"`
		Source    string      `json:"source"`
		Cause     int64       `json:"cause"`
		EventType string      `json:"event_type"`
		Payload   interface{} `json:"payload"`
	}
	if err := json.Unmarshal(er.Payload, &env); err != nil {
		return nil, err
	}
	// 重新序列化 payload 子节点，按 event_type 分发。
	payloadBytes, _ := json.Marshal(env.Payload)
	ts, _ := time.Parse(time.RFC3339Nano, env.Timestamp)
	if ts.IsZero() {
		ts = er.CreatedAt
	}
	// known event types: recall_action / recall_observation / condensation_action
	switch er.EventType {
	case "recall_action":
		var a RecallAction
		if err := json.Unmarshal(payloadBytes, &a); err == nil {
			a.assign(er.Seq, ts, EventSource(env.Source), env.Cause)
			return &a, nil
		}
	case "recall_observation":
		var o RecallObservation
		if err := json.Unmarshal(payloadBytes, &o); err == nil {
			o.assign(er.Seq, ts, EventSource(env.Source), env.Cause)
			return &o, nil
		}
	case "condensation_action":
		var c CondensationAction
		if err := json.Unmarshal(payloadBytes, &c); err == nil {
			c.assign(er.Seq, ts, EventSource(env.Source), env.Cause)
			return &c, nil
		}
	}
	// 未知类型 → genericEvent（保留所有字段，满足 Event 接口）。
	g := &genericEvent{
		id:        er.Seq,
		timestamp: ts,
		source:    EventSource(env.Source),
		cause:     env.Cause,
		etype:     er.EventType,
		payload:   payloadBytes,
	}
	return g, nil
}

// genericEvent 通用 Event 实现，用于还原未知 event_type 的持久化事件。
type genericEvent struct {
	id        int64
	timestamp time.Time
	source    EventSource
	cause     int64
	etype     string
	payload   json.RawMessage
}

func (g *genericEvent) ID() int64            { return g.id }
func (g *genericEvent) Timestamp() time.Time { return g.timestamp }
func (g *genericEvent) Source() EventSource  { return g.source }
func (g *genericEvent) Cause() int64         { return g.cause }
func (g *genericEvent) EventType() string    { return g.etype }

// extractProjectID / extractSessionID 从 Event 提取 project/session 标识（若事件携带）。
// 默认空串（参考项目 session_id 可空，用于项目级事件）。
func extractProjectID(ev Event) string {
	if pe, ok := ev.(interface{ ProjectID() string }); ok {
		return pe.ProjectID()
	}
	return ""
}

func extractSessionID(ev Event) string {
	if se, ok := ev.(interface{ SessionID() string }); ok {
		return se.SessionID()
	}
	return ""
}
