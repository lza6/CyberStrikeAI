// Package swarm — 后端注册表：检测可用后端 + Register/Get。
//
// 移植自 OpenHarness swarm/registry.py BackendRegistry（410 行）。
// 简化：去掉 tmux/iterm2 pane backend 检测（CyberStrikeAI 是 Web/Electron 形态，无需 pane 可视化）。
// 只保留 TeammateExecutor：subprocess + in_process 两后端。
package swarm

import (
	"context"
	"errors"
	"os"
	"sync"
)

// Registry 是后端注册表单例。移植自 OpenHarness registry.py:185 BackendRegistry。
type Registry struct {
	mu        sync.RWMutex
	executors map[BackendType]Backend
	preferred BackendType // 用户偏好（auto/in_process/subprocess）
	fallback  bool        // in_process 回退标记
}

var (
	registryOnce sync.Once
	registryInst *Registry
)

// GetRegistry 返回进程级注册表单例。移植自 OpenHarness registry.py:397 get_backend_registry。
func GetRegistry() *Registry {
	registryOnce.Do(func() {
		registryInst = &Registry{executors: make(map[BackendType]Backend)}
	})
	return registryInst
}

// Register 注册一个 TeammateExecutor 后端。
func (r *Registry) Register(b Backend) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.executors[b.Type()] = b
}

// Get 返回指定类型的后端。移植自 OpenHarness registry.py get_executor。
func (r *Registry) Get(bt BackendType) (Backend, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	b, ok := r.executors[bt]
	return b, ok
}

// SetPreferred 设置用户偏好后端（"auto"/"in_process"/"subprocess"）。
//
// 移植自 OpenHarness registry.py:292 get_preferred_backend。
func (r *Registry) SetPreferred(pref string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch pref {
	case "in_process":
		r.preferred = BackendInProcess
	case "subprocess":
		r.preferred = BackendSubprocess
	default: // "auto" 或空
		r.preferred = ""
	}
}

// MarkInProcessFallback 置位 in_process 回退标记。
//
// 移植自 OpenHarness registry.py:319 mark_in_process_fallback。
// spawn 失败后调用，使后续 Detect 持续返回 in_process。
func (r *Registry) MarkInProcessFallback() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fallback = true
}

// Detect 检测应使用的后端。移植自 OpenHarness registry.py:128 detect_backend。
//
// 优先级（简化版，去掉 tmux/iterm2）：
//  1. in_process 回退标记 → in_process
//  2. 用户偏好非空且已注册 → 偏好
//  3. subprocess（总是可用兜底）
func (r *Registry) Detect(ctx context.Context) (Backend, error) {
	r.mu.RLock()
	fallback := r.fallback
	pref := r.preferred
	r.mu.RUnlock()

	if fallback {
		if b, ok := r.Get(BackendInProcess); ok && b.IsAvailable(ctx) {
			return b, nil
		}
	}
	if pref != "" {
		if b, ok := r.Get(pref); ok && b.IsAvailable(ctx) {
			return b, nil
		}
	}
	if b, ok := r.Get(BackendSubprocess); ok && b.IsAvailable(ctx) {
		return b, nil
	}
	return nil, errors.New("swarm: no available backend (subprocess not registered)")
}

// HealthCheck 遍历所有注册后端调 IsAvailable。移植自 OpenHarness registry.py:340。
func (r *Registry) HealthCheck(ctx context.Context) map[BackendType]bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[BackendType]bool, len(r.executors))
	for bt, b := range r.executors {
		result[bt] = b.IsAvailable(ctx)
	}
	return result
}

// Reset 清缓存 + 重置。移植自 OpenHarness registry.py:364（测试用）。
func (r *Registry) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.executors = make(map[BackendType]Backend)
	r.preferred = ""
	r.fallback = false
}

// RegisterDefaults 注册默认后端。移植自 OpenHarness registry.py:379 _register_defaults。
//
// subprocess 无条件注册；in_process 仅当 homeDir 非空且 run 非空时注册。
// tmux/iterm2 pane backend 不注册（保留接口，实现返回 ErrNotSupported）。
func (r *Registry) RegisterDefaults(homeDir string, run RunFunc, execCfg SubprocessExec) error {
	sub, err := NewSubprocessBackend(homeDir, execCfg)
	if err != nil {
		return err
	}
	r.Register(sub)
	if run != nil {
		inProc, err := NewInProcessBackend(homeDir, run)
		if err != nil {
			return err
		}
		r.Register(inProc)
	}
	return nil
}

// detectTmux 检测是否在 tmux session 内。移植自 OpenHarness registry.py:25。
//
// CyberStrikeAI 不用 tmux pane，此函数仅保留用于环境探测诊断。
func detectTmux() bool {
	if os.Getenv("TMUX") == "" {
		return false
	}
	_, err := execLookPath("tmux")
	return err == nil
}

// detectITerm2 检测是否在 iTerm2 内。移植自 OpenHarness registry.py:42。
func detectITerm2() bool {
	return os.Getenv("ITERM_SESSION_ID") != ""
}
