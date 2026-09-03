// Package cost 提供轻量成本跟踪：按 session 聚合 token 用量与美元成本。
//
// 设计移植自参考项目 OpenHarness-main 的 src/openharness/engine/cost_tracker.py
//（24 行 CostTracker.add/total）与 src/openharness/api/usage.py UsageSnapshot。
// 适配 CyberStrikeAI (Go)：
//   - 复用 internal/agent.TokenCounter（tiktoken）估算 token 数。
//   - pricing.go 内置 model→price 表（input/output per 1M token，可扩展）。
//   - 聚合粒度：per-session（Add 累加）；Report 按 model 分组。
//   - 线程安全：sync.RWMutex 保护累加与读取。
package cost

import (
	"errors"
	"strings"
	"sync"
	"time"
)

// UsageSnapshot 是一次模型调用的 token 用量快照。
//
// 移植自 OpenHarness api/usage.py UsageSnapshot，扩展 CostUSD 与 Timestamp。
type UsageSnapshot struct {
	Model        string    // 模型名（如 claude-sonnet-4-5）
	InputTokens  int       // 输入 token
	OutputTokens int       // 输出 token
	CacheReadTokens  int   // 缓存命中读取（不计费或低价）
	CacheWriteTokens int   // 缓存写入（计费）
	CostUSD      float64   // 本次调用美元成本（由 pricing 算出）
	Timestamp    time.Time // 调用时间
}

// Tracker 累加 session 级用量。
//
// 移植自 OpenHarness engine/cost_tracker.py:8 CostTracker。
// 扩展：OpenHarness 只累加 input/output token，这里加 cost 与按 model 分组。
type Tracker struct {
	mu      sync.RWMutex
	usage   UsageSnapshot             // 全量累加
	byModel map[string]*UsageSnapshot // 按 model 分组累加
}

// New 创建一个 cost tracker。
func New() *Tracker {
	return &Tracker{byModel: make(map[string]*UsageSnapshot)}
}

// Add 累加一次用量快照。移植自 OpenHarness cost_tracker.py:14 add。
//
// L4 修复：byModel 分组键统一小写化（"Claude-Sonnet-4-5" 与 "claude-sonnet-4-5" 同组）。
// 注意：CostUSD==0 时自动补算（估算器语义）；调用方想显式记 0 成本应传负值哨兵或
// 直接构造 byModel（当前不支持显式 0）。
func (t *Tracker) Add(u UsageSnapshot) error {
	if u.Model == "" {
		return errors.New("cost: usage model must not be empty")
	}
	if u.Timestamp.IsZero() {
		u.Timestamp = time.Now()
	}
	if u.CostUSD == 0 {
		// 若调用方未算 cost，这里用 pricing 表补算
		u.CostUSD = Calculate(u)
	}
	groupKey := strings.ToLower(u.Model)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.usage.InputTokens += u.InputTokens
	t.usage.OutputTokens += u.OutputTokens
	t.usage.CacheReadTokens += u.CacheReadTokens
	t.usage.CacheWriteTokens += u.CacheWriteTokens
	t.usage.CostUSD += u.CostUSD
	t.usage.Timestamp = u.Timestamp
	m, ok := t.byModel[groupKey]
	if !ok {
		m = &UsageSnapshot{Model: groupKey}
		t.byModel[groupKey] = m
	}
	m.InputTokens += u.InputTokens
	m.OutputTokens += u.OutputTokens
	m.CacheReadTokens += u.CacheReadTokens
	m.CacheWriteTokens += u.CacheWriteTokens
	m.CostUSD += u.CostUSD
	m.Timestamp = u.Timestamp
	return nil
}

// Total 返回全量累加快照。移植自 OpenHarness cost_tracker.py:21 total。
func (t *Tracker) Total() UsageSnapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.usage
}

// Report 按 model 分组的用量报告。
type Report struct {
	Total   UsageSnapshot              // 全量
	ByModel map[string]UsageSnapshot   // 按 model 分组
}

// Report 返回按 model 分组的用量报告。
func (t *Tracker) Report() Report {
	t.mu.RLock()
	defer t.mu.RUnlock()
	byModel := make(map[string]UsageSnapshot, len(t.byModel))
	for k, v := range t.byModel {
		byModel[k] = *v
	}
	return Report{Total: t.usage, ByModel: byModel}
}

// Reset 清零（测试用）。
func (t *Tracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.usage = UsageSnapshot{}
	t.byModel = make(map[string]*UsageSnapshot)
}
