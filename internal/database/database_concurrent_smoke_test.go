package database

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

// newTestDB 构造临时 SQLite（WAL + busy_timeout 已在 NewDB 内配置），返回 db + cleanup。
func newTestDB(t *testing.T) (*DB, func(), error) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "concurrent-smoke.db")
	db, err := NewDB(dbPath, zap.NewNop())
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = db.Close() }
	return db, cleanup, nil
}

// TestConcurrentWritesNoDatabaseLocked 并发写不出现 "database is locked"（WAL + busy_timeout 已配置）。
// 模拟多实例/多 goroutine 并发对话场景：N 个写者并发 INSERT messages + UpdateConversationTime。
// 验收标准（P3）：并发对话冒烟无 `database is locked`。
func TestConcurrentWritesNoDatabaseLocked(t *testing.T) {
	db, cleanup, err := newTestDB(t)
	if err != nil {
		t.Fatalf("创建测试 DB 失败: %v", err)
	}
	defer cleanup()

	// 先建一个会话
	conv, err := db.CreateConversation("concurrency-smoke", ConversationCreateMeta{})
	if err != nil {
		t.Fatalf("CreateConversation 失败: %v", err)
	}

	const writers = 20
	const perWriter = 25
	var wg sync.WaitGroup
	errCh := make(chan error, writers*perWriter)
	wg.Add(writers)

	start := time.Now()
	for w := 0; w < writers; w++ {
		go func(writerID int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				_, err := db.AddMessage(conv.ID, "assistant", fmt.Sprintf("w%d-i%d", writerID, i), nil)
				if err != nil {
					errCh <- fmt.Errorf("writer %d msg %d: %w", writerID, i, err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errCh)
	elapsed := time.Since(start)

	var firstErr error
	for e := range errCh {
		if firstErr == nil {
			firstErr = e
		}
	}
	if firstErr != nil {
		t.Fatalf("并发写出现错误: %v", firstErr)
	}

	// 统计实际写入条数
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM messages WHERE conversation_id = ?", conv.ID).Scan(&count); err != nil {
		t.Fatalf("统计 messages 失败: %v", err)
	}
	want := writers * perWriter
	if count != want {
		t.Fatalf("并发写入条数 %d，期望 %d", count, want)
	}
	t.Logf("并发写 %d 条完成，耗时 %v，无 database is locked", want, elapsed)
}

// TestConcurrentReadWriteNoLocked 并发读+写不出现锁阻塞：读者持续查 messages，
// 写者持续 AddMessage，两者交错应全部成功（WAL 允许读不阻塞写）。
func TestConcurrentReadWriteNoLocked(t *testing.T) {
	db, cleanup, err := newTestDB(t)
	if err != nil {
		t.Fatalf("创建测试 DB 失败: %v", err)
	}
	defer cleanup()

	conv, err := db.CreateConversation("rw-smoke", ConversationCreateMeta{})
	if err != nil {
		t.Fatalf("CreateConversation 失败: %v", err)
	}

	const duration = 500 * time.Millisecond
	stop := make(chan struct{})
	var writeErr error
	var writeWg sync.WaitGroup
	var readErr error
	var readWg sync.WaitGroup

	writeWg.Add(1)
	go func() {
		defer writeWg.Done()
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
				_, err := db.AddMessage(conv.ID, "user", fmt.Sprintf("rw-%d", i), nil)
				if err != nil {
					writeErr = err
					return
				}
				i++
			}
		}
	}()

	readWg.Add(1)
	go func() {
		defer readWg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				var n int
				err := db.QueryRow("SELECT COUNT(*) FROM messages WHERE conversation_id = ?", conv.ID).Scan(&n)
				if err != nil {
					readErr = err
					return
				}
			}
		}
	}()

	time.Sleep(duration)
	close(stop)
	writeWg.Wait()
	readWg.Wait()

	if writeErr != nil {
		t.Fatalf("并发写失败: %v", writeErr)
	}
	if readErr != nil {
		t.Fatalf("并发读失败: %v", readErr)
	}
}
