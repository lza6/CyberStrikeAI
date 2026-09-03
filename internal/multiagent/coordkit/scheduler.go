// Package coordkit
package coordkit

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// SchedulerStrategy 调度策略。移植自 open-multi-agent-main Scheduler 四策略：
//协调器在每轮 ready 任务集中按策略选择派发顺序，配合 MaxParallel 信号量限并发。
//
//四策略语义（与参考项目 scheduler.ts 对齐）：
//   - round-robin：跨轮游标轮转，防某 assignee 饥饿（与改造前 dispatchQueue 等价）。
//   - least-busy：按 assignee 当前 running 数升序派发（负载均衡）。
//   - capability-match：assignee 已在 running 的优先（减少 agent 冷启动开销）。
//   - dependency-first：BFS countBlockedDependents 阻塞最多后继的优先（缩短关键路径）。
type SchedulerStrategy string

const (
	StrategyRoundRobin      SchedulerStrategy = "round-robin"
	StrategyLeastBusy       SchedulerStrategy = "least-busy"
	StrategyCapabilityMatch SchedulerStrategy = "capability-match"
	StrategyDependencyFirst SchedulerStrategy = "dependency-first"
)

// DefaultSchedulerStrategy 默认策略。round-robin 与改造前 dispatchQueue 行为等价
//（全部 ready 派发，仅顺序不同），保证向后兼容。
const DefaultSchedulerStrategy = StrategyRoundRobin

// RetryBackoffCap 重试退避上限。移植自 open-multi-agent-main computeRetryDelay cap 30s。
//导出供 coordinator_orchestrator.go runOneTask 消费。
const RetryBackoffCap = 30 * time.Second

// Scheduler 协调器调度器。无状态游标（rrIdx）保证 round-robin 跨轮稳定。
//所有策略都返回全部 ready 任务（按策略排序），MaxParallel 信号量仍限并发——
//四策略改变的是"先派谁"，不是"派多少"，与现有批次模式兼容。
//
// 并发安全：rrIdx 由 mu 保护，Select 可跨 goroutine 安全调用。
type Scheduler struct {
	strategy SchedulerStrategy
	mu       sync.Mutex
	rrIdx    int // round-robin 跨轮游标
}

// NewScheduler 构造调度器。空 strategy 走 DefaultSchedulerStrategy（向后兼容）。
func NewScheduler(strategy SchedulerStrategy) *Scheduler {
	s := strings.TrimSpace(string(strategy))
	if s == "" {
		strategy = DefaultSchedulerStrategy
	}
	return &Scheduler{strategy: strategy}
}

// Strategy 返回当前策略。
func (s *Scheduler) Strategy() SchedulerStrategy {
	if s == nil {
		return DefaultSchedulerStrategy
	}
	return s.strategy
}

// Select 返回本轮可派发的 ready 任务有序子集。
//   - dag 已解析的 DAG（dependency-first 需遍历后继）
//   - ready 本轮 dag.IsReady 命中的 pending 任务
//   - runningAssignees 当前 in_progress 任务的 assignee 计数（least-busy/capability-match 用）
//
// 所有策略都返回全部 ready（按策略排序），MaxParallel 信号量仍限并发。
func (s *Scheduler) Select(dag *DAG, ready []*Task, runningAssignees map[string]int) []*Task {
	if s == nil || len(ready) == 0 {
		return ready
	}
	out := append([]*Task(nil), ready...)
	switch s.strategy {
	case StrategyLeastBusy:
		sort.SliceStable(out, func(i, j int) bool {
			ai := runningAssignees[out[i].Assignee]
			aj := runningAssignees[out[j].Assignee]
			if ai != aj {
				return ai < aj
			}
			return out[i].Title < out[j].Title
		})
	case StrategyCapabilityMatch:
		sort.SliceStable(out, func(i, j int) bool {
			ri := runningAssignees[out[i].Assignee]
			rj := runningAssignees[out[j].Assignee]
			// 已 running 的优先（ri>0 排前），减少 agent 冷启动开销。
			if (ri > 0) != (rj > 0) {
				return ri > 0
			}
			return out[i].Title < out[j].Title
		})
	case StrategyDependencyFirst:
		blocked := make(map[string]int, len(out))
		for _, t := range out {
			blocked[t.ID] = countBlockedDependents(dag, t)
		}
		sort.SliceStable(out, func(i, j int) bool {
			if blocked[out[i].ID] != blocked[out[j].ID] {
				return blocked[out[i].ID] > blocked[out[j].ID]
			}
			return out[i].Title < out[j].Title
		})
	default: // StrategyRoundRobin
		// 跨轮游标：按 rrIdx 起始轮转，保证不同轮次起派 agent 不总同一批。
		s.mu.Lock()
		if s.rrIdx >= len(out) {
			s.rrIdx = 0
		}
		start := s.rrIdx
		s.rrIdx = (s.rrIdx + 1) % len(out)
		s.mu.Unlock()
		rotated := append([]*Task(nil), out[start:]...)
		rotated = append(rotated, out[:start]...)
		out = rotated
	}
	return out
}

// countBlockedDependents BFS 统计以 t 为前驱的传递后继数（含直接+间接）。
//移植自 open-multi-agent-main dependency-first 策略的 BFS。
//用于 dependency-first 排序：阻塞越多后继的任务越优先派发，缩短关键路径。
func countBlockedDependents(dag *DAG, t *Task) int {
	if dag == nil || t == nil {
		return 0
	}
	visited := map[string]struct{}{t.ID: {}}
	queue := []string{t.ID}
	count := 0
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, other := range dag.Tasks() {
			if _, seen := visited[other.ID]; seen {
				continue
			}
			for _, dep := range other.DependsOn {
				if dep == cur {
					visited[other.ID] = struct{}{}
					queue = append(queue, other.ID)
					count++
					break
				}
			}
		}
	}
	return count
}

// ComputeRetryDelay 计算重试退避时长。移植自 open-multi-agent-main computeRetryDelay。
//   - baseDelay 基准延迟（Task.RetryDelay 转 time.Duration；<=0 默认 1s，与 Task.RetryDelay 0=1000ms 对齐）
//   - backoff 退避因子（Task.Backoff；<=0 默认 2.0）
//   - attempt 已失败次数（0 基：首次失败 attempt=0，第一次重试前等待 baseDelay*backoff^0=baseDelay）
//   - cap 退避上限（<=0 用 RetryBackoffCap=30s）
//
// 公式：delay = baseDelay * backoff^attempt，cap 截断。
// 防 attempt 溢出：multiplier 超过 cap/baseDelay 后停止累乘。
func ComputeRetryDelay(baseDelay time.Duration, backoff float64, attempt int, cap time.Duration) time.Duration {
	if baseDelay <= 0 {
		baseDelay = time.Second
	}
	if backoff <= 0 {
		backoff = 2.0
	}
	if cap <= 0 {
		cap = RetryBackoffCap
	}
	// 防 attempt 溢出：先转 float64 计算再转回，且 multiplier 超过 cap/baseDelay 停止累乘。
	capMultiplier := float64(cap) / float64(baseDelay)
	multiplier := 1.0
	for i := 0; i < attempt && multiplier < capMultiplier; i++ {
		multiplier *= backoff
	}
	delay := time.Duration(float64(baseDelay) * multiplier)
	if delay > cap {
		delay = cap
	}
	if delay < baseDelay {
		delay = baseDelay
	}
	return delay
}
