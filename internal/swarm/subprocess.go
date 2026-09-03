// Package swarm — 子进程后端：用 os/exec spawn 独立进程的 teammate，用文件 mailbox + stdin JSON 行通信。
//
// 移植自 OpenHarness swarm/subprocess_backend.py SubprocessBackend（150 行）+ spawn_utils.py。
// 简化：去掉 CLI flag 构造（CyberStrikeAI 用自身二进制，不 spawn 同一 CLI 入口），
// 改为传入 executable + args + env，由编排层决定。
package swarm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/google/uuid"
)

// SubprocessBackend 用独立进程执行 teammate。
//
// 移植自 OpenHarness swarm/subprocess_backend.py:28 SubprocessBackend。
type SubprocessBackend struct {
	homeDir    string
	execCfg    SubprocessExec // 默认可执行配置（构造时注入，Spawn 用之）
	mu         sync.Mutex
	agentTasks map[string]*subprocessEntry
}

type subprocessEntry struct {
	agentID string
	taskID  string
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	mailbox *Mailbox
	cancel  context.CancelFunc
	done    chan struct{}
}

// NewSubprocessBackend 创建子进程后端。homeDir 为空时返回错误。
//
// execCfg 提供可执行文件路径/参数/环境（由编排层决定，通常为自身二进制 + teammate 子命令）。
func NewSubprocessBackend(homeDir string, execCfg SubprocessExec) (*SubprocessBackend, error) {
	if homeDir == "" {
		return nil, errors.New("swarm: subprocess homeDir is empty")
	}
	if execCfg.Path == "" {
		return nil, errors.New("swarm: subprocess execCfg.Path is empty")
	}
	return &SubprocessBackend{homeDir: homeDir, execCfg: execCfg, agentTasks: make(map[string]*subprocessEntry)}, nil
}

// Type 返回后端类型。移植自 subprocess_backend.py:35。
func (b *SubprocessBackend) Type() BackendType { return BackendSubprocess }

// IsAvailable 子进程后端始终可用（os/exec 跨平台）。移植自 subprocess_backend.py:43。
func (b *SubprocessBackend) IsAvailable(_ context.Context) bool { return true }

// SubprocessExec 由调用方（编排层）填充的可执行文件信息。
//
// 移植自 spawn_utils.py get_teammate_command + build_inherited_cli_flags/env_vars。
type SubprocessExec struct {
	Path string   // 可执行文件路径
	Args []string // 命令行参数（不含 argv[0]）
	Env  []string // 环境变量（追加到 os.Environ()）
}

// Spawn 用独立进程执行 teammate。移植自 subprocess_backend.py:47。
//
// 实现 Backend 接口：只用 SpawnConfig，execCfg 在构造时注入。
func (b *SubprocessBackend) Spawn(ctx context.Context, cfg SpawnConfig) (SpawnResult, error) {
	execCfg := b.execCfg
	agentID := fmt.Sprintf("%s@%s", cfg.Name, cfg.Team)
	b.mu.Lock()
	if _, exists := b.agentTasks[agentID]; exists {
		b.mu.Unlock()
		return SpawnResult{AgentID: agentID, BackendType: BackendSubprocess, Success: false, Error: "agent already spawned"}, nil
	}
	taskID := "subprocess_" + uuid.NewString()[:12]
	mb, err := NewMailbox(b.homeDir, cfg.Team, agentID)
	if err != nil {
		b.mu.Unlock()
		return SpawnResult{}, fmt.Errorf("swarm: subprocess spawn mailbox: %w", err)
	}

	procCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(procCtx, execCfg.Path, execCfg.Args...)
	cmd.Env = append(os.Environ(), execCfg.Env...)
	if cfg.Cwd != "" {
		cmd.Dir = cfg.Cwd
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		b.mu.Unlock()
		return SpawnResult{}, fmt.Errorf("swarm: subprocess stdin pipe: %w", err)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		cancel()
		b.mu.Unlock()
		return SpawnResult{AgentID: agentID, BackendType: BackendSubprocess, Success: false, Error: err.Error()}, nil
	}

	entry := &subprocessEntry{
		agentID: agentID, taskID: taskID, cmd: cmd,
		stdin: stdin, mailbox: mb, cancel: cancel, done: make(chan struct{}),
	}
	b.agentTasks[agentID] = entry
	b.mu.Unlock()

	go func() {
		defer close(entry.done)
		_ = cmd.Wait()
	}()

	return SpawnResult{TaskID: taskID, AgentID: agentID, BackendType: BackendSubprocess, Success: true}, nil
}

// SendMessage 投递消息：写单行 JSON 到 stdin + 落文件 mailbox。
//
// 移植自 OpenHarness subprocess_backend.py:96。OpenHarness 只写 stdin JSON 行。
// 这里同时落文件 mailbox（持久化 + 跨进程可见），stdin JSON 行作为低延迟旁路。
func (b *SubprocessBackend) SendMessage(ctx context.Context, agentID string, msg Message) error {
	b.mu.Lock()
	entry, ok := b.agentTasks[agentID]
	b.mu.Unlock()
	if !ok {
		return ErrAgentNotFound
	}
	// 落文件 mailbox
	mbMsg := MailboxMessage{
		ID: uuid.NewString(), Type: MsgUserMessage,
		Sender: msg.FromAgent, Recipient: agentID,
		Payload: map[string]interface{}{
			"text": msg.Text, "from": msg.FromAgent, "summary": msg.Summary,
		},
		Timestamp: float64(time.Now().UnixNano()) / 1e9,
	}
	if err := entry.mailbox.Write(ctx, mbMsg); err != nil {
		return fmt.Errorf("swarm: subprocess send mailbox: %w", err)
	}
	// stdin JSON 行（字段名用 from，对齐 OpenHarness subprocess_backend.py:108）。
	// 写失败不阻塞（子进程可能已退出/未读 stdin）；写管道与 Shutdown 并发时以 mu 串行化由
	// 调用方保证——entry 生命周期内 SendMessage 与 Shutdown 不同协程竞争时由 os/exec pipe
	// 的 Close 原子性兜底（写已关闭管道返回错误，被忽略）。
	line := fmt.Sprintf(`{"text":%q,"from":%q,"timestamp":%q,"summary":%q}`+"\n",
		msg.Text, msg.FromAgent, msg.Timestamp.Format(time.RFC3339), msg.Summary)
	if entry.stdin != nil {
		_, _ = io.WriteString(entry.stdin, line)
	}
	return nil
}

// Shutdown 终止子进程。移植自 subprocess_backend.py:120。
//
// force 被 OpenHarness 忽略（注释：always SIGTERM then SIGKILL）。
// 这里实现：cancel()（发 SIGTERM，等 done；父 ctx 超时则 Kill）。
func (b *SubprocessBackend) Shutdown(ctx context.Context, agentID string, force bool) (bool, error) {
	b.mu.Lock()
	entry, ok := b.agentTasks[agentID]
	b.mu.Unlock()
	if !ok {
		return false, ErrAgentNotFound
	}
	// 先关 stdin 发 EOF（子进程若在读 stdin 会自然退出），再 cancel 发 SIGTERM
	if entry.stdin != nil {
		_ = entry.stdin.Close()
	}
	entry.cancel() // context 取消 → exec.CommandContext 发 SIGTERM
	select {
	case <-entry.done:
	case <-ctx.Done():
		_ = entry.cmd.Process.Kill()
		<-entry.done
	}
	b.mu.Lock()
	delete(b.agentTasks, agentID)
	b.mu.Unlock()
	return true, nil
}

// ShutdownAll 终止所有子进程 teammate（退出清理用）。
func (b *SubprocessBackend) ShutdownAll(ctx context.Context) {
	b.mu.Lock()
	ids := make([]string, 0, len(b.agentTasks))
	for id := range b.agentTasks {
		ids = append(ids, id)
	}
	b.mu.Unlock()
	for _, id := range ids {
		_, _ = b.Shutdown(ctx, id, true)
	}
}
