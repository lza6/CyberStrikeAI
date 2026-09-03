package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/security"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// newProcessDetailsTestEnv 建测试 DB + 会话 + 指定角色消息，返回 (db, conversation)。
func newProcessDetailsTestEnv(t *testing.T, name string) (*database.DB, *database.Conversation) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := database.NewDB(filepath.Join(t.TempDir(), name), zap.NewNop())
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	conversation, err := db.CreateConversation("waterfall", database.ConversationCreateMeta{})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	return db, conversation
}

// TestConversationProcessDetailsAggregatesAcrossMessages 验证 conversation 级端点
// 跨消息聚合 processDetails，且字段结构与前端 trace waterfall 读取的对齐
//（processDetails[].eventType / data.toolCallId / data.toolName / data.einoScope / createdAt）。
func TestConversationProcessDetailsAggregatesAcrossMessages(t *testing.T) {
	db, conversation := newProcessDetailsTestEnv(t, "conv-process-details.db")

	msg1, err := db.AddMessage(conversation.ID, "assistant", "turn 1", nil)
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	msg2, err := db.AddMessage(conversation.ID, "assistant", "turn 2", nil)
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	// msg1: tool_call + tool_result（不同 toolCallId）
	if err := db.AddProcessDetail(msg1.ID, conversation.ID, "tool_call", "call", map[string]interface{}{
		"toolName": "exec", "toolCallId": "call-1", "einoScope": "sub", "einoAgent": "recon",
	}); err != nil {
		t.Fatalf("AddProcessDetail(tool_call): %v", err)
	}
	if err := db.AddProcessDetail(msg1.ID, conversation.ID, "tool_result", "result", map[string]interface{}{
		"toolName": "exec", "toolCallId": "call-1", "success": true,
	}); err != nil {
		t.Fatalf("AddProcessDetail(tool_result): %v", err)
	}
	// msg2: 未闭合的 tool_call
	if err := db.AddProcessDetail(msg2.ID, conversation.ID, "tool_call", "call", map[string]interface{}{
		"toolName": "http_request", "toolCallId": "call-2",
	}); err != nil {
		t.Fatalf("AddProcessDetail(tool_call): %v", err)
	}

	handler := NewConversationHandler(db, zap.NewNop())
	request := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/conversations/"+conversation.ID+"/process-details", nil)
		c.Params = gin.Params{{Key: "id", Value: conversation.ID}}
		c.Set(security.ContextSessionKey, security.Session{UserID: "u1", Scope: database.RBACScopeAll})
		handler.GetConversationProcessDetails(c)
		return w
	}

	w := request()
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		ProcessDetails []map[string]interface{} `json:"processDetails"`
		Total          int                      `json:"total"`
		HasMore        bool                     `json:"hasMore"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.ProcessDetails) != 3 || response.Total != 3 {
		t.Fatalf("processDetails = %d total = %d, want 3/3", len(response.ProcessDetails), response.Total)
	}

	// 断言聚合结构：事件类型按时间正序跨两条消息聚合
	eventTypes := make([]string, 0, len(response.ProcessDetails))
	for _, d := range response.ProcessDetails {
		et, _ := d["eventType"].(string)
		eventTypes = append(eventTypes, et)
		if d["conversationId"] != conversation.ID {
			t.Fatalf("conversationId = %#v, want %s", d["conversationId"], conversation.ID)
		}
		if _, ok := d["createdAt"]; !ok {
			t.Fatalf("createdAt missing in %#v", d)
		}
	}
	want := []string{"tool_call", "tool_result", "tool_call"}
	for i, wt := range want {
		if eventTypes[i] != wt {
			t.Fatalf("eventTypes[%d] = %q, want %q (all=%v)", i, eventTypes[i], wt, eventTypes)
		}
	}

	// 前端 waterfall 读取 data.toolCallId / data.toolName / data.einoScope
	first := response.ProcessDetails[0]
	data, ok := first["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("data = %#v", first["data"])
	}
	if data["toolCallId"] != "call-1" {
		t.Fatalf("toolCallId = %#v, want call-1", data["toolCallId"])
	}
	if data["toolName"] != "exec" {
		t.Fatalf("toolName = %#v, want exec", data["toolName"])
	}
	if data["einoScope"] != "sub" {
		t.Fatalf("einoScope = %#v, want sub", data["einoScope"])
	}
}

// TestConversationProcessDetailsLimitWindow 验证 limit 只取最近 N 条且按时间正序返回。
func TestConversationProcessDetailsLimitWindow(t *testing.T) {
	db, conversation := newProcessDetailsTestEnv(t, "conv-process-details-limit.db")
	message, err := db.AddMessage(conversation.ID, "assistant", "done", nil)
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	for i := 1; i <= 6; i++ {
		id := "call-" + string(rune('a'+i-1))
		if err := db.AddProcessDetail(message.ID, conversation.ID, "tool_call", "call", map[string]interface{}{
			"toolName": "exec", "toolCallId": id,
		}); err != nil {
			t.Fatalf("AddProcessDetail(%d): %v", i, err)
		}
		time.Sleep(2 * time.Millisecond) // 保证 created_at 可区分
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/conversations/"+conversation.ID+"/process-details?limit=3", nil)
	c.Params = gin.Params{{Key: "id", Value: conversation.ID}}
	c.Set(security.ContextSessionKey, security.Session{UserID: "u1", Scope: database.RBACScopeAll})
	NewConversationHandler(db, zap.NewNop()).GetConversationProcessDetails(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		ProcessDetails []map[string]interface{} `json:"processDetails"`
		Total          int                      `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.ProcessDetails) != 3 {
		t.Fatalf("processDetails = %d, want 3", len(response.ProcessDetails))
	}
	// 应为最近 3 条（call-d/e/f），按正序返回
	wantIDs := []string{"call-d", "call-e", "call-f"}
	for i, d := range response.ProcessDetails {
		data, _ := d["data"].(map[string]interface{})
		if data["toolCallId"] != wantIDs[i] {
			t.Fatalf("details[%d].toolCallId = %#v, want %s", i, data["toolCallId"], wantIDs[i])
		}
	}
}

// TestConversationProcessDetailsRequiresConversationAccess 验证会话级权限校验：
// assigned scope 用户未获分配该会话 → 403。
func TestConversationProcessDetailsRequiresConversationAccess(t *testing.T) {
	db, conversation := newProcessDetailsTestEnv(t, "conv-process-details-rbac.db")
	message, err := db.AddMessage(conversation.ID, "assistant", "done", nil)
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if err := db.AddProcessDetail(message.ID, conversation.ID, "tool_call", "call", map[string]interface{}{
		"toolName": "exec", "toolCallId": "call-1",
	}); err != nil {
		t.Fatalf("AddProcessDetail: %v", err)
	}

	user, err := db.CreateRBACUser("limited-user", "Limited", "hash", true, nil)
	if err != nil {
		t.Fatalf("CreateRBACUser: %v", err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/conversations/"+conversation.ID+"/process-details", nil)
	c.Params = gin.Params{{Key: "id", Value: conversation.ID}}
	c.Set(security.ContextSessionKey, security.Session{UserID: user.ID, Scope: database.RBACScopeAssigned})
	NewConversationHandler(db, zap.NewNop()).GetConversationProcessDetails(c)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}
