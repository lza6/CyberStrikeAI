package database

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// formatSQLiteUTC stores instants as UTC RFC3339 for consistent SQLite reads/writes.
func formatSQLiteUTC(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// formatNullableSQLiteUTC 是 formatSQLiteUTC 的可空版本，用于 DATETIME 列写入：
// Invalid 传 nil（NULL），Valid 传 UTC RFC3339 文本。理由见 formatSQLiteUTC——
// 必须避免 modernc 驱动把 time.Time 序列化为带 "m=+..." 后缀的 Go 文本，
// 该文本无法被 SQLite 时间函数（julianday/strftime/datetime/date）解析。
func formatNullableSQLiteUTC(t sql.NullTime) interface{} {
	if !t.Valid {
		return nil
	}
	return formatSQLiteUTC(t.Time)
}

// sqliteEpochGE returns SQL comparing column to param as Unix seconds (timezone-safe).
func sqliteEpochGE(column, op string) string {
	return "strftime('%s', " + column + ") " + op + " strftime('%s', ?)"
}

// ParseRFC3339Time parses API/query timestamps (RFC3339 or RFC3339Nano).
func ParseRFC3339Time(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, errors.New("empty time value")
	}
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t.UTC(), nil
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}
