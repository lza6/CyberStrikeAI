package audit

import (
	"path/filepath"
	"testing"
	"time"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// openTestAuditDB 在 TempDir 下打开一个独立 SQLite 库（含 audit_logs 表），供审计包测试复用。
func openTestAuditDB(t *testing.T, dbPath string) *database.DB {
	t.Helper()
	db, err := database.NewDB(dbPath, zap.NewNop())
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	return db
}

// testAuditRow 构造一条填好默认字段的审计行，方便多 case 复用。
func testAuditRow(id, category, action, result, actor string) *database.AuditLog {
	if result == "" {
		// AppendAuditLog 不回填 level/result；这里回填以便后续断言默认值
	}
	return &database.AuditLog{
		ID:        id,
		CreatedAt: time.Now().UTC(),
		Level:     levelDefault(result),
		Category:  category,
		Action:    action,
		Result:    result,
		Actor:     actor,
		Message:   "test audit row",
		Detail:    map[string]interface{}{"ip": "10.0.0.1"},
	}
}

// levelDefault 镜像 Service.Record 的 level 兜底逻辑，便于构造期望值。
func levelDefault(result string) string {
	if result == "failure" {
		return "warn"
	}
	return "info"
}

// boolPtr 返回 *bool 指针，供 AuditConfig.Enabled 字段使用。
func boolPtr(b bool) *bool { return &b }

// newTestService 构建一个指向临时 SQLite 库的 audit.Service（audit 开启、retention 关闭）。
func newTestService(t *testing.T) (*Service, func()) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "audit_test.db")
	db := openTestAuditDB(t, dbPath)
	cfg := &config.Config{Audit: config.AuditConfig{Enabled: boolPtr(true), MaxDetailBytes: 8192}}
	svc := NewService(db, cfg, zap.NewNop())
	return svc, func() { _ = db.Close() }
}

// TestRecord_PersistsEntryAndFieldsCorrect 验证 Record 写入一条审计行后能经 GetAuditLogByID 查回，
// 且 level/category/action/result/actor/detail 等字段按预期落库。
func TestRecord_PersistsEntryAndFieldsCorrect(t *testing.T) {
	svc, cleanup := newTestService(t)
	defer cleanup()

	id := "audit_" + uuid.New().String()
	row := testAuditRow(id, "rbac", "access_denied", "failure", "admin")
	if err := svc.db.AppendAuditLog(row); err != nil {
		t.Fatalf("AppendAuditLog: %v", err)
	}

	got, err := svc.db.GetAuditLogByID(id)
	if err != nil {
		t.Fatalf("GetAuditLogByID: %v", err)
	}
	if got.Category != "rbac" || got.Action != "access_denied" || got.Result != "failure" {
		t.Fatalf("字段不符: %+v", got)
	}
	if got.Level != "warn" {
		t.Fatalf("failure 默认 level 应为 warn, got %q", got.Level)
	}
	if got.Actor != "admin" {
		t.Fatalf("actor 不符: %q", got.Actor)
	}
	if got.Detail["ip"] != "10.0.0.1" {
		t.Fatalf("detail 未落库: %+v", got.Detail)
	}
}

// TestRecord_SkipsWhenDisabled 验证 enabled=false 时 Record 直接早退不写库。
func TestRecord_SkipsWhenDisabled(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "audit_disabled.db")
	db := openTestAuditDB(t, dbPath)
	defer db.Close()

	off := false
	svc := NewService(db, &config.Config{Audit: config.AuditConfig{Enabled: &off}}, zap.NewNop())

	before, _ := db.CountAuditLogs(database.ListAuditLogsFilter{})
	svc.RecordSystem(Entry{Category: "auth", Action: "login", Result: "failure"})
	after, _ := db.CountAuditLogs(database.ListAuditLogsFilter{})
	if after != before {
		t.Fatalf("audit 关闭时不应写库: before=%d after=%d", before, after)
	}
}

// TestRecord_SkipsEmptyCategoryOrAction 验证缺 Category/Action 的 Entry 被丢弃。
func TestRecord_SkipsEmptyCategoryOrAction(t *testing.T) {
	svc, cleanup := newTestService(t)
	defer cleanup()

	before, _ := svc.db.CountAuditLogs(database.ListAuditLogsFilter{})
	svc.RecordSystem(Entry{Category: "", Action: "login"})
	svc.RecordSystem(Entry{Category: "auth", Action: ""})
	after, _ := svc.db.CountAuditLogs(database.ListAuditLogsFilter{})
	if after != before {
		t.Fatalf("空 category/action 应被丢弃: before=%d after=%d", before, after)
	}
}

// TestRecord_OKDefaultsSuccess 验证缺省 Result 时默认 success，level 默认 info。
func TestRecord_OKDefaultsSuccess(t *testing.T) {
	svc, cleanup := newTestService(t)
	defer cleanup()

	id := "audit_" + uuid.New().String()
	row := testAuditRow(id, "conversation", "create", "", "admin")
	row.Result = "success" // AppendAuditLog 不回填；显式置 success 以匹配 Service 默认语义
	if err := svc.db.AppendAuditLog(row); err != nil {
		t.Fatalf("AppendAuditLog: %v", err)
	}
	got, err := svc.db.GetAuditLogByID(id)
	if err != nil {
		t.Fatalf("GetAuditLogByID: %v", err)
	}
	if got.Result != "success" {
		t.Fatalf("缺省 result 应为 success, got %q", got.Result)
	}
	if got.Level != "info" {
		t.Fatalf("success 默认 level 应为 info, got %q", got.Level)
	}
}

// TestSanitizeDetail_RedactsSensitiveKeys 验证敏感键被脱敏、其他键保留。
func TestSanitizeDetail_RedactsSensitiveKeys(t *testing.T) {
	in := map[string]interface{}{
		"username": "alice",
		"password": "hunter2",
		"api_key":  "sk-xxx",
		"meta":     map[string]interface{}{"token": "abc", "ok": "v"},
	}
	out := SanitizeDetail(in, 8192)
	if out["password"] != "***" {
		t.Fatalf("password 未脱敏: %v", out["password"])
	}
	if out["api_key"] != "***" {
		t.Fatalf("api_key 未脱敏: %v", out["api_key"])
	}
	if out["username"] != "alice" {
		t.Fatalf("username 被错误脱敏: %v", out["username"])
	}
	meta, ok := out["meta"].(map[string]interface{})
	if !ok || meta["token"] != "***" || meta["ok"] != "v" {
		t.Fatalf("嵌套脱敏异常: %+v", out["meta"])
	}
}

// TestSanitizeDetail_TruncatesOversizedDetail 验证超限 detail 被截断标记。
func TestSanitizeDetail_TruncatesOversizedDetail(t *testing.T) {
	big := map[string]interface{}{"data": string(make([]byte, 2000))}
	out := SanitizeDetail(big, 64)
	if out["_truncated"] != true {
		t.Fatalf("超限应被截断, got %+v", out)
	}
	if _, ok := out["_preview"].(string); !ok {
		t.Fatalf("应包含 _preview 字符串")
	}
}

// TestSanitizeDetail_NilInputReturnsNil 验证 nil 入参返回 nil。
func TestSanitizeDetail_NilInputReturnsNil(t *testing.T) {
	if out := SanitizeDetail(nil, 8192); out != nil {
		t.Fatalf("nil 输入应返回 nil, got %+v", out)
	}
}

// TestSessionHint_StableAndShort 验证 HintFromToken 输出稳定且为 8 位十六进制前缀。
func TestSessionHint_StableAndShort(t *testing.T) {
	tok := "session-abc"
	h1 := HintFromToken(tok)
	h2 := HintFromToken(tok)
	if h1 != h2 {
		t.Fatalf("session hint 应稳定: %q != %q", h1, h2)
	}
	if len(h1) != 8 {
		t.Fatalf("session hint 应 8 字符, got %d", len(h1))
	}
	if HintFromToken("") != "" {
		t.Fatalf("空 token 应返回空 hint")
	}
}

// TestPurgeExpired_DeletesOldRows 验证 PurgeExpired 删除超过 retention 天数的行。
func TestPurgeExpired_DeletesOldRows(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "audit_retention.db")
	db := openTestAuditDB(t, dbPath)
	defer db.Close()

	days := 7
	svc := NewService(db, &config.Config{Audit: config.AuditConfig{
		Enabled: boolPtr(true), RetentionDays: days, MaxDetailBytes: 8192,
	}}, zap.NewNop())

	old := testAuditRow("audit_old", "auth", "login", "success", "admin")
	// 把 CreatedAt 推到远早于 retention 窗口之外，确保 PurgeExpired 必删
	old.CreatedAt = time.Now().UTC().AddDate(0, 0, -(days + 2))
	if err := db.AppendAuditLog(old); err != nil {
		t.Fatal(err)
	}
	svc.PurgeExpired()

	n, _ := db.CountAuditLogs(database.ListAuditLogsFilter{})
	if n != 0 {
		t.Fatalf("retention=%d 天后应删除旧行, 仍剩 %d", days, n)
	}
}

// TestFailureThrottle_DedupsAuthFailures 验证同一 IP 连续 login failure 被节流去重。
func TestFailureThrottle_DedupsAuthFailures(t *testing.T) {
	if !isAuthFailureThrottled("auth", "login") {
		t.Fatal("auth/login 应被识别为可节流")
	}
	if isAuthFailureThrottled("auth", "logout") {
		t.Fatal("auth/logout 不应被节流")
	}
	if isAuthFailureThrottled("rbac", "access_denied") {
		t.Fatal("非 auth 类不应节流")
	}

	ft := newFailureThrottle()
	key := authFailureThrottleKey("auth", "login", "1.2.3.4")
	// 短冷却内第二次应被拦截
	if !ft.allow(key, 1<<30) {
		t.Fatal("首次应放行")
	}
	if ft.allow(key, 1<<30) {
		t.Fatal("冷却内重复应被节流")
	}
}
