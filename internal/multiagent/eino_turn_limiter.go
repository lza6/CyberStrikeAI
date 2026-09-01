package multiagent

import (
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// TurnToolCallLimiter 限制单个对话轮（turn）内工具调用次数，防卡死。
// 移植自 strix config.tool_call_limits.TurnToolCallLimiter 思想：退化生成
// 可能在一次回复里排队数百上千次工具调用（典型如 poll/wait 循环），运行环
// 全部执行会让 agent 数小时无响应。本限流器按 turnID 计数，超限返回合成
// 工具结果而非真实执行，让模型在下一轮自行重新决策。
//
// 设计要点：
//   - turnID 取 conversationID+用户消息 hash 或 conversationID+turn 序号；
//     Eino 无原生 turnID 概念，故由调用方在每轮新消息时 Reset。
//   - limit<=0 表示不启用（与 strix TurnToolCallLimiter.enabled 语义一致）。
//   - 并发安全：mu 保护 counts；CheckAndIncrement 原子"检查+自增"。
type TurnToolCallLimiter struct {
	maxPerTurn int
	mu         sync.Mutex
	counts     map[string]int
	// decisions 记录同一 callID 的决策幂等性：同一工具调用可能被中间件多次
	// 询问（流式事件 + 完成响应），与 strix _decisions 语义一致。
	decisions map[string]bool
	// dropped 累计被拦截的工具调用数（仅观测，不参与判定）。
	dropped int
}

// NewTurnToolCallLimiter 创建限流器。max<=0 时返回的实例 CheckAndIncrement
// 恒返回 true（不启用），仍可安全调用。
func NewTurnToolCallLimiter(max int) *TurnToolCallLimiter {
	return &TurnToolCallLimiter{
		maxPerTurn: max,
		counts:     make(map[string]int),
		decisions:  make(map[string]bool),
	}
}

// Enabled 是否启用限流。max<=0 时未启用。
func (l *TurnToolCallLimiter) Enabled() bool {
	return l != nil && l.maxPerTurn > 0
}

// MaxPerTurn 返回单轮上限（未启用时返回 0）。
func (l *TurnToolCallLimiter) MaxPerTurn() int {
	if l == nil {
		return 0
	}
	return l.maxPerTurn
}

// CheckAndIncrement 检查 turnID 当前计数是否在上限内，若是则自增并返回 true；
// 否则返回 false 且不执行真实工具。callID 用于幂等：同一 callID 多次询问
// 返回同一决策（与 strix _decisions 语义一致），避免流式 + 完成两次询问
// 双倍计数。
//
// 返回 (allowed, current, limit)：current 是本轮已放行的工具调用数（含本次），
// limit 是上限（未启用时 current=0、limit=0、allowed=true）。
func (l *TurnToolCallLimiter) CheckAndIncrement(turnID, callID string) (allowed bool, current int, limit int) {
	if !l.Enabled() {
		return true, 0, 0
	}
	turnID = strings.TrimSpace(turnID)
	callID = strings.TrimSpace(callID)
	l.mu.Lock()
	defer l.mu.Unlock()

	// 幂等：同一 callID 已决策则复用，不重复计数。
	if callID != "" {
		if decided, ok := l.decisions[callID]; ok {
			return decided, l.counts[turnID], l.maxPerTurn
		}
	}

	cur := l.counts[turnID]
	if cur >= l.maxPerTurn {
		// 超限：记决策并累计 dropped。
		if callID != "" {
			l.decisions[callID] = false
		}
		l.dropped++
		return false, cur, l.maxPerTurn
	}
	cur++
	l.counts[turnID] = cur
	if callID != "" {
		l.decisions[callID] = true
	}
	return true, cur, l.maxPerTurn
}

// Reset 清零指定 turnID 的计数（每轮新消息开始时调用）。
// 同时清理该 turnID 关联的 callID 决策无法精确定位，故仅清计数；
// decisions 跨轮累积的内存由调用方按需 Reset 全量或依赖 GC（callID 量级有限）。
func (l *TurnToolCallLimiter) Reset(turnID string) {
	if l == nil {
		return
	}
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.counts, turnID)
}

// ResetAll 清空所有 turnID 计数与决策（如配置热重载时）。
func (l *TurnToolCallLimiter) ResetAll() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.counts = make(map[string]int)
	l.decisions = make(map[string]bool)
	l.dropped = 0
}

// Dropped 返回累计被拦截的工具调用数（观测用）。
func (l *TurnToolCallLimiter) Dropped() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.dropped
}

// EnsureUniqueToolCallID 在已知 callID 集合存在时返回一个不重复的新 callID。
// 移植自 strix config.tool_call_ids.new_call_id / dedupe_history_call_ids 思想：
// 某些 provider 每轮重置计数器（exec_command:0, exec_command:1...），同一 id
// 在对话历史里重复出现会让严格 provider 拒整轮，永久卡死 agent。本 helper
// 用于在生成 fallback id 时避免与已有集合碰撞。
//
// existing 可为 nil/空；返回形如 "call_<uuidhex>" 的唯一 id。
func EnsureUniqueToolCallID(existing []string) string {
	used := make(map[string]struct{}, len(existing)+1)
	for _, id := range existing {
		id = strings.TrimSpace(id)
		if id != "" {
			used[id] = struct{}{}
		}
	}
	// 最多重试 8 次，uuid 碰撞概率极低；仍碰撞则兜底加时间戳。
	for i := 0; i < 8; i++ {
		candidate := "call_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		if _, ok := used[candidate]; !ok {
			return candidate
		}
	}
	// 兜底：uuid 连续碰撞 8 次（理论上不可能），加纳秒时间戳确保唯一。
	return "call_" + strings.ReplaceAll(uuid.NewString(), "-", "") + "_" + itoa(int(time.Now().UnixNano()))
}

// itoa 轻量整数→字符串，避免引入 strconv 以减少依赖（本文件已 import uuid）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
