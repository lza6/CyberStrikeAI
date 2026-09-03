package handler

import (
	"os"
	"path/filepath"
	"testing"

	"cyberstrike-ai/internal/database"

	"go.uber.org/zap"
)

func TestEnrichHitlApprovalPayload(t *testing.T) {
	tmp := t.TempDir()
	db, err := database.NewDB(filepath.Join(tmp, "test.sqlite"), zap.NewNop())
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	// Windows：t.TempDir 清理前必须关库句柄，否则 unlinkat 撞文件锁。
	defer func() {
		_ = db.Close()
		_ = os.RemoveAll(tmp)
	}()

	conv, err := db.CreateConversation("hitl ctx", database.ConversationCreateMeta{})
	if err != nil {
		t.Fatalf("conv: %v", err)
	}
	if _, err := db.AddMessage(conv.ID, "user", "scan 10.0.0.1 please", nil); err != nil {
		t.Fatalf("user msg: %v", err)
	}
	asst, err := db.AddMessage(conv.ID, "assistant", "", nil)
	if err != nil {
		t.Fatalf("asst msg: %v", err)
	}
	if err := db.AddProcessDetail(asst.ID, conv.ID, "thinking", "need port scan first", nil); err != nil {
		t.Fatalf("detail: %v", err)
	}

	h := &AgentHandler{db: db, tasks: NewAgentTaskManager()}
	payload := map[string]interface{}{"toolName": "nmap", "arguments": "{}"}
	h.enrichHitlApprovalPayload(conv.ID, asst.ID, payload)

	if got := payload["userMessage"]; got != "scan 10.0.0.1 please" {
		t.Fatalf("userMessage=%v", got)
	}
	if got := payload["thinking"]; got != "need port scan first" {
		t.Fatalf("thinking=%v", got)
	}
}

// TestEnrichHitlApprovalPayloadReasoning P1：任务记录 reasoning 意图后，
// HITL payload 应含 reasoning={mode,effort}，供审批页（非 chat 页）渲染「推理强度」。
func TestEnrichHitlApprovalPayloadReasoning(t *testing.T) {
	tmp := t.TempDir()
	db, err := database.NewDB(filepath.Join(tmp, "test.sqlite"), zap.NewNop())
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	defer func() {
		_ = db.Close()
		_ = os.RemoveAll(tmp)
	}()

	conv, err := db.CreateConversation("hitl reasoning", database.ConversationCreateMeta{})
	if err != nil {
		t.Fatalf("conv: %v", err)
	}
	asst, err := db.AddMessage(conv.ID, "assistant", "", nil)
	if err != nil {
		t.Fatalf("asst msg: %v", err)
	}

	h := &AgentHandler{db: db, tasks: NewAgentTaskManager()}
	cancel := func(error) {}
	if _, err := h.tasks.StartTask(conv.ID, "do scan", cancel); err != nil {
		t.Fatalf("start task: %v", err)
	}
	// 模拟 chat 请求携带 reasoning（sendMessage 时前端已发）。
	h.tasks.SetHitlReasoningIntent(conv.ID, "on", "high")

	payload := map[string]interface{}{"toolName": "exec", "arguments": "{}"}
	h.enrichHitlApprovalPayload(conv.ID, asst.ID, payload)

	r, ok := payload["reasoning"].(map[string]string)
	if !ok {
		t.Fatalf("payload[reasoning] missing or wrong type: %#v", payload["reasoning"])
	}
	if r["mode"] != "on" || r["effort"] != "high" {
		t.Fatalf("reasoning = %+v, want mode=on effort=high", r)
	}
}

// TestEnrichHitlApprovalPayloadNoReasoning P1 向后兼容：无 reasoning 意图时
// payload 不含 reasoning 字段（前端回退 buildReasoningRequestPayload 旧行为）。
func TestEnrichHitlApprovalPayloadNoReasoning(t *testing.T) {
	tmp := t.TempDir()
	db, err := database.NewDB(filepath.Join(tmp, "test.sqlite"), zap.NewNop())
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	defer func() {
		_ = db.Close()
		_ = os.RemoveAll(tmp)
	}()

	conv, err := db.CreateConversation("hitl no reasoning", database.ConversationCreateMeta{})
	if err != nil {
		t.Fatalf("conv: %v", err)
	}
	asst, err := db.AddMessage(conv.ID, "assistant", "", nil)
	if err != nil {
		t.Fatalf("asst msg: %v", err)
	}

	h := &AgentHandler{db: db, tasks: NewAgentTaskManager()}
	if _, err := h.tasks.StartTask(conv.ID, "do scan", func(error) {}); err != nil {
		t.Fatalf("start task: %v", err)
	}

	payload := map[string]interface{}{"toolName": "exec", "arguments": "{}"}
	h.enrichHitlApprovalPayload(conv.ID, asst.ID, payload)

	if _, exists := payload["reasoning"]; exists {
		t.Fatalf("payload[reasoning] should be absent without intent, got %#v", payload["reasoning"])
	}
}
