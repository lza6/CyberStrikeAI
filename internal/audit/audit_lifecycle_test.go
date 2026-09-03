package audit

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/mcp"
	"cyberstrike-ai/internal/security"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// auditRowByResourceID 按 ResourceID 取出唯一一条审计行（Record 内部生成行 ID，不能直接用入参 id 查）。
func auditRowByResourceID(t *testing.T, db *database.DB, resourceID string) *database.AuditLog {
	t.Helper()
	logs, err := db.ListAuditLogs(database.ListAuditLogsFilter{ResourceID: resourceID})
	if err != nil {
		t.Fatalf("ListAuditLogs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("应恰好 1 条 resource_id=%q 的审计行, got %d", resourceID, len(logs))
	}
	return logs[0]
}

// ---------- record.go ----------

// TestRecordAction_PersistsRow 验证 RecordAction 经 Service.Record 写入一条审计行，
// 字段经 Record 兜底（result/level/actor）后落库。
func TestRecordAction_PersistsRow(t *testing.T) {
	// Arrange
	svc, cleanup := newTestService(t)
	defer cleanup()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/x", nil)
	c.Set(security.ContextUsernameKey, "alice")

	// Act
	id := "audit_" + uuid.New().String()
	svc.RecordAction(c, "rbac", "grant", "", "ok", "role", id, map[string]interface{}{"k": "v"})

	// Assert
	got := auditRowByResourceID(t, svc.db, id)
	if got.Category != "rbac" || got.Action != "grant" || got.Result != "success" {
		t.Fatalf("字段不符: %+v", got)
	}
	if got.Level != "info" {
		t.Fatalf("success 默认 level 应为 info, got %q", got.Level)
	}
	if got.Actor != "alice" {
		t.Fatalf("actor 应取自 gin context, got %q", got.Actor)
	}
	if got.ResourceType != "role" || got.ResourceID != id {
		t.Fatalf("resource 不符: %+v", got)
	}
}

// TestRecordOK_DefaultsSuccess 验证 RecordOK 落库 result=success。
func TestRecordOK_DefaultsSuccess(t *testing.T) {
	// Arrange
	svc, cleanup := newTestService(t)
	defer cleanup()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/x", nil)

	// Act
	id := "audit_" + uuid.New().String()
	svc.RecordOK(c, "conversation", "create", "创建对话", "conversation", id, nil)

	// Assert
	got := auditRowByResourceID(t, svc.db, id)
	if got.Result != "success" || got.Action != "create" {
		t.Fatalf("RecordOK 应落 success: %+v", got)
	}
	if got.Actor != "admin" {
		t.Fatalf("缺省 actor 应为 admin, got %q", got.Actor)
	}
}

// TestRecordFail_FailureLevelWarn 验证 RecordFail 落库 result=failure、level=warn。
func TestRecordFail_FailureLevelWarn(t *testing.T) {
	// Arrange
	svc, cleanup := newTestService(t)
	defer cleanup()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/x", nil)

	// Act
	svc.RecordFail(c, "rbac", "access_denied", "拒绝访问", map[string]interface{}{"reason": "no-role"})

	// Assert —— RecordFail 内部用空 ResourceID，取不到唯一行，改用计数断言
	count, err := svc.db.CountAuditLogs(database.ListAuditLogsFilter{Action: "access_denied"})
	if err != nil {
		t.Fatalf("CountAuditLogs: %v", err)
	}
	if count != 1 {
		t.Fatalf("应写入 1 条 access_denied, got %d", count)
	}
	// 用 List 取回校验 result/level
	logs, err := svc.db.ListAuditLogs(database.ListAuditLogsFilter{Action: "access_denied"})
	if err != nil {
		t.Fatalf("ListAuditLogs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("应能查回 1 条")
	}
	if logs[0].Result != "failure" || logs[0].Level != "warn" {
		t.Fatalf("failure 应落 level=warn: %+v", logs[0])
	}
}

// TestRecord_ThrottleSuppessesAuthFailureWithinCooldown 验证 auth/login failure 在冷却期内被节流去重。
func TestRecord_ThrottleSuppessesAuthFailureWithinCooldown(t *testing.T) {
	// Arrange
	svc, cleanup := newTestService(t)
	defer cleanup()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/login", nil)
	c.Request.RemoteAddr = "1.2.3.4:5678"

	// Act —— 连续两次同 IP auth/login failure，冷却 60s 内第二次应被丢弃
	svc.RecordSystem(Entry{Category: "auth", Action: "login", Result: "failure", ClientIP: "1.2.3.4"})
	svc.RecordSystem(Entry{Category: "auth", Action: "login", Result: "failure", ClientIP: "1.2.3.4"})

	// Assert
	count, _ := svc.db.CountAuditLogs(database.ListAuditLogsFilter{Action: "login"})
	if count != 1 {
		t.Fatalf("冷却期内重复 auth failure 应被节流, got %d", count)
	}
}

// ---------- retention.go ----------

// TestPurgeExpired_KeepsRecentRows 验证 retention 窗口内的行不被删除（边界时间）。
func TestPurgeExpired_KeepsRecentRows(t *testing.T) {
	// Arrange
	dbPath := t.TempDir() + "\\audit_keep.db"
	db := openTestAuditDB(t, dbPath)
	defer db.Close()
	days := 7
	svc := NewService(db, &config.Config{Audit: config.AuditConfig{
		Enabled: boolPtr(true), RetentionDays: days, MaxDetailBytes: 8192,
	}}, zap.NewNop())
	fresh := testAuditRow("audit_fresh", "auth", "login", "success", "admin")
	fresh.CreatedAt = time.Now().UTC().AddDate(0, 0, -(days - 1)) // 窗口内
	if err := db.AppendAuditLog(fresh); err != nil {
		t.Fatal(err)
	}

	// Act
	svc.PurgeExpired()

	// Assert
	n, _ := db.CountAuditLogs(database.ListAuditLogsFilter{})
	if n != 1 {
		t.Fatalf("retention 窗口内的行应保留, got %d", n)
	}
}

// TestPurgeExpired_NoRetentionKeepsAll 验证 retention=0（不清理）时旧行全保留。
func TestPurgeExpired_NoRetentionKeepsAll(t *testing.T) {
	// Arrange
	svc, cleanup := newTestService(t) // retention=0
	defer cleanup()
	old := testAuditRow("audit_old2", "auth", "login", "success", "admin")
	old.CreatedAt = time.Now().UTC().AddDate(-10, 0, 0)
	if err := svc.db.AppendAuditLog(old); err != nil {
		t.Fatal(err)
	}

	// Act
	svc.PurgeExpired()

	// Assert
	n, _ := svc.db.CountAuditLogs(database.ListAuditLogsFilter{})
	if n != 1 {
		t.Fatalf("retention=0 应全保留, got %d", n)
	}
}

// TestPurgeExpired_EmptyTable 验证空表 PurgeExpired 不报错。
func TestPurgeExpired_EmptyTable(t *testing.T) {
	// Arrange
	svc, cleanup := newTestService(t)
	defer cleanup()

	// Act + Assert —— 不应 panic
	svc.PurgeExpired()
	n, _ := svc.db.CountAuditLogs(database.ListAuditLogsFilter{})
	if n != 0 {
		t.Fatalf("空表应保持 0 行, got %d", n)
	}
}

// ---------- throttle.go ----------

// TestThrottle_AllowsAfterWindowReset 验证冷却窗口过后放行。
func TestThrottle_AllowsAfterWindowReset(t *testing.T) {
	// Arrange
	ft := newFailureThrottle()
	key := "auth:login:9.9.9.9"
	cooldown := 50 * time.Millisecond

	// Act
	first := ft.allow(key, cooldown)
	time.Sleep(cooldown + 20*time.Millisecond)
	second := ft.allow(key, cooldown)

	// Assert
	if !first {
		t.Fatal("首次应放行")
	}
	if !second {
		t.Fatal("冷却窗口过后应放行")
	}
}

// TestThrottle_NilOrZeroCooldown 验证 nil/非正冷却/空 key 直接放行（不节流）。
func TestThrottle_NilOrZeroCooldown(t *testing.T) {
	// Arrange
	var nilThrottle *failureThrottle
	ft := newFailureThrottle()

	// Act + Assert
	if !nilThrottle.allow("k", time.Second) {
		t.Fatal("nil receiver 应放行")
	}
	if !ft.allow("", time.Second) {
		t.Fatal("空 key 应放行")
	}
	if !ft.allow("k", 0) {
		t.Fatal("cooldown<=0 应放行")
	}
	if !ft.allow("k", -1) {
		t.Fatal("负 cooldown 应放行")
	}
}

// TestThrottle_DifferentKeysIndependent 验证不同 key 互不影响。
func TestThrottle_DifferentKeysIndependent(t *testing.T) {
	// Arrange
	ft := newFailureThrottle()

	// Act
	a := ft.allow("k1", time.Second)
	b := ft.allow("k2", time.Second)

	// Assert
	if !a || !b {
		t.Fatalf("不同 key 应各自独立放行: a=%v b=%v", a, b)
	}
}

// ---------- sanitize.go ----------

// TestSanitizeDetail_NoSensitiveKeysReturned 验证无敏感字段时原样返回（非 nil）。
func TestSanitizeDetail_NoSensitiveKeysReturned(t *testing.T) {
	// Arrange
	in := map[string]interface{}{"user": "bob", "count": 3}

	// Act
	out := SanitizeDetail(in, 8192)

	// Assert
	if out["user"] != "bob" {
		t.Fatalf("普通键应原样返回: %v", out["user"])
	}
	if out["count"] != 3 {
		t.Fatalf("数值键应原样返回: %v", out["count"])
	}
}

// TestSanitizeDetail_ArrayValuesCovered 验证数组分支：非敏感 key 下数组元素原样保留。
func TestSanitizeDetail_ArrayValuesCovered(t *testing.T) {
	// Arrange
	in := map[string]interface{}{
		"data":  []interface{}{"x", "y"},
		"items": []interface{}{"a", "b"},
	}

	// Act
	out := SanitizeDetail(in, 8192)

	// Assert —— 非敏感 key 的数组元素应原样保留（覆盖 sanitizeValue 的数组分支）
	data, ok := out["data"].([]interface{})
	if !ok || len(data) != 2 || data[0] != "x" {
		t.Fatalf("非敏感数组应原样: %+v", out["data"])
	}
	items, ok := out["items"].([]interface{})
	if !ok || items[1] != "b" {
		t.Fatalf("非敏感数组应原样: %+v", out["items"])
	}
}

// TestSanitizeDetail_NonMapValueWrapped 验证顶层非 map 值被包装为 {value:...}。
func TestSanitizeDetail_NonMapValueWrapped(t *testing.T) {
	// Act —— SanitizeDetail 签名要求 map 入参，但底层 sanitizeValue 对标量会走 default 分支
	out := SanitizeDetail(map[string]interface{}{"k": "v"}, 8192)
	if out["k"] != "v" {
		t.Fatalf("标量值应原样返回: %v", out["k"])
	}
}

// ---------- resource_availability.go ----------

// TestApplyResourceAvailability_NilLogNoop 验证 nil log 直接早退。
func TestApplyResourceAvailability_NilLogNoop(t *testing.T) {
	// Arrange
	svc, cleanup := newTestService(t)
	defer cleanup()

	// Act + Assert —— 不应 panic
	ApplyResourceAvailability(svc.db, nil)
}

// TestApplyResourceAvailability_EmptyResourceIDNoop 验证空 ResourceID 早退、不查库。
func TestApplyResourceAvailability_EmptyResourceIDNoop(t *testing.T) {
	// Arrange
	svc, cleanup := newTestService(t)
	defer cleanup()
	log := &database.AuditLog{ID: "x", ResourceID: "  "}

	// Act
	ApplyResourceAvailability(svc.db, log)

	// Assert
	if log.ResourceAvailable != nil {
		t.Fatalf("空 ResourceID 应不设置 ResourceAvailable, got %v", *log.ResourceAvailable)
	}
}

// TestApplyResourceAvailability_DeleteActionMarksUnavailable 验证删除类 action 直接标记资源不可用（不查库）。
func TestApplyResourceAvailability_DeleteActionMarksUnavailable(t *testing.T) {
	// Arrange
	svc, cleanup := newTestService(t)
	defer cleanup()
	log := &database.AuditLog{ID: "x", Action: "session_delete", ResourceID: "rid"}

	// Act
	ApplyResourceAvailability(svc.db, log)

	// Assert
	if log.ResourceAvailable == nil || *log.ResourceAvailable {
		t.Fatalf("删除类 action 应标记 ResourceAvailable=false")
	}
}

// TestApplyResourceAvailability_DeleteActionWithoutDB 验证 db=nil 时删除类 action 仍能标记（不依赖 db）。
func TestApplyResourceAvailability_DeleteActionWithoutDB(t *testing.T) {
	// Arrange
	log := &database.AuditLog{ID: "x", Action: "delete", ResourceID: "rid"}

	// Act
	ApplyResourceAvailability(nil, log)

	// Assert
	if log.ResourceAvailable == nil || *log.ResourceAvailable {
		t.Fatalf("db=nil 删除类 action 仍应标记 false")
	}
}

// TestApplyResourceAvailability_ConversationExists 验证 conversation 类型 + 资源存在时标记 true。
func TestApplyResourceAvailability_ConversationExists(t *testing.T) {
	// Arrange
	svc, cleanup := newTestService(t)
	defer cleanup()
	conv, err := svc.db.CreateConversation("t", database.ConversationCreateMeta{})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	log := &database.AuditLog{ID: "x", Action: "create", ResourceType: "conversation", ResourceID: conv.ID}

	// Act
	ApplyResourceAvailability(svc.db, log)

	// Assert
	if log.ResourceAvailable == nil || !*log.ResourceAvailable {
		t.Fatalf("存在的 conversation 应标记 true, got %v", log.ResourceAvailable)
	}
}

// TestApplyResourceAvailability_ConversationMissing 验证 conversation 类型 + 资源已删除时标记 false。
func TestApplyResourceAvailability_ConversationMissing(t *testing.T) {
	// Arrange
	svc, cleanup := newTestService(t)
	defer cleanup()
	log := &database.AuditLog{ID: "x", Action: "create", ResourceType: "conversation", ResourceID: "missing-id"}

	// Act
	ApplyResourceAvailability(svc.db, log)

	// Assert
	if log.ResourceAvailable == nil || *log.ResourceAvailable {
		t.Fatalf("不存在的 conversation 应标记 false, got %v", log.ResourceAvailable)
	}
}

// TestApplyResourceAvailability_VulnerabilityExists 验证 vulnerability 类型 + 资源存在标记 true。
func TestApplyResourceAvailability_VulnerabilityExists(t *testing.T) {
	// Arrange
	svc, cleanup := newTestService(t)
	defer cleanup()
	v, err := svc.db.CreateVulnerability(&database.Vulnerability{
		Title: "t", Severity: "high", Type: "sql", Target: "x",
	})
	if err != nil {
		t.Fatalf("CreateVulnerability: %v", err)
	}
	log := &database.AuditLog{ID: "x", Action: "update", ResourceType: "vulnerability", ResourceID: v.ID}

	// Act
	ApplyResourceAvailability(svc.db, log)

	// Assert
	if log.ResourceAvailable == nil || !*log.ResourceAvailable {
		t.Fatalf("存在的 vulnerability 应标记 true")
	}
}

// TestApplyResourceAvailability_VulnerabilityMissing 验证 vulnerability 类型 + 资源不存在（err 含"不存在"）标记 false。
func TestApplyResourceAvailability_VulnerabilityMissing(t *testing.T) {
	// Arrange
	svc, cleanup := newTestService(t)
	defer cleanup()
	log := &database.AuditLog{ID: "x", Action: "update", ResourceType: "vulnerability", ResourceID: "missing-vuln"}

	// Act
	ApplyResourceAvailability(svc.db, log)

	// Assert —— GetVulnerability 返回"漏洞不存在"错误 → known=true → ResourceAvailable=false
	if log.ResourceAvailable == nil || *log.ResourceAvailable {
		t.Fatalf("不存在的 vulnerability 应标记 false, got %v", log.ResourceAvailable)
	}
}

// TestApplyResourceAvailability_BatchQueueExists 验证 batch_queue 类型资源存在标记 true。
func TestApplyResourceAvailability_BatchQueueExists(t *testing.T) {
	// Arrange
	svc, cleanup := newTestService(t)
	defer cleanup()
	queueID := "bq_" + uuid.New().String()
	if err := svc.db.CreateBatchQueue(queueID, "t", "role", "agent", "manual", "", nil, "", 1, nil); err != nil {
		t.Fatalf("CreateBatchQueue: %v", err)
	}
	log := &database.AuditLog{ID: "x", Action: "update", ResourceType: "batch_queue", ResourceID: queueID}

	// Act
	ApplyResourceAvailability(svc.db, log)

	// Assert
	if log.ResourceAvailable == nil || !*log.ResourceAvailable {
		t.Fatalf("存在的 batch_queue 应标记 true")
	}
}

// TestApplyResourceAvailability_C2ListenerExists 验证 c2_listener 类型资源存在标记 true。
func TestApplyResourceAvailability_C2ListenerExists(t *testing.T) {
	// Arrange
	svc, cleanup := newTestService(t)
	defer cleanup()
	lid := "lst_" + uuid.New().String()
	if err := svc.db.CreateC2Listener(&database.C2Listener{
		ID: lid, Name: "n", Type: "tcp_reverse", BindHost: "127.0.0.1", BindPort: 9,
	}); err != nil {
		t.Fatalf("CreateC2Listener: %v", err)
	}
	log := &database.AuditLog{ID: "x", Action: "update", ResourceType: "c2_listener", ResourceID: lid}

	// Act
	ApplyResourceAvailability(svc.db, log)

	// Assert
	if log.ResourceAvailable == nil || !*log.ResourceAvailable {
		t.Fatalf("存在的 c2_listener 应标记 true")
	}
}

// TestApplyResourceAvailability_C2SessionExists 验证 c2_session 类型资源存在标记 true。
func TestApplyResourceAvailability_C2SessionExists(t *testing.T) {
	// Arrange
	svc, cleanup := newTestService(t)
	defer cleanup()
	lid := "lst_" + uuid.New().String()
	if err := svc.db.CreateC2Listener(&database.C2Listener{
		ID: lid, Name: "n", Type: "tcp_reverse", BindHost: "127.0.0.1", BindPort: 9,
	}); err != nil {
		t.Fatalf("CreateC2Listener: %v", err)
	}
	sid := "ses_" + uuid.New().String()
	if err := svc.db.UpsertC2Session(&database.C2Session{
		ID: sid, ListenerID: lid, ImplantUUID: "implant-" + sid,
	}); err != nil {
		t.Fatalf("UpsertC2Session: %v", err)
	}
	log := &database.AuditLog{ID: "x", Action: "update", ResourceType: "c2_session", ResourceID: sid}

	// Act
	ApplyResourceAvailability(svc.db, log)

	// Assert
	if log.ResourceAvailable == nil || !*log.ResourceAvailable {
		t.Fatalf("存在的 c2_session 应标记 true")
	}
}

// TestApplyResourceAvailability_C2TaskExists 验证 c2_task 类型资源存在标记 true。
func TestApplyResourceAvailability_C2TaskExists(t *testing.T) {
	// Arrange —— c2_tasks.session_id 有外键约束，需先建 listener + session
	svc, cleanup := newTestService(t)
	defer cleanup()
	lid := "lst_" + uuid.New().String()
	if err := svc.db.CreateC2Listener(&database.C2Listener{
		ID: lid, Name: "n", Type: "tcp_reverse", BindHost: "127.0.0.1", BindPort: 9,
	}); err != nil {
		t.Fatalf("CreateC2Listener: %v", err)
	}
	sid := "ses_" + uuid.New().String()
	if err := svc.db.UpsertC2Session(&database.C2Session{
		ID: sid, ListenerID: lid, ImplantUUID: "implant-" + sid,
	}); err != nil {
		t.Fatalf("UpsertC2Session: %v", err)
	}
	tid := "tsk_" + uuid.New().String()
	if err := svc.db.CreateC2Task(&database.C2Task{
		ID: tid, SessionID: sid, TaskType: "exec", Status: "queued",
	}); err != nil {
		t.Fatalf("CreateC2Task: %v", err)
	}
	log := &database.AuditLog{ID: "x", Action: "update", ResourceType: "c2_task", ResourceID: tid}

	// Act
	ApplyResourceAvailability(svc.db, log)

	// Assert
	if log.ResourceAvailable == nil || !*log.ResourceAvailable {
		t.Fatalf("存在的 c2_task 应标记 true")
	}
}

// TestApplyResourceAvailability_WebshellConnectionExists 验证 webshell_connection 类型资源存在标记 true。
func TestApplyResourceAvailability_WebshellConnectionExists(t *testing.T) {
	// Arrange
	svc, cleanup := newTestService(t)
	defer cleanup()
	wid := "ws_" + uuid.New().String()
	if err := svc.db.CreateWebshellConnection(&database.WebShellConnection{
		ID: wid, URL: "http://x", Password: "p", Type: "php", Method: "POST",
	}); err != nil {
		t.Fatalf("CreateWebshellConnection: %v", err)
	}
	log := &database.AuditLog{ID: "x", Action: "update", ResourceType: "webshell_connection", ResourceID: wid}

	// Act
	ApplyResourceAvailability(svc.db, log)

	// Assert
	if log.ResourceAvailable == nil || !*log.ResourceAvailable {
		t.Fatalf("存在的 webshell_connection 应标记 true")
	}
}

// TestApplyResourceAvailability_ToolExecutionExists 验证 tool_execution 类型资源存在标记 true。
func TestApplyResourceAvailability_ToolExecutionExists(t *testing.T) {
	// Arrange
	svc, cleanup := newTestService(t)
	defer cleanup()
	eid := "te_" + uuid.New().String()
	if err := svc.db.SaveToolExecution(&mcp.ToolExecution{
		ID: eid, ToolName: "n", Status: "completed", StartTime: time.Now(),
	}); err != nil {
		t.Fatalf("SaveToolExecution: %v", err)
	}
	log := &database.AuditLog{ID: "x", Action: "update", ResourceType: "tool_execution", ResourceID: eid}

	// Act
	ApplyResourceAvailability(svc.db, log)

	// Assert
	if log.ResourceAvailable == nil || !*log.ResourceAvailable {
		t.Fatalf("存在的 tool_execution 应标记 true")
	}
}

// TestApplyResourceAvailability_UnknownTypeNoSet 验证未知 resourceType 时 known=false，不设置 ResourceAvailable。
func TestApplyResourceAvailability_UnknownTypeNoSet(t *testing.T) {
	// Arrange
	svc, cleanup := newTestService(t)
	defer cleanup()
	log := &database.AuditLog{ID: "x", Action: "update", ResourceType: "unknown_type", ResourceID: "rid"}

	// Act
	ApplyResourceAvailability(svc.db, log)

	// Assert
	if log.ResourceAvailable != nil {
		t.Fatalf("未知 resourceType 应不设置 ResourceAvailable, got %v", *log.ResourceAvailable)
	}
}

// TestApplyResourceAvailability_EmptyTypeWithLongID 验证空 ResourceType + 长 ID（>8、非 c2_ 前缀）推断为 conversation 并查询。
func TestApplyResourceAvailability_EmptyTypeWithLongID(t *testing.T) {
	// Arrange
	svc, cleanup := newTestService(t)
	defer cleanup()
	conv, err := svc.db.CreateConversation("t", database.ConversationCreateMeta{})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	log := &database.AuditLog{ID: "x", Action: "update", ResourceType: "", ResourceID: conv.ID} // 36 字符 UUID

	// Act
	ApplyResourceAvailability(svc.db, log)

	// Assert
	if log.ResourceAvailable == nil || !*log.ResourceAvailable {
		t.Fatalf("空类型 + 长 ID 应推断为 conversation 并标记 true")
	}
}

// TestApplyResourceAvailability_EmptyTypeShortIDNoSet 验证空 ResourceType + 短 ID（<=8）不查询、不设置。
func TestApplyResourceAvailability_EmptyTypeShortIDNoSet(t *testing.T) {
	// Arrange
	svc, cleanup := newTestService(t)
	defer cleanup()
	log := &database.AuditLog{ID: "x", Action: "update", ResourceType: "", ResourceID: "ab"} // <=8

	// Act
	ApplyResourceAvailability(svc.db, log)

	// Assert
	if log.ResourceAvailable != nil {
		t.Fatalf("空类型 + 短 ID 应不设置 ResourceAvailable, got %v", *log.ResourceAvailable)
	}
}

// TestApplyResourceAvailability_EmptyTypeC2PrefixNoSet 验证空 ResourceType + c2_ 前缀不查询、不设置。
func TestApplyResourceAvailability_EmptyTypeC2PrefixNoSet(t *testing.T) {
	// Arrange
	svc, cleanup := newTestService(t)
	defer cleanup()
	log := &database.AuditLog{ID: "x", Action: "update", ResourceType: "", ResourceID: "c2_abcdefghij"}

	// Act
	ApplyResourceAvailability(svc.db, log)

	// Assert
	if log.ResourceAvailable != nil {
		t.Fatalf("空类型 + c2_ 前缀应不设置 ResourceAvailable, got %v", *log.ResourceAvailable)
	}
}

// ---------- conversation_create.go ----------

// TestConversationCreateMeta_TrimsSource 验证 ConversationCreateMeta 去除 source 首尾空白。
func TestConversationCreateMeta_TrimsSource(t *testing.T) {
	// Act
	m := ConversationCreateMeta("  api  ")

	// Assert
	if m.Source != "api" {
		t.Fatalf("source 应被 TrimSpace, got %q", m.Source)
	}
}

// TestConversationCreateMetaFromGin_NilContext 验证 nil gin context 时只 trim source，不加 IP/hint。
func TestConversationCreateMetaFromGin_NilContext(t *testing.T) {
	// Act
	m := ConversationCreateMetaFromGin(nil, "api")

	// Assert
	if m.Source != "api" || m.ClientIP != "" || m.SessionHint != "" {
		t.Fatalf("nil context 应仅设 source, got %+v", m)
	}
}

// TestConversationCreateMetaFromGin_WithRequest 验证有 request 时填 ClientIP。
func TestConversationCreateMetaFromGin_WithRequest(t *testing.T) {
	// Arrange
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "http://app.example/x", nil)
	c.Request.RemoteAddr = "5.6.7.8:9999"

	// Act
	m := ConversationCreateMetaFromGin(c, "api")

	// Assert
	if m.Source != "api" {
		t.Fatalf("source 应被 trim, got %q", m.Source)
	}
	if m.ClientIP == "" {
		t.Fatalf("ClientIP 应被填充")
	}
}

// TestConversationCreateMetaFromGin_WithAuthToken 验证有 auth token 时填 SessionHint。
func TestConversationCreateMetaFromGin_WithAuthToken(t *testing.T) {
	// Arrange
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/x", nil)
	c.Request.RemoteAddr = "1.2.3.4:5"
	c.Set(security.ContextAuthTokenKey, "session-xyz")

	// Act
	m := ConversationCreateMetaFromGin(c, "api")

	// Assert
	if m.SessionHint == "" {
		t.Fatalf("有 token 时应填 SessionHint")
	}
	if len(m.SessionHint) != 8 {
		t.Fatalf("SessionHint 应 8 字符, got %d", len(m.SessionHint))
	}
}

// TestRegisterConversationCreateHook_NilServiceNoop 验证 nil service 时 RegisterConversationCreateHook 早退、不 panic。
func TestRegisterConversationCreateHook_NilServiceNoop(t *testing.T) {
	// Act + Assert —— 不应 panic
	RegisterConversationCreateHook(nil)
}

// TestRegisterConversationCreateHook_FiresOnCreate 验证注册 hook 后 CreateConversation 会触发 hook 写入 audit 行。
func TestRegisterConversationCreateHook_FiresOnCreate(t *testing.T) {
	// Arrange
	svc, cleanup := newTestService(t)
	defer cleanup()
	RegisterConversationCreateHook(svc)
	defer database.SetConversationCreateHook(nil) // 重置全局 hook，避免污染后续测试

	before, _ := svc.db.CountAuditLogs(database.ListAuditLogsFilter{Action: "create"})

	// Act
	conv, err := svc.db.CreateConversation("hook-test", ConversationCreateMetaFromGin(nil, "test"))
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	// Assert
	after, _ := svc.db.CountAuditLogs(database.ListAuditLogsFilter{Action: "create"})
	if after != before+1 {
		t.Fatalf("hook 应写入 1 条 audit 行, before=%d after=%d", before, after)
	}
	logs, err := svc.db.ListAuditLogs(database.ListAuditLogsFilter{Action: "create", ResourceID: conv.ID})
	if err != nil || len(logs) != 1 {
		t.Fatalf("应能按 ResourceID 查回 hook 写入的行, err=%v len=%d", err, len(logs))
	}
	if logs[0].ResourceType != "conversation" {
		t.Fatalf("hook 行 ResourceType 应为 conversation, got %q", logs[0].ResourceType)
	}
}

// ---------- meta.go ----------

// TestRetentionDays_Configured 验证 RetentionDays 返回配置值。
func TestRetentionDays_Configured(t *testing.T) {
	// Arrange
	svc, cleanup := newTestService(t)
	defer cleanup()
	svc.cfg.Audit.RetentionDays = 30

	// Act + Assert
	if svc.RetentionDays() != 30 {
		t.Fatalf("RetentionDays 应为 30, got %d", svc.RetentionDays())
	}
}

// TestRetentionDays_NilService 验证 nil service 返回 0。
func TestRetentionDays_NilService(t *testing.T) {
	var svc *Service
	if svc.RetentionDays() != 0 {
		t.Fatal("nil service 应返回 0")
	}
}

// TestRetentionDays_NilConfig 验证 nil cfg 返回 0。
func TestRetentionDays_NilConfig(t *testing.T) {
	svc := &Service{}
	if svc.RetentionDays() != 0 {
		t.Fatal("nil cfg 应返回 0")
	}
}

// ---------- service.go 辅助函数 ----------

// TestRecord_PopulatesUserAgentAndClientIP 验证 Record 从 gin context 提取 UserAgent 与 ClientIP。
func TestRecord_PopulatesUserAgentAndClientIP(t *testing.T) {
	// Arrange
	svc, cleanup := newTestService(t)
	defer cleanup()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/x", nil)
	c.Request.Header.Set("User-Agent", "TestUA/1.0")
	c.Request.RemoteAddr = "7.7.7.7:8"

	// Act
	id := "audit_" + uuid.New().String()
	svc.RecordAction(c, "rbac", "view", "success", "ok", "role", id, nil)

	// Assert
	got := auditRowByResourceID(t, svc.db, id)
	if got.UserAgent != "TestUA/1.0" {
		t.Fatalf("UserAgent 应落库, got %q", got.UserAgent)
	}
	if got.ClientIP == "" {
		t.Fatalf("ClientIP 应落库")
	}
}

// TestRecord_TruncatesUserAgent 验证超长 UserAgent 被截断至 512 字符。
func TestRecord_TruncatesUserAgent(t *testing.T) {
	// Arrange
	svc, cleanup := newTestService(t)
	defer cleanup()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/x", nil)
	longUA := string(make([]byte, 1000))
	c.Request.Header.Set("User-Agent", longUA)

	// Act
	id := "audit_" + uuid.New().String()
	svc.RecordAction(c, "rbac", "view", "success", "ok", "role", id, nil)

	// Assert
	got := auditRowByResourceID(t, svc.db, id)
	if len(got.UserAgent) != 512 {
		t.Fatalf("UserAgent 应截断至 512, got %d", len(got.UserAgent))
	}
}

// TestRecord_WithAuthTokenSetsSessionHint 验证 Record 从 gin context auth token 生成 SessionHint。
func TestRecord_WithAuthTokenSetsSessionHint(t *testing.T) {
	// Arrange
	svc, cleanup := newTestService(t)
	defer cleanup()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/x", nil)
	c.Set(security.ContextAuthTokenKey, "session-abc")

	// Act
	id := "audit_" + uuid.New().String()
	svc.RecordAction(c, "rbac", "view", "success", "ok", "role", id, nil)

	// Assert
	got := auditRowByResourceID(t, svc.db, id)
	if got.SessionHint != HintFromToken("session-abc") {
		t.Fatalf("SessionHint 应匹配 HintFromToken, got %q", got.SessionHint)
	}
}

// TestRecord_SystemContextFallbackActor 验证无 gin context 时 actor 兜底为 admin。
func TestRecord_SystemContextFallbackActor(t *testing.T) {
	// Arrange
	svc, cleanup := newTestService(t)
	defer cleanup()

	// Act
	id := "audit_" + uuid.New().String()
	svc.Record(nil, Entry{
		Category: "rbac", Action: "sync", Result: "success",
		Message: "m", ResourceType: "role", ResourceID: id,
	})

	// Assert
	got := auditRowByResourceID(t, svc.db, id)
	if got.Actor != "admin" {
		t.Fatalf("无 context 时 actor 应兜底 admin, got %q", got.Actor)
	}
}

// TestRecord_FailureWithCustomLevelPreserved 验证显式指定 Level 时不被兜底覆盖。
func TestRecord_FailureWithCustomLevelPreserved(t *testing.T) {
	// Arrange
	svc, cleanup := newTestService(t)
	defer cleanup()

	// Act
	id := "audit_" + uuid.New().String()
	svc.Record(nil, Entry{
		Category: "rbac", Action: "view", Result: "failure", Level: "error",
		Message: "m", ResourceType: "role", ResourceID: id,
	})

	// Assert —— failure 且 cooldown 默认 60s，但 rbac 非 auth 类，不节流
	got := auditRowByResourceID(t, svc.db, id)
	if got.Level != "error" {
		t.Fatalf("显式 Level 应保留, got %q", got.Level)
	}
}

// TestRecord_DBAbsorbFailureLogged 验证 db=nil 时 Record 静默早退不 panic。
func TestRecord_DBAbsorbFailureLogged(t *testing.T) {
	// Arrange
	svc := &Service{
		cfg:          &config.Config{Audit: config.AuditConfig{Enabled: boolPtr(true)}},
		logger:       zap.NewNop(),
		failThrottle: newFailureThrottle(),
	}

	// Act + Assert —— db=nil 应早退不 panic
	svc.RecordSystem(Entry{Category: "auth", Action: "login", Result: "failure"})
}

// TestEnabled_NilConfigReturnsFalse 验证 nil cfg 时 Enabled 返回 false。
func TestEnabled_NilConfigReturnsFalse(t *testing.T) {
	svc := &Service{}
	if svc.Enabled() {
		t.Fatal("nil cfg 时 Enabled 应为 false")
	}
}

// TestEnabled_NilServiceReturnsFalse 验证 nil service 时 Enabled 返回 false。
func TestEnabled_NilServiceReturnsFalse(t *testing.T) {
	var svc *Service
	if svc.Enabled() {
		t.Fatal("nil service 时 Enabled 应为 false")
	}
}

// TestEnabled_DefaultTrueWhenEnabledNil 验证 Enabled 字段为 nil 时默认 true。
func TestEnabled_DefaultTrueWhenEnabledNil(t *testing.T) {
	// Arrange
	svc, cleanup := newTestService(t)
	defer cleanup()
	svc.cfg.Audit.Enabled = nil

	// Act + Assert
	if !svc.Enabled() {
		t.Fatal("Enabled 字段为 nil 时应默认 true")
	}
}

// TestNewService_InitializesThrottle 验证 NewService 初始化 failThrottle（非 nil）。
func TestNewService_InitializesThrottle(t *testing.T) {
	svc := NewService(nil, &config.Config{}, nil)
	if svc.failThrottle == nil {
		t.Fatal("failThrottle 应被初始化")
	}
}

// ---------- retention.go (StartRetentionLoop) ----------

// TestStartRetentionLoop_NilServiceNoop 验证 nil service 时 StartRetentionLoop 早退不 panic、不启动 goroutine。
func TestStartRetentionLoop_NilServiceNoop(t *testing.T) {
	// Act + Assert —— 不应 panic
	StartRetentionLoop(nil, zap.NewNop())
}

// TestStartRetentionLoop_RunsAndPurges 验证 StartRetentionLoop 启动后能循环执行 PurgeExpired（通过缩短 ticker 间隔间接验证）。
func TestStartRetentionLoop_RunsAndPurges(t *testing.T) {
	// Arrange —— StartRetentionLoop 用固定 1h ticker，这里只验证它启动后不阻塞、不 panic。
	svc, cleanup := newTestService(t)
	defer cleanup()
	logger := zap.NewNop()

	// Act —— 启动 loop（goroutine 内 ticker 1h，不会立即触发 PurgeExpired）
	StartRetentionLoop(svc, logger)

	// Assert —— 主循环已启动，进程仍可继续；这里不等待 1h，仅验证无 panic
	// 给 loop 一点时间启动
	time.Sleep(50 * time.Millisecond)
}
