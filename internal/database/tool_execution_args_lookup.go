package database

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// FindNearestToolExecutionArguments returns the arguments for the execution record
// closest to a persisted tool_call detail. Eino can persist a tool_call with empty
// model arguments while the monitor execution row still has the real command/URL.
//
// 实现说明：不用 julianday() 做列上时间过滤——pure-go 驱动下 DATETIME 文本
// 列上的 julianday 返回 NULL，窗口过滤会静默查不到任何行。改为按
// conversation + tool_name 取最近一批记录，在 Go 侧解析 start_time 并选窗口内最近者。
func (db *DB) FindNearestToolExecutionArguments(conversationID, toolName string, at time.Time, window time.Duration) (string, map[string]interface{}, error) {
	conversationID = strings.TrimSpace(conversationID)
	toolName = strings.TrimSpace(toolName)
	if db == nil || conversationID == "" || toolName == "" || at.IsZero() {
		return "", nil, sql.ErrNoRows
	}
	if window <= 0 {
		window = 5 * time.Second
	}
	names := []string{toolName}
	if !strings.Contains(toolName, "::") {
		names = append(names, "eino_fs::"+toolName)
	}
	rows, err := db.Query(`
SELECT id, arguments, start_time
FROM tool_executions
WHERE conversation_id = ?
  AND tool_name IN (?, ?)
ORDER BY created_at DESC
LIMIT 200`, conversationID, names[0], names[len(names)-1])
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()

	bestID := ""
	var bestArgs map[string]interface{}
	var bestDelta time.Duration = 1 << 62
	for rows.Next() {
		var id string
		var raw string
		var startTime interface{}
		if err := rows.Scan(&id, &raw, &startTime); err != nil {
			return "", nil, err
		}
		ts := parseToolExecutionStartTime(startTime)
		if ts.IsZero() {
			continue
		}
		delta := ts.Sub(at)
		if delta < 0 {
			delta = -delta
		}
		if delta > window || delta >= bestDelta {
			continue
		}
		var args map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			return "", nil, fmt.Errorf("parse tool execution arguments: %w", err)
		}
		bestID, bestArgs, bestDelta = strings.TrimSpace(id), args, delta
	}
	if err := rows.Err(); err != nil {
		return "", nil, err
	}
	if bestID == "" {
		return "", nil, sql.ErrNoRows
	}
	return bestID, bestArgs, nil
}

// parseToolExecutionStartTime 解析驱动返回的 start_time 值。
// 不同驱动（CGO mattn / pure-go modernc）都以 TEXT 存储但格式不同：
// "2006-01-02 15:04:05.999999999-07:00"（空格分隔）或 RFC3339Nano（T 分隔）；
// 个别驱动可能直接返回 time.Time，这里统一归一化。
func parseToolExecutionStartTime(v interface{}) time.Time {
	switch t := v.(type) {
	case time.Time:
		return t
	case []byte:
		return parseToolExecutionStartTime(string(t))
	case string:
		layouts := []string{
			time.RFC3339Nano,
			"2006-01-02 15:04:05.999999999-07:00",
			"2006-01-02 15:04:05",
		}
		for _, layout := range layouts {
			if ts, err := time.Parse(layout, t); err == nil {
				return ts
			}
		}
	}
	return time.Time{}
}
