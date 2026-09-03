package swarm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestHome 创建临时 home 目录。
func newTestHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return dir
}

// TestMailboxWriteRead 验证原子写 + 读取升序 + unreadOnly。
func TestMailboxWriteRead(t *testing.T) {
	home := newTestHome(t)
	mb, err := NewMailbox(home, "team1", "agent1@team1")
	if err != nil {
		t.Fatalf("NewMailbox: %v", err)
	}
	ctx := context.Background()

	// 写 3 条消息，时间戳递增
	msgs := []MailboxMessage{
		NewUserMessage("leader@team1", "agent1@team1", "hello"),
		NewUserMessage("leader@team1", "agent1@team1", "world"),
		NewShutdownRequest("leader@team1", "agent1@team1"),
	}
	// 确保时间戳递增
	for i := range msgs {
		msgs[i].Timestamp = float64(time.Now().UnixNano()+int64(i))/1e9
		time.Sleep(time.Millisecond)
	}
	for _, m := range msgs {
		if err := mb.Write(ctx, m); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	// 读全部未读，按时间戳升序
	got, err := mb.ReadAll(ctx, true)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(got))
	}
	if got[0].Payload["content"] != "hello" {
		t.Errorf("first message should be hello, got %v", got[0].Payload["content"])
	}
	if got[2].Type != MsgShutdown {
		t.Errorf("last message should be shutdown, got %s", got[2].Type)
	}

	// MarkRead 第一条，再读 unreadOnly 应返回 2
	if err := mb.MarkRead(ctx, got[0].ID); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	got, _ = mb.ReadAll(ctx, true)
	if len(got) != 2 {
		t.Fatalf("after MarkRead, expected 2 unread, got %d", len(got))
	}

	// Clear
	if err := mb.Clear(ctx); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	got, _ = mb.ReadAll(ctx, false)
	if len(got) != 0 {
		t.Errorf("after Clear, expected 0, got %d", len(got))
	}
}

// TestMailboxDir 验证目录结构与权限。
func TestMailboxDir(t *testing.T) {
	home := newTestHome(t)
	mb, _ := NewMailbox(home, "team1", "agent1@team1")
	dir, err := mb.Dir()
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	expected := filepath.Join(home, "teams", "team1", "agents", "agent1@team1", "inbox")
	if dir != expected {
		t.Errorf("dir = %s, want %s", dir, expected)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat inbox: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("inbox is not a dir")
	}
}

// TestMailboxEmptyHome 验证空 homeDir 报错。
func TestMailboxEmptyHome(t *testing.T) {
	_, err := NewMailbox("", "team1", "agent1")
	if err == nil {
		t.Error("expected error for empty homeDir")
	}
	_, err = NewMailbox(home_TmpDir(t), "", "agent1")
	if err == nil {
		t.Error("expected error for empty teamName")
	}
}

// TestMailboxPathTraversal H1 修复验证：目录穿越被拒绝。
func TestMailboxPathTraversal(t *testing.T) {
	home := newTestHome(t)
	bad := []string{"../../../../escape", "..", ".", "a/b", "a\\b", "a:b", "a*b", "a|b", "a\x00b"}
	for _, s := range bad {
		// teamName 穿越
		mb, err := NewMailbox(home, s, "agent1")
		if err == nil {
			dir, derr := mb.Dir()
			if derr == nil && !strings.HasPrefix(dir, home) {
				t.Errorf("teamName %q escaped home: %s", s, dir)
			}
			t.Errorf("teamName %q should be rejected", s)
		}
		// agentID 穿越
		mb, err = NewMailbox(home, "team1", s)
		if err == nil {
			dir, derr := mb.Dir()
			if derr == nil && !strings.HasPrefix(dir, home) {
				t.Errorf("agentID %q escaped home: %s", s, dir)
			}
			t.Errorf("agentID %q should be rejected", s)
		}
	}
	// 合法 agentID（含 @ 和 . - _）应通过
	if _, err := NewMailbox(home, "team-1", "worker.name@team-1"); err != nil {
		t.Errorf("valid agentID should pass: %v", err)
	}
}

// home_TmpDir 复用 t.TempDir 但避免与 newTestHome 冲突命名。
func home_TmpDir(t *testing.T) string {
	return t.TempDir()
}

// TestMailboxPermissionMessages 验证 permission_request/response 工厂。
func TestMailboxPermissionMessages(t *testing.T) {
	home := newTestHome(t)
	mb, _ := NewMailbox(home, "team1", "worker@team1")
	ctx := context.Background()

	req := NewPermissionRequest("worker@team1", "leader@team1", map[string]interface{}{
		"request_id": "perm-1", "tool_name": "bash", "description": "rm -rf /tmp",
	})
	if req.Type != MsgPermissionRequest {
		t.Errorf("expected permission_request, got %s", req.Type)
	}
	if req.Payload["tool_name"] != "bash" {
		t.Errorf("expected tool_name=bash, got %v", req.Payload["tool_name"])
	}
	if err := mb.Write(ctx, req); err != nil {
		t.Fatalf("Write: %v", err)
	}

	resp := NewPermissionResponse("leader@team1", "worker@team1", map[string]interface{}{
		"request_id": "perm-1", "subtype": "success",
	})
	if resp.Type != MsgPermissionResponse {
		t.Errorf("expected permission_response, got %s", resp.Type)
	}
	if err := mb.Write(ctx, resp); err != nil {
		t.Fatalf("Write resp: %v", err)
	}

	got, _ := mb.ReadAll(ctx, false)
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got))
	}
}

// TestMailboxConcurrentWrite 验证并发写不冲突（lockfile 保护）。
func TestMailboxConcurrentWrite(t *testing.T) {
	home := newTestHome(t)
	mb, _ := NewMailbox(home, "team1", "agent1@team1")
	ctx := context.Background()

	done := make(chan error, 10)
	for i := 0; i < 10; i++ {
		go func(i int) {
			msg := NewUserMessage("leader@team1", "agent1@team1", "msg")
			msg.Timestamp = float64(time.Now().UnixNano()+int64(i))/1e9
			done <- mb.Write(ctx, msg)
		}(i)
	}
	for i := 0; i < 10; i++ {
		if err := <-done; err != nil {
			t.Errorf("concurrent write %d: %v", i, err)
		}
	}
	got, _ := mb.ReadAll(ctx, false)
	if len(got) != 10 {
		t.Errorf("expected 10 messages after concurrent write, got %d", len(got))
	}
}
