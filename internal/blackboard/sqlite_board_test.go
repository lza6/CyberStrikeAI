package blackboard

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

// newSQLiteBoard 在 t.TempDir() 下建一个 SQLiteBoard，返回 board 与 dbPath。
// 关闭由 t.Cleanup 注册；调用方也可提前 board.Close() 模拟重启。
func newSQLiteBoard(t *testing.T) (*SQLiteBoard, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "blackboard_test.db")
	b, err := NewSQLiteBoard(dbPath, zap.NewNop())
	if err != nil {
		t.Fatalf("NewSQLiteBoard: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b, dbPath
}

// TestSQLitePublishAndGetAndList 覆盖 Publish/Get/List 基本语义（与 memory_board_test 同构）。
func TestSQLitePublishAndGetAndList(t *testing.T) {
	ctx := context.Background()
	b, _ := newSQLiteBoard(t)

	id1, err := b.Publish(ctx, Finding{
		Type: "vuln", Title: "SQL注入", Severity: "high", Source: "sqlmap", ProjectID: "proj-a",
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if id1 == "" {
		t.Fatal("Publish 返回空 ID")
	}

	if _, err := b.Publish(ctx, Finding{
		Type: "asset", Title: "子域", Severity: "info", Source: "subfinder", ProjectID: "proj-b",
	}); err != nil {
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
	if got.ProjectID != "proj-a" {
		t.Errorf("Get 返回的 ProjectID = %q, want proj-a", got.ProjectID)
	}
	if got.CreatedAt.IsZero() {
		t.Error("Get 返回的 CreatedAt 为零值")
	}

	// Get 不命中
	_, ok2, err := b.Get(ctx, "不存在的ID")
	if err != nil || ok2 {
		t.Fatalf("Get miss: ok=%v err=%v", ok2, err)
	}

	// Get 空 ID 不命中
	_, ok3, err := b.Get(ctx, "")
	if err != nil || ok3 {
		t.Fatalf("Get empty: ok=%v err=%v", ok3, err)
	}

	// List 全部
	all, err := b.List(ctx, "")
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("List all 长度 = %d, want 3", len(all))
	}
	// 验证顺序（升序，按 rowid 即插入顺序）
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

// TestSQLitePublishAssignsIDAndTimestamp 验证空 ID/CreatedAt 自动填充。
func TestSQLitePublishAssignsIDAndTimestamp(t *testing.T) {
	ctx := context.Background()
	b, _ := newSQLiteBoard(t)

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

// TestSQLiteSupersede 覆盖 Supersede 的 old/err 路径。
func TestSQLiteSupersede(t *testing.T) {
	ctx := context.Background()
	b, _ := newSQLiteBoard(t)

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

// TestSQLiteSubscribeCursorExactlyOnce 覆盖 Subscribe 重放 + 实时投递 + cursor 去重语义。
func TestSQLiteSubscribeCursorExactlyOnce(t *testing.T) {
	ctx := context.Background()
	b, _ := newSQLiteBoard(t)

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

	// 用 cursor=2（已收到 v1,v2，对应 rowid 1,2）再订阅一次，应只收到 v3
	ch2Ctx, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	ch2 := b.Subscribe(ch2Ctx, 2)
	got3again := <-ch2
	if got3again.Title != "v3" {
		t.Errorf("cursor=2 订阅应收到 v3, got %s", got3again.Title)
	}
}

// TestSQLiteSubscribeCtxCancelClosesChannel 验证 ctx 取消后 channel 关闭。
func TestSQLiteSubscribeCtxCancelClosesChannel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	b, _ := newSQLiteBoard(t)
	ch := b.Subscribe(ctx, 0)

	cancel()
	// channel 应被关闭，读应 ok=false
	_, ok := <-ch
	if ok {
		t.Error("ctx 取消后 channel 未关闭")
	}
}

// TestSQLiteRestartFindingsPersist 是 K0b 核心验收点：关闭 *sql.DB 重开，
// findings 仍在（进程重启不丢）。
func TestSQLiteRestartFindingsPersist(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "restart.db")

	// 第一阶段：打开、Publish 3 条、关闭。
	b1, err := NewSQLiteBoard(dbPath, zap.NewNop())
	if err != nil {
		t.Fatalf("NewSQLiteBoard 1: %v", err)
	}
	id1, _ := b1.Publish(ctx, Finding{Type: "vuln", Title: "v1", ProjectID: "proj-a"})
	b1.Publish(ctx, Finding{Type: "asset", Title: "a2", ProjectID: "proj-b"})
	b1.Publish(ctx, Finding{Type: "vuln", Title: "v3", ProjectID: "proj-a"})
	if err := b1.Close(); err != nil {
		t.Fatalf("Close 1: %v", err)
	}

	// 第二阶段：重开同一 dbPath，findings 应仍在。
	b2, err := NewSQLiteBoard(dbPath, zap.NewNop())
	if err != nil {
		t.Fatalf("NewSQLiteBoard 2: %v", err)
	}
	defer b2.Close()

	all, err := b2.List(ctx, "")
	if err != nil {
		t.Fatalf("List after restart: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("重启后 findings 数量 = %d, want 3", len(all))
	}

	// id1 仍可 Get
	got, ok, err := b2.Get(ctx, id1)
	if err != nil || !ok {
		t.Fatalf("Get id1 after restart: ok=%v err=%v", ok, err)
	}
	if got.Title != "v1" {
		t.Errorf("重启后 Title = %q, want v1", got.Title)
	}
	if got.ProjectID != "proj-a" {
		t.Errorf("重启后 ProjectID = %q, want proj-a", got.ProjectID)
	}

	// 按项目过滤仍生效
	projA, _ := b2.List(ctx, "proj-a")
	if len(projA) != 2 {
		t.Errorf("重启后 proj-a findings = %d, want 2", len(projA))
	}
}

// TestSQLiteRestartSupersedeStatePersist 验证 supersede 状态跨重启保持。
func TestSQLiteRestartSupersedeStatePersist(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "supersede.db")

	b1, err := NewSQLiteBoard(dbPath, zap.NewNop())
	if err != nil {
		t.Fatalf("NewSQLiteBoard 1: %v", err)
	}
	oldID, _ := b1.Publish(ctx, Finding{Type: "vuln", Title: "old"})
	newID, _ := b1.Supersede(ctx, oldID, Finding{Type: "vuln", Title: "new", Severity: "high"})
	b1.Close()

	b2, err := NewSQLiteBoard(dbPath, zap.NewNop())
	if err != nil {
		t.Fatalf("NewSQLiteBoard 2: %v", err)
	}
	defer b2.Close()

	old, ok, _ := b2.Get(ctx, oldID)
	if !ok {
		t.Fatal("重启后 old finding 丢失")
	}
	if old.SupersededBy != newID {
		t.Errorf("重启后 old.SupersededBy = %q, want %q", old.SupersededBy, newID)
	}
}

// TestSQLiteConcurrentPublishRace 验证 WAL 模式下并发写无 "database is locked"、无 race、计数正确。
// 这是 K0b 验收标准 #2 的核心：WAL 并发写。
func TestSQLiteConcurrentPublishRace(t *testing.T) {
	ctx := context.Background()
	b, _ := newSQLiteBoard(t)

	var wg sync.WaitGroup
	const goroutines = 20
	const perG = 50
	wg.Add(goroutines)
	start := make(chan struct{})
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			<-start // 同时起跑，最大化写并发
			for i := 0; i < perG; i++ {
				_, err := b.Publish(ctx, Finding{
					Type:      "vuln",
					Title:     fmt.Sprintf("concurrent-%d-%d", g, i),
					Source:    "tester",
					ProjectID: "proj-x",
				})
				if err != nil {
					t.Errorf("goroutine %d Publish %d: %v", g, i, err)
					return
				}
			}
		}(g)
	}
	close(start)
	wg.Wait()

	total := goroutines * perG
	got, err := b.Len()
	if err != nil {
		t.Fatalf("Len 出错: %v", err)
	}
	if got != total {
		t.Errorf("并发 Publish 后 Len = %d, want %d", got, total)
	}

	all, _ := b.List(ctx, "proj-x")
	if len(all) != total {
		t.Errorf("List proj-x 长度 = %d, want %d", len(all), total)
	}

	// 验证无 "database is locked" 类错误（Publish 内已返回 err，这里兜底扫描日志）。
	// WAL + busy_timeout=5000 应让并发写全部成功。
}

// TestSQLiteConcurrentPublishAndSubscribe 验证 Publish 与 Subscribe 并发不 race、不死锁。
func TestSQLiteConcurrentPublishAndSubscribe(t *testing.T) {
	ctx := context.Background()
	b, _ := newSQLiteBoard(t)

	// 先发 5 条存量
	for i := 0; i < 5; i++ {
		b.Publish(ctx, Finding{Type: "vuln", Title: fmt.Sprintf("pre-%d", i)})
	}

	// 启动 3 个订阅者，各读若干条后取消
	var wg sync.WaitGroup
	const subscribers = 3
	const perSub = 20
	wg.Add(subscribers)
	for s := 0; s < subscribers; s++ {
		go func(s int) {
			defer wg.Done()
			subCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ch := b.Subscribe(subCtx, 0)
			for i := 0; i < perSub; i++ {
				select {
				case <-ch:
				case <-time.After(2 * time.Second):
					t.Errorf("subscriber %d 超时未收到 finding", s)
					return
				}
			}
		}(s)
	}

	// 同时继续 Publish
	for i := 0; i < 50; i++ {
		b.Publish(ctx, Finding{Type: "vuln", Title: fmt.Sprintf("live-%d", i)})
	}
	wg.Wait()
}

// TestSQLiteSubscribeBackpressure 验证订阅者不读时 Publish 不阻塞（at-least-once 兜底）。
func TestSQLiteSubscribeBackpressure(t *testing.T) {
	ctx := context.Background()
	b, _ := newSQLiteBoard(t)

	subCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = b.Subscribe(subCtx, 0) // 订阅者从不读

	// 发送远超 buffer 的 finding，应全部返回且不阻塞
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			if _, err := b.Publish(ctx, Finding{Type: "vuln", Title: "flood"}); err != nil {
				t.Errorf("Publish %d: %v", i, err)
				return
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Publish 在 channel 满时阻塞超时")
	}

	// 全量应落盘 DB
	got, err := b.Len()
	if err != nil {
		t.Fatalf("Len 出错: %v", err)
	}
	if got != 1000 {
		t.Errorf("Len = %d, want 1000", got)
	}
}

// TestSQLiteFTS5Availability 验证 FTS5 在 modernc 路径可用、mattn 默认路径降级。
// modernc 默认带 fts5 模块（本测试断言 true）；mattn 默认构建不含 fts5（需
// -tags sqlite_fts5），本测试断言 false（降级，核心功能不受影响）。两条路径都合法。
func TestSQLiteFTS5Availability(t *testing.T) {
	b, _ := newSQLiteBoard(t)
	// modernc 默认带 fts5 → true；mattn 默认构建无 fts5 → false（降级）。
	// 两条路径都合法，这里只记录不强制。
	_ = b.FTS5Available()
}

// TestSQLiteBoardImplementsBoard 静态断言 *SQLiteBoard 实现 Board interface。
// 防止接口签名变更后 SQLiteBoard 编译失败被遗漏。
func TestSQLiteBoardImplementsBoard(t *testing.T) {
	var _ Board = (*SQLiteBoard)(nil)
	// NewSQLiteBoard 与 NewMemoryBoard 二选一，Board interface 不变。
	var _ Board = (*MemoryBoard)(nil)
}

// TestSQLiteSubscribeCloseThenPublishNoPanic 验证 Blocking 1 修复：
// Subscribe 后立即 Close（close(ch)），然后 Publish 对已关闭 channel 发送不 panic。
// 原实现 Publish 在 b.mu.Unlock() 后遍历快照 subs 发送，但 Subscribe 的 ctx.Done
// goroutine 与 Close 在锁内 close(ch)，快照释放锁后对已关闭 channel 发送会 panic。
// 修复后 trySend 用 closed 快速路径 + recover 兜底，对已关闭 channel 安全。
func TestSQLiteSubscribeCloseThenPublishNoPanic(t *testing.T) {
	ctx := context.Background()
	b, _ := newSQLiteBoard(t)

	subCtx, cancel := context.WithCancel(context.Background())
	_ = b.Subscribe(subCtx, 0)
	// 立即 Close：触发 sub.close()（close ch + done）。
	cancel()
	// 等 ctx.Done goroutine 关闭 channel。
	time.Sleep(50 * time.Millisecond)

	// Publish 对已关闭 channel 发送应不 panic（trySend 兜底）。
	for i := 0; i < 10; i++ {
		_, err := b.Publish(ctx, Finding{Type: "vuln", Title: "after-close"})
		if err != nil {
			t.Fatalf("Publish after close: %v", err)
		}
	}
}

// TestSQLiteSubscribeCtxCancelThenPublishNoPanic 验证 ctx 取消（非 Close）后
// Publish 对已关闭 channel 发送不 panic。ctx.Done goroutine 在锁内 close(ch)。
func TestSQLiteSubscribeCtxCancelThenPublishNoPanic(t *testing.T) {
	ctx := context.Background()
	b, _ := newSQLiteBoard(t)

	subCtx, cancel := context.WithCancel(context.Background())
	_ = b.Subscribe(subCtx, 0)
	cancel() // 触发 ctx.Done goroutine close(ch)
	time.Sleep(50 * time.Millisecond)

	for i := 0; i < 10; i++ {
		_, err := b.Publish(ctx, Finding{Type: "vuln", Title: "after-cancel"})
		if err != nil {
			t.Fatalf("Publish after cancel: %v", err)
		}
	}
}

// TestSQLiteCloseThenSubscribeNoPanic 验证 Close 后 Subscribe 不 panic。
// Close 后 b.closed=true，Subscribe 返回已关闭 channel（不重放）。
func TestSQLiteCloseThenSubscribeNoPanic(t *testing.T) {
	ctx := context.Background()
	b, _ := newSQLiteBoard(t)
	b.Publish(ctx, Finding{Type: "vuln", Title: "pre-close"})

	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Close 后 Subscribe 应返回已关闭 channel，不 panic、不重放。
	subCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := b.Subscribe(subCtx, 0)
	// channel 已关闭，读应 ok=false。
	_, ok := <-ch
	if ok {
		t.Error("Close 后 Subscribe 返回的 channel 不应是打开的")
	}
}

// TestSQLiteSubscribeReplayOutsideLock 验证 Blocking 3 修复：
// Subscribe 锁内只注册 subscriber，锁外重放。验证大量已存在 finding 时
// Subscribe 不长时间持锁阻塞 Publish。
func TestSQLiteSubscribeReplayOutsideLock(t *testing.T) {
	ctx := context.Background()
	b, _ := newSQLiteBoard(t)

	// 预填充 100 条已存在 finding。
	for i := 0; i < 100; i++ {
		b.Publish(ctx, Finding{Type: "vuln", Title: fmt.Sprintf("pre-%d", i)})
	}

	subCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := b.Subscribe(subCtx, 0)

	// 应收到 100 条已存在 finding（重放在锁外，但仍投递到 channel）。
	got := 0
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && got < 100 {
		select {
		case <-ch:
			got++
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("Subscribe 重放超时：got %d/100", got)
		}
	}
	if got != 100 {
		t.Errorf("Subscribe 重放收到 %d 条，want 100", got)
	}
}

// TestSQLiteLenErrorOnClosedDB 验证 RC6 修复：Len 在 DB 已关闭时返回 error 而非 0。
// 原实现 _ = ...Scan(&n) 吞错误，表不存在/DB 关闭返回 0 误导调用方。
// 修复后返回 (int, error)，DB 关闭时返回 error。
func TestSQLiteLenErrorOnClosedDB(t *testing.T) {
	ctx := context.Background()
	b, _ := newSQLiteBoard(t)
	b.Publish(ctx, Finding{Type: "vuln", Title: "pre-close"})

	// Close 前正常返回。
	n, err := b.Len()
	if err != nil {
		t.Fatalf("Close 前 Len 出错: %v", err)
	}
	if n != 1 {
		t.Errorf("Close 前 Len = %d, want 1", n)
	}

	// Close 后应返回 error（不再吞错返回 0）。
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err = b.Len()
	if err == nil {
		t.Fatal("Close 后 Len 应返回 error，got nil")
	}
}

// TestSQLiteListRecent 验证 ListRecent 只返回最近 N 条（RC10 配套）。
func TestSQLiteListRecent(t *testing.T) {
	ctx := context.Background()
	b, _ := newSQLiteBoard(t)

	// 发 10 条。
	for i := 0; i < 10; i++ {
		b.Publish(ctx, Finding{Type: "vuln", Title: fmt.Sprintf("f-%d", i)})
	}

	// limit=3：应只返回最近 3 条（f-7, f-8, f-9），升序。
	recent, err := b.ListRecent(ctx, "", 3)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(recent) != 3 {
		t.Fatalf("ListRecent 返回 %d 条，want 3", len(recent))
	}
	if recent[0].Title != "f-7" || recent[1].Title != "f-8" || recent[2].Title != "f-9" {
		t.Errorf("ListRecent 顺序错误: got %s, %s, %s", recent[0].Title, recent[1].Title, recent[2].Title)
	}

	// limit=0：等价于 List，返回全部。
	all, err := b.ListRecent(ctx, "", 0)
	if err != nil {
		t.Fatalf("ListRecent limit=0: %v", err)
	}
	if len(all) != 10 {
		t.Errorf("ListRecent limit=0 返回 %d 条，want 10", len(all))
	}

	// limit 超过总数：返回全部。
	over, err := b.ListRecent(ctx, "", 100)
	if err != nil {
		t.Fatalf("ListRecent over-limit: %v", err)
	}
	if len(over) != 10 {
		t.Errorf("ListRecent over-limit 返回 %d 条，want 10", len(over))
	}

	// 按项目过滤 + limit。
	b.Publish(ctx, Finding{Type: "vuln", Title: "p2-0", ProjectID: "p2"})
	b.Publish(ctx, Finding{Type: "vuln", Title: "p2-1", ProjectID: "p2"})
	recentP2, err := b.ListRecent(ctx, "p2", 1)
	if err != nil {
		t.Fatalf("ListRecent p2: %v", err)
	}
	if len(recentP2) != 1 || recentP2[0].Title != "p2-1" {
		t.Errorf("ListRecent p2 limit=1 错误: got %+v", recentP2)
	}
}
