package securityevents

import (
	"context"
	"sync"
	"testing"
	"time"

	"cyberstrike-ai/internal/blackboard"

	"go.uber.org/zap"
)

// TestSetBoardNoopWhenNil board 未注入时所有 Publish* 不 panic。
func TestSetBoardNoopWhenNil(t *testing.T) {
	SetBoard(nil) // 显式禁用
	PublishHighImpactTool("nmap", "high", "conv-1")
	PublishScopeViolation("proj-1", "nmap", "out of scope")
	PublishCapabilityRollback("modify-file", "err")
}

// TestPublishHighImpactToolE2E H1 复验：注入 board 后 PublishHighImpactTool
// 真实广播一条 high-impact-tool finding，reactions 引擎可订阅消费。
func TestPublishHighImpactToolE2E(t *testing.T) {
	logger := zap.NewNop()
	b := blackboard.NewMemoryBoard(logger)
	SetBoard(b)
	t.Cleanup(func() { SetBoard(nil) })

	// 订阅（reactions 引擎同款消费方式）。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := b.Subscribe(ctx, 0)

	PublishHighImpactTool("nmap", "high", "conv-42")

	select {
	case f := <-ch:
		if f.Type != "high-impact-tool" {
			t.Fatalf("Type = %q, want high-impact-tool", f.Type)
		}
		if f.Severity != "high" || f.Source != "executor" {
			t.Fatalf("finding mismatch: %+v", f)
		}
		if f.Detail != "conversationId=conv-42" {
			t.Fatalf("Detail = %q, want conversationId=conv-42", f.Detail)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for high-impact-tool finding")
	}
}

// TestPublishScopeViolationE2E H1 复验：scope-violation 事件广播带 projectID。
func TestPublishScopeViolationE2E(t *testing.T) {
	logger := zap.NewNop()
	b := blackboard.NewMemoryBoard(logger)
	SetBoard(b)
	t.Cleanup(func() { SetBoard(nil) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := b.Subscribe(ctx, 0)

	PublishScopeViolation("proj-9", "sqlmap", "10.0.0.99 越界")

	select {
	case f := <-ch:
		if f.Type != "scope-violation" {
			t.Fatalf("Type = %q, want scope-violation", f.Type)
		}
		if f.ProjectID != "proj-9" {
			t.Fatalf("ProjectID = %q, want proj-9", f.ProjectID)
		}
		if f.Source != "scope_block" {
			t.Fatalf("Source = %q, want scope_block", f.Source)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for scope-violation finding")
	}
}

// TestPublishCapabilityRollbackE2E H1 复验：capability-rollback 事件广播。
func TestPublishCapabilityRollbackE2E(t *testing.T) {
	logger := zap.NewNop()
	b := blackboard.NewMemoryBoard(logger)
	SetBoard(b)
	t.Cleanup(func() { SetBoard(nil) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := b.Subscribe(ctx, 0)

	PublishCapabilityRollback("modify-file", "write failed: disk full")

	select {
	case f := <-ch:
		if f.Type != "capability-rollback" {
			t.Fatalf("Type = %q, want capability-rollback", f.Type)
		}
		if f.Severity != "warning" || f.Source != "capability" {
			t.Fatalf("finding mismatch: %+v", f)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for capability-rollback finding")
	}
}

// TestConcurrentPublish H1 复验：并发发布不 panic（board 内部有锁；本包只透传）。
func TestConcurrentPublish(t *testing.T) {
	logger := zap.NewNop()
	b := blackboard.NewMemoryBoard(logger)
	SetBoard(b)
	t.Cleanup(func() { SetBoard(nil) })

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			PublishHighImpactTool("tool", "risk", "conv")
			PublishScopeViolation("p", "t", "r")
		}(i)
	}
	wg.Wait()
}
