//go:build cgo

// Package orchestrator_test 提供 AO-E2E 三能力联动测验：
// worker 隔离（pluginslot Workspace）→ daemon 推进（StatusProvider 轮询 + Action 派生）
// → CDC 派生看板（EventStream + SQLiteStore 持久化 + statusboard 纯函数）。
//
// 场景：两个 worker 各建独立隔离工作区（directory + git-worktree）→
// daemon 观察其状态事实（active → waiting_input → 消失）→ 每 tick 发出的 Action
// 以 CDC 事件形式 Append 进 EventStream（SQLite 持久化）→ Poller live 推给看板订阅者。
//
// cgo build tag：E2E 依赖 mattn/go-sqlite3（CGO 驱动）；纯 Go 测试矩阵（CGO_ENABLED=0）
// 跳过本文件，避免 stub 驱动 ping 失败污染矩阵（审计发现 4）。
package orchestrator_test

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberstrike-ai/internal/eventstream"
	"cyberstrike-ai/internal/orchestrator"
	"cyberstrike-ai/internal/pluginslot"
	"cyberstrike-ai/internal/statusboard"
	"cyberstrike-ai/internal/statusboard/cdc"

	_ "github.com/mattn/go-sqlite3" // E2E 需要 CGO sqlite 驱动（与主项目生产一致）
)

// skipIfNoGit 在系统无 git 时跳过。
func skipIfNoGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
}

func makeGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		fullArgs := append([]string{"-C", dir}, args...)
		out, err := exec.Command("git", fullArgs...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, string(out))
		}
	}
	run("init", "-q")
	run("config", "user.email", "e2e@test.com")
	run("config", "user.name", "e2e")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# e2e\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-q", "-m", "init")
	return dir
}

// cdcEvent CDC 事件载体（genericEvent 风格的 E2E 专用 Event 实现）。
type cdcEvent struct {
	id        int64
	ts        time.Time
	src       eventstream.EventSource
	cause     int64
	etype     string
	sessionID string
	payload   string
}

func (e *cdcEvent) ID() int64                       { return e.id }
func (e *cdcEvent) Timestamp() time.Time            { return e.ts }
func (e *cdcEvent) Source() eventstream.EventSource { return e.src }
func (e *cdcEvent) Cause() int64                    { return e.cause }
func (e *cdcEvent) EventType() string               { return e.etype }
func (e *cdcEvent) SessionID() string               { return e.sessionID }

var _ eventstream.Event = (*cdcEvent)(nil)

// TestE2E_WorkerIsolationToDaemonToCDC 全链路 E2E。
func TestE2E_WorkerIsolationToDaemonToCDC(t *testing.T) {
	skipIfNoGit(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// === 阶段 1：worker 隔离（SlotWorkspace 两种模式真实建工作区）===
	pluginslot.RegisterWorkspaceFactories()
	root := t.TempDir()
	repo := makeGitRepo(t)

	// worker-1: directory 隔离。
	dirWS := pluginslot.Get(pluginslot.SlotWorkspace, "directory", map[string]interface{}{"managed_root": root}).(pluginslot.Workspace)
	info1, err := dirWS.Create(pluginslot.WorkspaceConfig{ProjectID: "proj-e2e", SessionID: "worker-1"})
	if err != nil {
		t.Fatalf("worker-1 directory create: %v", err)
	}
	defer func() { _ = dirWS.Destroy(info1) }()
	if info1.Isolation != pluginslot.IsolationDirectory {
		t.Fatalf("worker-1 isolation = %q", info1.Isolation)
	}

	// worker-2: git-worktree 隔离（真实 git worktree + branch）。
	gitWS := pluginslot.Get(pluginslot.SlotWorkspace, "git-worktree", map[string]interface{}{"managed_root": root}).(pluginslot.Workspace)
	info2, err := gitWS.Create(pluginslot.WorkspaceConfig{
		ProjectID: "proj-e2e", SessionID: "worker-2", Kind: "worker",
		RepoPath: repo, BaseBranch: "master",
	})
	if err != nil {
		t.Fatalf("worker-2 git create: %v", err)
	}
	defer func() {
		_ = os.WriteFile(filepath.Join(info2.Path, ".e2e-cleanup"), []byte("x"), 0o644)
		_ = gitWS.(interface {
			ForceDestroy(context.Context, pluginslot.WorkspaceInfo) error
		}).ForceDestroy(context.Background(), info2)
	}()
	if info2.Isolation != pluginslot.IsolationGitWorktree || info2.Branch != "ao/worker-2" {
		t.Fatalf("worker-2 info = %+v", info2)
	}
	// 两 worker 工作区互不可见（隔离生效）。
	if samePath(info1.Path, info2.Path) {
		t.Fatal("worker workspaces must be distinct")
	}

	// === 阶段 2：daemon 推进（StatusProvider 观察 worker 事实 → Action）===
	// 用 map 模拟运行时事实源（生产中由会话存储/会话表填充）。
	provider := &e2eProvider{facts: map[string]orchestrator.WorkerFacts{}}
	provider.set("worker-1", workerFacts("proj-e2e", statusboard.ActivityActive))
	provider.set("worker-2", workerFacts("proj-e2e", statusboard.ActivityActive))

	// === 阶段 3：CDC 持久化（EventStream 分发 + SQLiteStore 显式 Append 持久化）===
	db := openE2ESQLite(t)
	store, err := eventstream.NewSQLiteStore(db)
	if err != nil {
		t.Fatal(err)
	}
	// EventStream 用 nil store（纯分发）；持久化由 daemon handler 显式 Append（见下）。
	es := eventstream.NewEventStream(nil)
	defer es.Close()

	var mu sync.Mutex
	boardEvents := make(map[string]string) // sessionID → latest status（看板内存投影）
	boardSub := func(ev eventstream.Event) {
		mu.Lock()
		defer mu.Unlock()
		if ce, ok := ev.(*cdcEvent); ok && ce.payload != "" {
			boardEvents[ce.sessionID] = ce.payload
		}
	}
	// daemon Action → CDC Event → EventStream（持久化 + 分发）。
	// 关键：先构造事件（id=0），AddEvent 分配真实 ID 后把 id 回写进事件，
	// 再显式 Append 持久化（AddEvent 内部 store.Append 用的是分配前 id=0 的 insert
	// 会撞 UNIQUE(seq=0)——本 E2E 直接 AddEvent(nil store) 分配 + 手动 Append）。
	// 简化：EventStream 用 nil store 只做分发；持久化走显式 store.Append(ev)（id 已分配）。
	var lastCause int64
	d := orchestrator.NewDaemon(provider, func(_ context.Context, a orchestrator.Action) {
		ce := &cdcEvent{
			ts:        time.Now().UTC(),
			src:       eventstream.SourceEnvironment,
			cause:     lastCause,
			etype:     "worker_status_changed",
			sessionID: a.SessionID,
			payload:   statusOf(a),
		}
		id, err := es.AddEvent(ce, eventstream.SourceEnvironment, ce.cause)
		if err != nil {
			t.Logf("AddEvent: %v", err)
			return
		}
		ce.id = id // 回写 ID 后再持久化
		lastCause = id
		if err := store.Append(ce); err != nil {
			t.Logf("store.Append: %v", err)
		}
	}, orchestrator.OrchestratorConfig{Interval: 50 * time.Millisecond})
	d.Start(ctx)
	// 注意：EventStream 的订阅者收到的是 AddEvent 分发；boardSub 需在 daemon 前订阅。
	_, _ = es.Subscribe(eventstream.SubscriberTest, "board", 8, boardSub)

	// 等首轮 poll（首观察 → 2 条 status_changed）。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		c := len(boardEvents)
		mu.Unlock()
		if c >= 2 {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	mu.Lock()
	if len(boardEvents) != 2 {
		mu.Unlock()
		t.Fatalf("board events = %d, want 2 (worker-1 + worker-2)", len(boardEvents))
	}
	if boardEvents["worker-1"] != string(statusboard.StatusWorking) || boardEvents["worker-2"] != string(statusboard.StatusWorking) {
		t.Fatalf("board = %v, want both working", boardEvents)
	}
	mu.Unlock()

	// worker-1 转 waiting_input → daemon 应发 nudge → CDC 更新看板投影。
	provider.set("worker-1", workerFacts("proj-e2e", statusboard.ActivityWaitingInput))
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		s := boardEvents["worker-1"]
		mu.Unlock()
		if s == string(statusboard.StatusNeedsInput) {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	mu.Lock()
	if boardEvents["worker-1"] != string(statusboard.StatusNeedsInput) {
		mu.Unlock()
		t.Fatalf("worker-1 board = %q, want needs_input", boardEvents["worker-1"])
	}
	mu.Unlock()

	// 验证 SQLite 持久化：change_log 有 ≥3 行，且 GetEvent 可还原。
	if got := store.LatestEventID(); got < 3 {
		t.Fatalf("persisted events = %d, want >= 3", got)
	}
	ev1, ok := store.GetEvent(1)
	if !ok || ev1.EventType() != "worker_status_changed" {
		t.Fatalf("GetEvent(1) = %v (ok=%v)", ev1, ok)
	}
	// 重启恢复：新 EventStream 从 Store 恢复 cursor（此时用注入 store 的构造验证恢复语义）。
	es2 := eventstream.NewEventStream(store)
	defer es2.Close()
	if got := es2.LatestEventID(); got < 3 {
		t.Fatalf("restored cursor = %d, want >= 3", got)
	}

	// === 阶段 4：CDC live push 真串联（审计发现 3）===
	// StoreSource(SQLiteStore) → cdc.Poller → cdc.Broadcaster → 看板订阅者。
	// 验证持久化事件经 CDC live push 路径真实到达订阅者（此前该段只被单测覆盖）。
	src := &cdc.StoreSource{Store: store}
	bc := cdc.NewBroadcaster()
	var cdcMu sync.Mutex
	var cdcGot []int64 // 收到的 seq
	cdcUnsub := bc.Subscribe(func(ev eventstream.Event) {
		cdcMu.Lock()
		cdcGot = append(cdcGot, ev.ID())
		cdcMu.Unlock()
	})
	defer cdcUnsub()
	poller := cdc.NewPoller(src, bc, cdc.PollerConfig{Interval: 30 * time.Millisecond, StartSeq: 0})
	pollCtx, pollCancel := context.WithCancel(context.Background())
	pollDone := poller.Start(pollCtx)
	// 驱动一轮同步 Poll 保证确定性消费（Start 的异步 ticker 也在跑，两者幂等）。
	if err := poller.Poll(context.Background()); err != nil {
		t.Fatalf("cdc poll: %v", err)
	}
	pollCancel()
	<-pollDone
	cdcMu.Lock()
	if len(cdcGot) < 3 {
		cdcMu.Unlock()
		t.Fatalf("cdc live push delivered %d events, want >= 3", len(cdcGot))
	}
	// 顺序升序（seq 序）。
	for i := 1; i < len(cdcGot); i++ {
		if cdcGot[i] <= cdcGot[i-1] {
			cdcMu.Unlock()
			t.Fatalf("cdc delivery out of order: %v", cdcGot)
		}
	}
	if poller.LastSeq() < 3 {
		cdcMu.Unlock()
		t.Fatalf("poller cursor = %d, want >= 3", poller.LastSeq())
	}
	cdcMu.Unlock()

	// === 收尾：worker-2 ForceDestroy 清理 worktree（注册表同步清）===
	_ = gitWS.(interface {
		ForceDestroy(context.Context, pluginslot.WorkspaceInfo) error
	}).ForceDestroy(context.Background(), info2)
	// 验证 worktree 已从 git 注册表移除。
	list, _ := exec.Command("git", "-C", repo, "worktree", "list", "--porcelain").Output()
	if strings.Contains(strings.ReplaceAll(string(list), "\\", "/"), filepath.ToSlash(info2.Path)) {
		t.Fatalf("worktree still registered after ForceDestroy: %s", string(list))
	}

	d.Stop()
	cancel()
}

// e2eProvider E2E 用的 StatusProvider。
type e2eProvider struct {
	mu    sync.Mutex
	facts map[string]orchestrator.WorkerFacts
}

func (p *e2eProvider) set(sid string, wf orchestrator.WorkerFacts) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.facts[sid] = wf
}

func (p *e2eProvider) ListWorkerFacts(ctx context.Context) (map[string]orchestrator.WorkerFacts, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]orchestrator.WorkerFacts, len(p.facts))
	for k, v := range p.facts {
		out[k] = v
	}
	return out, nil
}

// workerFacts 构造一个活动 worker 事实。
func workerFacts(projectID string, activity statusboard.ActivityState) orchestrator.WorkerFacts {
	return orchestrator.WorkerFacts{
		ProjectID: projectID,
		Session: statusboard.SessionFacts{
			Activity:       activity,
			LastActivityAt: time.Now().UTC(),
			HasSignal:      true,
		},
	}
}

// statusOf 从 Action 提取新状态（Payload["to"] 或 Payload["status"]）。
func statusOf(a orchestrator.Action) string {
	if to, ok := a.Payload["to"].(string); ok {
		return to
	}
	if s, ok := a.Payload["status"].(string); ok {
		return s
	}
	return ""
}

func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// openE2ESQLite 打开 E2E 用 SQLite（t.TempDir 隔离）。
func openE2ESQLite(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "e2e_cdc.db")
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_foreign_keys=1&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(4)
	if err := db.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return db
}
