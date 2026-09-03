//go:build cgo

package eventstream

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3" // CGO 驱动；纯 Go 构建（CGO_ENABLED=0）跳过本文件
)

// openSQLiteDB 用主项目双驱动抽象打开一个真实 SQLite（t.TempDir 隔离）。
// 本测试文件带 //go:build cgo tag，纯 Go 测试矩阵不编译本文件。
func openSQLiteDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_cdc.db")
	// DSN 与主项目 database.go:27 一致（WAL/foreign_keys/busy_timeout/synchronous）。
	dsn := dbPath + "?_journal_mode=WAL&_foreign_keys=1&_busy_timeout=5000&_synchronous=NORMAL"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(4)
	if err := db.Ping(); err != nil {
		t.Fatalf("ping sqlite: %v", err)
	}
	return db
}

// makeAssigned 构造一条已分配 ID 的 RecallAction（white-box：直接调 assign）。
func makeAssigned(id int64, query string, src EventSource, cause int64) *RecallAction {
	a := &RecallAction{RecallType: RecallTypeKnowledge, Query: query}
	a.assign(id, time.Now().UTC().Truncate(time.Second), src, cause)
	return a
}

// TestSQLiteStore_AppendAndGetEvent 验证：Append 一条事件后 GetEvent 能还原同字段。
func TestSQLiteStore_AppendAndGetEvent(t *testing.T) {
	db := openSQLiteDB(t)
	store, err := NewSQLiteStore(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	ev := &RecallAction{RecallType: RecallTypeKnowledge, Query: "sqli"}
	ev.assign(1, now, SourceUser, 0)

	if err := store.Append(ev); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, ok := store.GetEvent(1)
	if !ok {
		t.Fatal("GetEvent(1) returned false")
	}
	if got.ID() != 1 || got.EventType() != "recall_action" || got.Source() != SourceUser {
		t.Fatalf("roundtrip mismatch: id=%d type=%q source=%q", got.ID(), got.EventType(), got.Source())
	}
	if !got.Timestamp().Equal(now) {
		t.Fatalf("timestamp mismatch: got %v want %v", got.Timestamp(), now)
	}
	ra, ok := got.(*RecallAction)
	if !ok {
		t.Fatalf("expected *RecallAction, got %T", got)
	}
	if ra.Query != "sqli" || ra.RecallType != RecallTypeKnowledge {
		t.Fatalf("payload fields lost: %+v", ra)
	}
}

// TestSQLiteStore_LatestEventID 空表返 0；追加后返最大 seq。
func TestSQLiteStore_LatestEventID(t *testing.T) {
	db := openSQLiteDB(t)
	store, _ := NewSQLiteStore(db)

	if got := store.LatestEventID(); got != 0 {
		t.Fatalf("empty table LatestEventID = %d, want 0", got)
	}
	now := time.Now().UTC()
	for i := 1; i <= 3; i++ {
		ev := makeAssigned(int64(i), fmt.Sprintf("q%d", i), SourceAgent, 0)
		// 覆盖时间戳为固定 now，便于校验。
		ev.assign(int64(i), now, SourceAgent, 0)
		if err := store.Append(ev); err != nil {
			t.Fatal(err)
		}
	}
	if got := store.LatestEventID(); got != 3 {
		t.Fatalf("LatestEventID = %d, want 3", got)
	}
}

// TestSQLiteStore_EventsAfter 从 after 按升序流式读取。
func TestSQLiteStore_EventsAfter(t *testing.T) {
	db := openSQLiteDB(t)
	store, _ := NewSQLiteStore(db)
	now := time.Now().UTC()
	for i := 1; i <= 5; i++ {
		ev := makeAssigned(int64(i), fmt.Sprintf("q%d", i), SourceAgent, 0)
		ev.assign(int64(i), now, SourceAgent, 0)
		if err := store.Append(ev); err != nil {
			t.Fatal(err)
		}
	}
	// 从 seq=2 之后读，应得 3,4,5。
	ch, err := store.EventsAfter(context.Background(), 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []int64
	for ev := range ch {
		got = append(got, ev.ID())
	}
	want := []int64{3, 4, 5}
	if len(got) != len(want) {
		t.Fatalf("EventsAfter(2) = %v, want %v", got, want)
	}
	for i, id := range got {
		if id != want[i] {
			t.Fatalf("EventsAfter(2)[%d] = %d, want %d (out-of-order)", i, id, want[i])
		}
	}
	// limit 截断。
	ch2, _ := store.EventsAfter(context.Background(), 0, 2)
	var got2 []int64
	for ev := range ch2 {
		got2 = append(got2, ev.ID())
	}
	if len(got2) != 2 || got2[0] != 1 || got2[1] != 2 {
		t.Fatalf("EventsAfter(0, limit=2) = %v, want [1 2]", got2)
	}
}

// TestSQLiteStore_SearchEvents 按 IncludeTypes 过滤。
func TestSQLiteStore_SearchEvents(t *testing.T) {
	db := openSQLiteDB(t)
	store, _ := NewSQLiteStore(db)
	now := time.Now().UTC()
	// 3 条 recall_action + 2 条 condensation_action。
	for i := 1; i <= 3; i++ {
		ev := &RecallAction{Query: fmt.Sprintf("r%d", i)}
		ev.assign(int64(i), now, SourceAgent, 0)
		_ = store.Append(ev)
	}
	for i := 4; i <= 5; i++ {
		ev := &CondensationAction{Summary: fmt.Sprintf("c%d", i)}
		ev.assign(int64(i), now, SourceAgent, 0)
		_ = store.Append(ev)
	}
	ch := store.SearchEvents(1, Filter{IncludeTypes: []string{"recall_action"}})
	var got []string
	for ev := range ch {
		got = append(got, ev.EventType())
	}
	if len(got) != 3 {
		t.Fatalf("SearchEvents IncludeTypes=recall_action got %d events, want 3", len(got))
	}
	for _, et := range got {
		if et != "recall_action" {
			t.Fatalf("SearchEvents leaked non-matching type: %q", et)
		}
	}
}

// TestE2E_EventStreamWithSQLiteStore 全链路：AddEvent→SQLiteStore 持久化→broadcastLoop fan-out→订阅者收到。
// 同时验证 LatestEventID 恢复 curID（模拟 daemon 重启不丢历史 cursor）。
func TestE2E_EventStreamWithSQLiteStore(t *testing.T) {
	db := openSQLiteDB(t)
	store, _ := NewSQLiteStore(db)
	es := NewEventStream(store)
	defer es.Close()

	var mu sync.Mutex
	var got []Event
	cancel, err := es.Subscribe(SubscriberTest, "cb1", 8, func(ev Event) {
		mu.Lock()
		got = append(got, ev)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	// 发布 3 条事件，形成 cause 链。
	id1, _ := es.AddEvent(&RecallAction{Query: "a"}, SourceUser, 0)
	id2, _ := es.AddEvent(&RecallAction{Query: "b"}, SourceUser, id1)
	id3, _ := es.AddEvent(&CondensationAction{Summary: "s"}, SourceAgent, id2)

	if id1 != 1 || id2 != 2 || id3 != 3 {
		t.Fatalf("ids = %d %d %d, want 1 2 3", id1, id2, id3)
	}
	// 等待订阅者消费（broadcastLoop 异步）。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		c := len(got)
		mu.Unlock()
		if c >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 3 {
		t.Fatalf("delivered %d events, want 3", len(got))
	}
	// cause 链保留。
	if got[1].Cause() != id1 || got[2].Cause() != id2 {
		t.Fatalf("cause chain broken: ev2.cause=%d ev3.cause=%d", got[1].Cause(), got[2].Cause())
	}
	// 持久化生效：LatestEventID 反映已落库。
	if es.LatestEventID() != 3 {
		t.Fatalf("LatestEventID = %d, want 3", es.LatestEventID())
	}
	// GetEvent 从 SQLite 还原。
	ev1, ok := store.GetEvent(1)
	if !ok || ev1.EventType() != "recall_action" {
		t.Fatalf("GetEvent(1) roundtrip failed: %+v", ev1)
	}
}

// TestSQLiteStore_NilSafe db=nil 时所有方法 no-op 不 panic。
func TestSQLiteStore_NilSafe(t *testing.T) {
	store, _ := NewSQLiteStore(nil)
	if err := store.Append(makeAssigned(1, "x", SourceAgent, 0)); err != nil {
		t.Fatalf("nil db Append should no-op, got %v", err)
	}
	if _, ok := store.GetEvent(1); ok {
		t.Fatal("nil db GetEvent should return false")
	}
	if got := store.LatestEventID(); got != 0 {
		t.Fatalf("nil db LatestEventID = %d, want 0", got)
	}
	ch, err := store.EventsAfter(context.Background(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
		t.Fatal("nil db EventsAfter should yield nothing")
	}
	ch2 := store.SearchEvents(0, Filter{})
	for range ch2 {
		t.Fatal("nil db SearchEvents should yield nothing")
	}
}

// TestSQLiteStore_RestoreCursor 模拟 daemon 重启：新建 EventStream 时从 Store 恢复 curID。
func TestSQLiteStore_RestoreCursor(t *testing.T) {
	db := openSQLiteDB(t)
	store, _ := NewSQLiteStore(db)
	// 第一段生命周期：发布 5 条事件后 Close。
	es1 := NewEventStream(store)
	for i := 0; i < 5; i++ {
		_, _ = es1.AddEvent(&RecallAction{Query: "x"}, SourceAgent, 0)
	}
	es1.Close()
	if got := store.LatestEventID(); got != 5 {
		t.Fatalf("after first run LatestEventID = %d, want 5", got)
	}
	// 第二段生命周期：新 EventStream 应从 Store 恢复 curID=5，下一条事件 ID=6。
	es2 := NewEventStream(store)
	defer es2.Close()
	if got := es2.LatestEventID(); got != 5 {
		t.Fatalf("restored curID = %d, want 5", got)
	}
	id, _ := es2.AddEvent(&RecallAction{Query: "y"}, SourceAgent, 0)
	if id != 6 {
		t.Fatalf("new event id = %d, want 6 (cursor not restored)", id)
	}
}
