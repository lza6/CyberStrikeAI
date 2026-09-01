package blackboard

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

func newTestBoard(t *testing.T) *MemoryBoard {
	t.Helper()
	return NewMemoryBoard(zap.NewNop())
}

func TestPublishAndGetAndList(t *testing.T) {
	ctx := context.Background()
	b := newTestBoard(t)

	// Publish 三条，分属两个项目
	id1, err := b.Publish(ctx, Finding{
		Type: "vuln", Title: "SQL注入", Severity: "high", Source: "sqlmap", ProjectID: "proj-a",
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if id1 == "" {
		t.Fatal("Publish 返回空 ID")
	}

	_, err = b.Publish(ctx, Finding{
		Type: "asset", Title: "子域", Severity: "info", Source: "subfinder", ProjectID: "proj-b",
	})
	if err != nil {
		t.Fatalf("Publish 2: %v", err)
	}

	id3, err := b.Publish(ctx, Finding{
		Type: "vuln", Title: "XSS", Severity: "medium", Source: "xsser", ProjectID: "proj-a",
	})
	if err != nil {
		t.Fatalf("Publish 3: %v", err)
	}

	// Get 命中
	got, ok, err := b.Get(ctx, id1)
	if err != nil || !ok {
		t.Fatalf("Get id1: ok=%v err=%v", ok, err)
	}
	if got.Title != "SQL注入" {
		t.Errorf("Get 返回的 Title = %q, want SQL注入", got.Title)
	}

	// Get 不命中
	_, ok2, err := b.Get(ctx, "不存在的ID")
	if err != nil || ok2 {
		t.Fatalf("Get miss: ok=%v err=%v", ok2, err)
	}

	// List 全部
	all, err := b.List(ctx, "")
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("List all 长度 = %d, want 3", len(all))
	}
	// 验证顺序（升序）
	if all[0].ID != id1 || all[2].ID != id3 {
		t.Errorf("List all 顺序错误: got %s,%s,%s", all[0].ID, all[1].ID, all[2].ID)
	}

	// List 按项目过滤
	projA, _ := b.List(ctx, "proj-a")
	if len(projA) != 2 {
		t.Errorf("List proj-a 长度 = %d, want 2", len(projA))
	}
	projB, _ := b.List(ctx, "proj-b")
	if len(projB) != 1 {
		t.Errorf("List proj-b 长度 = %d, want 1", len(projB))
	}
}

func TestPublishAssignsIDAndTimestamp(t *testing.T) {
	ctx := context.Background()
	b := newTestBoard(t)

	// 留空 ID 与 CreatedAt，应自动填充
	id, err := b.Publish(ctx, Finding{Type: "vuln", Title: "test"})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if id == "" {
		t.Fatal("应自动生成 ID")
	}
	got, ok, _ := b.Get(ctx, id)
	if !ok {
		t.Fatal("Get miss")
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt 未自动填充")
	}
}

func TestSupersede(t *testing.T) {
	ctx := context.Background()
	b := newTestBoard(t)

	oldID, _ := b.Publish(ctx, Finding{Type: "vuln", Title: "疑似注入", Severity: "medium"})
	newID, err := b.Supersede(ctx, oldID, Finding{
		Type: "vuln", Title: "确认 SQL注入", Severity: "high", Source: "manual",
	})
	if err != nil {
		t.Fatalf("Supersede: %v", err)
	}
	if newID == "" {
		t.Fatal("newID 为空")
	}
	if newID == oldID {
		t.Fatal("newID 不应等于 oldID")
	}

	// old 应被标记 SupersededBy = newID
	old, ok, _ := b.Get(ctx, oldID)
	if !ok {
		t.Fatal("old 不存在")
	}
	if old.SupersededBy != newID {
		t.Errorf("old.SupersededBy = %q, want %q", old.SupersededBy, newID)
	}

	// new 应存在
	_, ok2, _ := b.Get(ctx, newID)
	if !ok2 {
		t.Fatal("new finding 不存在")
	}

	// supersede 不存在的 old 应报错
	_, err = b.Supersede(ctx, "不存在", Finding{Type: "vuln", Title: "x"})
	if !errors.Is(err, ErrOldNotFound) {
		t.Errorf("Supersede 不存在的 old 应返回 ErrOldNotFound, got %v", err)
	}

	// supersede 空 old 应报错
	_, err = b.Supersede(ctx, "", Finding{Type: "vuln", Title: "x"})
	if !errors.Is(err, ErrEmptyOldID) {
		t.Errorf("Supersede 空 old 应返回 ErrEmptyOldID, got %v", err)
	}
}

func TestSubscribeCursorExactlyOnce(t *testing.T) {
	ctx := context.Background()
	b := newTestBoard(t)

	// 先发 2 条
	b.Publish(ctx, Finding{Type: "vuln", Title: "v1"})
	b.Publish(ctx, Finding{Type: "vuln", Title: "v2"})

	// cursor=0 订阅，应先收到已存在的 2 条
	subCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := b.Subscribe(subCtx, 0)

	// 收已存在
	got1 := <-ch
	got2 := <-ch
	if got1.Title != "v1" || got2.Title != "v2" {
		t.Errorf("已存在 finding 顺序错误: got %s, %s", got1.Title, got2.Title)
	}

	// 再发一条，应实时收到
	b.Publish(ctx, Finding{Type: "vuln", Title: "v3"})
	got3 := <-ch
	if got3.Title != "v3" {
		t.Errorf("实时 finding 错误: got %s", got3.Title)
	}

	// 用 cursor=2（已收到 v1,v2）再订阅一次，应只收到 v3（重复订阅同一 cursor 语义）
	ch2Ctx, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	ch2 := b.Subscribe(ch2Ctx, 2)
	got3again := <-ch2
	if got3again.Title != "v3" {
		t.Errorf("cursor=2 订阅应收到 v3, got %s", got3again.Title)
	}
}

func TestSubscribeCtxCancelClosesChannel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	b := newTestBoard(t)
	ch := b.Subscribe(ctx, 0)

	cancel()
	// channel 应被关闭，读应 ok=false
	_, ok := <-ch
	if ok {
		t.Error("ctx 取消后 channel 未关闭")
	}
}

func TestConcurrentPublishRace(t *testing.T) {
	// 并发 Publish 验证无 race、无 panic、计数正确。
	ctx := context.Background()
	b := newTestBoard(t)

	var wg sync.WaitGroup
	const goroutines = 20
	const perG = 50
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				_, _ = b.Publish(ctx, Finding{
					Type:    "vuln",
					Title:   "concurrent",
					Source:  "tester",
					ProjectID: "proj-x",
				})
			}
		}(g)
	}
	wg.Wait()

	total := goroutines * perG
	if got := b.Len(); got != total {
		t.Errorf("并发 Publish 后 Len = %d, want %d", got, total)
	}

	all, _ := b.List(ctx, "proj-x")
	if len(all) != total {
		t.Errorf("List proj-x 长度 = %d, want %d", len(all), total)
	}
}

func TestSubscribeBackpressure(t *testing.T) {
	// 订阅者不读，channel 满后 Publish 不应阻塞；丢弃旧 finding 仍保 at-least-once 语义。
	ctx := context.Background()
	b := newTestBoard(t)

	subCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = b.Subscribe(subCtx, 0) // 订阅者从不读

	// 发送远超 buffer 的 finding，应全部返回且不阻塞
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			_, _ = b.Publish(ctx, Finding{Type: "vuln", Title: "flood"})
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish 在 channel 满时阻塞超时")
	}

	// 全量应落盘内存
	if b.Len() != 1000 {
		t.Errorf("Len = %d, want 1000", b.Len())
	}
}
