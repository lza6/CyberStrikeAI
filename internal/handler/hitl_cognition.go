package handler

import (
	"strings"
)

type hitlCognitionState struct {
	AssistantMessageID string
	UserMessage        string
	Thinking           string
	ReasoningChain     string
	Planning           string
	// ReasoningMode / ReasoningEffort P1：本轮 chat 请求的推理意图（ChatRequest.reasoning）。
	// HITL 中断时写入 payload["reasoning"]，审批页（非 chat 页）据此渲染「推理强度」。
	ReasoningMode   string
	ReasoningEffort string
}

// GetHitlCognition 返回当前运行任务上缓存的本轮 HITL 上下文（不含会话历史）。
func (m *AgentTaskManager) GetHitlCognition(conversationID string) hitlCognitionFields {
	conversationID = strings.TrimSpace(conversationID)
	if m == nil || conversationID == "" {
		return hitlCognitionFields{}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.tasks[conversationID]
	if !ok || t == nil || t.hitlCognition == nil {
		return hitlCognitionFields{}
	}
	c := t.hitlCognition
	return hitlCognitionFields{
		UserMessage:    c.UserMessage,
		Thinking:       c.Thinking,
		ReasoningChain: c.ReasoningChain,
		Planning:       c.Planning,
	}
}

// ResetHitlCognition 新任务开始时重置本轮 HITL 上下文。
func (m *AgentTaskManager) ResetHitlCognition(conversationID, userMessage string) {
	conversationID = strings.TrimSpace(conversationID)
	if m == nil || conversationID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[conversationID]
	if !ok || t == nil {
		return
	}
	t.hitlCognition = &hitlCognitionState{UserMessage: strings.TrimSpace(userMessage)}
}

// SetHitlAssistantMessageID 记录当前助手消息 ID，供 HITL 与 DB 回退对齐。
func (m *AgentTaskManager) SetHitlAssistantMessageID(conversationID, assistantMessageID string) {
	conversationID = strings.TrimSpace(conversationID)
	assistantMessageID = strings.TrimSpace(assistantMessageID)
	if m == nil || conversationID == "" || assistantMessageID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[conversationID]
	if !ok || t == nil {
		return
	}
	if t.hitlCognition == nil {
		t.hitlCognition = &hitlCognitionState{}
	}
	t.hitlCognition.AssistantMessageID = assistantMessageID
}

// SetHitlReasoningIntent 记录本轮请求的 reasoning 意图（ChatRequest.reasoning），供
// HITL 中断 payload 回放。mode/effort 均为空时不记录（与前端 buildReasoningRequestPayload
// 返回 undefined 的语义一致：无显式推理意图）。
func (m *AgentTaskManager) SetHitlReasoningIntent(conversationID, mode, effort string) {
	conversationID = strings.TrimSpace(conversationID)
	if m == nil || conversationID == "" {
		return
	}
	mode = strings.TrimSpace(mode)
	effort = strings.TrimSpace(effort)
	if mode == "" && effort == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[conversationID]
	if !ok || t == nil {
		return
	}
	if t.hitlCognition == nil {
		t.hitlCognition = &hitlCognitionState{}
	}
	t.hitlCognition.ReasoningMode = mode
	t.hitlCognition.ReasoningEffort = effort
}

// GetHitlReasoningIntent 返回当前任务记录的 reasoning 意图。ok=false 表示无记录。
func (m *AgentTaskManager) GetHitlReasoningIntent(conversationID string) (mode, effort string, ok bool) {
	conversationID = strings.TrimSpace(conversationID)
	if m == nil || conversationID == "" {
		return "", "", false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, exists := m.tasks[conversationID]
	if !exists || t == nil || t.hitlCognition == nil {
		return "", "", false
	}
	c := t.hitlCognition
	if c.ReasoningMode == "" && c.ReasoningEffort == "" {
		return "", "", false
	}
	return c.ReasoningMode, c.ReasoningEffort, true
}

// UpdateHitlCognitionSnapshot 从进行中的进度流快照更新 thinking / reasoning / planning。
func (m *AgentTaskManager) UpdateHitlCognitionSnapshot(conversationID, assistantMessageID, thinking, reasoningChain, planning string) {
	conversationID = strings.TrimSpace(conversationID)
	if m == nil || conversationID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[conversationID]
	if !ok || t == nil {
		return
	}
	if t.hitlCognition == nil {
		t.hitlCognition = &hitlCognitionState{}
	}
	if id := strings.TrimSpace(assistantMessageID); id != "" {
		t.hitlCognition.AssistantMessageID = id
	}
	if s := strings.TrimSpace(thinking); s != "" {
		t.hitlCognition.Thinking = s
	}
	if s := strings.TrimSpace(reasoningChain); s != "" {
		t.hitlCognition.ReasoningChain = s
	}
	if s := strings.TrimSpace(planning); s != "" {
		t.hitlCognition.Planning = s
	}
}
