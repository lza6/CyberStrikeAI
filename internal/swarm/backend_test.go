package swarm

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestRegistryRegisterGet 验证 Register/Get。
func TestRegistryRegisterGet(t *testing.T) {
	r := GetRegistry()
	r.Reset()
	defer r.Reset()
	home := t.TempDir()
	inProc, err := NewInProcessBackend(home, func(ctx context.Context, cfg SpawnConfig, msgs <-chan Message) (string, error) {
		return "done", nil
	})
	if err != nil {
		t.Fatalf("NewInProcessBackend: %v", err)
	}
	r.Register(inProc)
	b, ok := r.Get(BackendInProcess)
	if !ok {
		t.Error("expected to get in_process backend")
	}
	if b.Type() != BackendInProcess {
		t.Errorf("type = %s", b.Type())
	}
}

// TestRegistryDetectFallback 验证 fallback 标记优先选 in_process。
func TestRegistryDetectFallback(t *testing.T) {
	r := GetRegistry()
	r.Reset()
	defer r.Reset()
	home := t.TempDir()
	inProc, _ := NewInProcessBackend(home, func(ctx context.Context, cfg SpawnConfig, msgs <-chan Message) (string, error) {
		return "done", nil
	})
	sub, _ := NewSubprocessBackend(home, SubprocessExec{Path: "echo"})
	r.Register(inProc)
	r.Register(sub)
	ctx := context.Background()

	// 无 fallback、无偏好 → 选 subprocess（兜底）
	b, err := r.Detect(ctx)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if b.Type() != BackendSubprocess {
		t.Errorf("expected subprocess, got %s", b.Type())
	}

	// 设置偏好 in_process → 选 in_process
	r.SetPreferred("in_process")
	b, _ = r.Detect(ctx)
	if b.Type() != BackendInProcess {
		t.Errorf("expected in_process via preference, got %s", b.Type())
	}

	// 清偏好，置 fallback → 选 in_process
	r.SetPreferred("")
	r.MarkInProcessFallback()
	b, _ = r.Detect(ctx)
	if b.Type() != BackendInProcess {
		t.Errorf("expected in_process via fallback, got %s", b.Type())
	}
}

// TestRegistryDetectNoBackend 验证无后端报错。
func TestRegistryDetectNoBackend(t *testing.T) {
	r := GetRegistry()
	r.Reset()
	defer r.Reset()
	_, err := r.Detect(context.Background())
	if err == nil {
		t.Error("expected error when no backend registered")
	}
}

// TestRegistryHealthCheck 验证健康检查。
func TestRegistryHealthCheck(t *testing.T) {
	r := GetRegistry()
	r.Reset()
	defer r.Reset()
	home := t.TempDir()
	inProc, _ := NewInProcessBackend(home, func(ctx context.Context, cfg SpawnConfig, msgs <-chan Message) (string, error) {
		return "done", nil
	})
	r.Register(inProc)
	hc := r.HealthCheck(context.Background())
	if !hc[BackendInProcess] {
		t.Error("expected in_process to be available")
	}
}

// TestInProcessBackendSpawnSendShutdown 验证完整生命周期。
func TestInProcessBackendSpawnSendShutdown(t *testing.T) {
	home := t.TempDir()
	var runMu sync.Mutex
	received := []string{}
	run := func(ctx context.Context, cfg SpawnConfig, msgs <-chan Message) (string, error) {
		for {
			select {
			case <-ctx.Done():
				return "shutdown", ctx.Err()
			case m, ok := <-msgs:
				if !ok {
					return "done", nil
				}
				runMu.Lock()
				received = append(received, m.Text)
				runMu.Unlock()
			}
		}
	}
	b, err := NewInProcessBackend(home, run)
	if err != nil {
		t.Fatalf("NewInProcessBackend: %v", err)
	}
	ctx := context.Background()
	if !b.IsAvailable(ctx) {
		t.Error("expected in_process to be available")
	}
	res, err := b.Spawn(ctx, SpawnConfig{Name: "worker", Team: "team1"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if !res.Success {
		t.Fatalf("Spawn failed: %s", res.Error)
	}
	if res.AgentID != "worker@team1" {
		t.Errorf("agentID = %s", res.AgentID)
	}
	// 重复 spawn 同 agent 应失败
	res2, _ := b.Spawn(ctx, SpawnConfig{Name: "worker", Team: "team1"})
	if res2.Success {
		t.Error("expected duplicate spawn to fail")
	}

	// 发消息
	if err := b.SendMessage(ctx, res.AgentID, Message{Text: "hello", FromAgent: "leader@team1"}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	// 等待 goroutine 处理
	time.Sleep(100 * time.Millisecond)
	runMu.Lock()
	got := len(received)
	runMu.Unlock()
	if got < 1 {
		t.Errorf("expected at least 1 received message, got %d", got)
	}

	// 优雅 shutdown
	ok, err := b.Shutdown(ctx, res.AgentID, false)
	if err != nil || !ok {
		t.Fatalf("Shutdown: ok=%v err=%v", ok, err)
	}
	// 再 shutdown 应报 not found
	_, err = b.Shutdown(ctx, res.AgentID, false)
	if !errors.Is(err, ErrAgentNotFound) {
		t.Errorf("expected ErrAgentNotFound, got %v", err)
	}
}

// TestInProcessBackendDrainMailbox 验证 DrainMailbox 把文件消息注入 channel。
func TestInProcessBackendDrainMailbox(t *testing.T) {
	home := t.TempDir()
	var runMu sync.Mutex
	received := []string{}
	run := func(ctx context.Context, cfg SpawnConfig, msgs <-chan Message) (string, error) {
		for {
			select {
			case <-ctx.Done():
				return "shutdown", ctx.Err()
			case m, ok := <-msgs:
				if !ok {
					return "done", nil
				}
				runMu.Lock()
				received = append(received, m.Text)
				runMu.Unlock()
			}
		}
	}
	b, _ := NewInProcessBackend(home, run)
	ctx := context.Background()
	res, _ := b.Spawn(ctx, SpawnConfig{Name: "w", Team: "t"})

	// 直接写文件 mailbox（绕过 SendMessage 的内存旁路），再 Drain
	mb, _ := NewMailbox(home, "t", res.AgentID)
	_ = mb.Write(ctx, NewUserMessage("leader@t", res.AgentID, "from-file"))
	time.Sleep(50 * time.Millisecond)
	// Drain 把文件消息注入 channel
	if err := b.DrainMailbox(ctx, res.AgentID); err != nil {
		t.Fatalf("DrainMailbox: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	runMu.Lock()
	defer runMu.Unlock()
	if len(received) < 1 || !strings.Contains(received[0], "from-file") {
		t.Errorf("expected from-file drained, got %v", received)
	}
	_, _ = b.Shutdown(ctx, res.AgentID, true)
}

// TestSubprocessBackendSpawn 验证子进程 spawn（用 echo 或 cmd 兜底）。
func TestSubprocessBackendSpawn(t *testing.T) {
	home := t.TempDir()
	// 用一个无害命令：sleep/echo。跨平台用 "ping" 或 "timeout" 不稳，这里用 Go 自身
	b, err := NewSubprocessBackend(home, SubprocessExec{Path: "go", Args: []string{"version"}})
	if err != nil {
		t.Fatalf("NewSubprocessBackend: %v", err)
	}
	ctx := context.Background()
	if !b.IsAvailable(ctx) {
		t.Error("expected subprocess to be available")
	}
	res, err := b.Spawn(ctx, SpawnConfig{Name: "w", Team: "t"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if !res.Success {
		t.Fatalf("Spawn failed: %s", res.Error)
	}
	// Shutdown（force）
	ok, err := b.Shutdown(ctx, res.AgentID, true)
	if err != nil || !ok {
		t.Fatalf("Shutdown: ok=%v err=%v", ok, err)
	}
}

// TestSubprocessBackendEmptyPath 验证空 path 报错。
func TestSubprocessBackendEmptyPath(t *testing.T) {
	_, err := NewSubprocessBackend(t.TempDir(), SubprocessExec{})
	if err == nil {
		t.Error("expected error for empty path")
	}
}

// TestSubprocessBackendSendMessageNotFound 验证未知 agent 报错。
func TestSubprocessBackendSendMessageNotFound(t *testing.T) {
	b, _ := NewSubprocessBackend(t.TempDir(), SubprocessExec{Path: "echo"})
	err := b.SendMessage(context.Background(), "unknown@t", Message{Text: "x"})
	if !errors.Is(err, ErrAgentNotFound) {
		t.Errorf("expected ErrAgentNotFound, got %v", err)
	}
}
