package multiagent

import (
	"context"
	"fmt"
	"strings"

	"cyberstrike-ai/internal/metrics"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// turnToolCallLimiterMiddleware 把 TurnToolCallLimiter 挂为 Eino compose.ToolMiddleware。
// 在工具调用前 CheckAndIncrement；超限返回合成工具结果而非真实执行，
// 让模型在下一轮重新决策（移植自 strix TurnToolCallLimiter.allow 思想）。
//
// turnID 策略：Eino 无原生 turnID，用 conversationID 充当 key。
// 每轮新消息由调用方 Reset；此处用 ctx 中的 conversationID（由
// mcp.WithMCPConversationID 注入）。若 ctx 无 conversationID 则
// 退化为 "global"，仍能对整轮限流（粒度变粗，但不会误放行）。
//
// 备注：本中间件不阻断：超限时返回合成 ToolOutput（soft error）而非 hard error，
// 与 softRecoveryToolCallMiddleware 的"把错误转为工具结果"语义一致。
type turnToolCallLimiterMiddleware struct {
	limiter *TurnToolCallLimiter
	logger  *zap.Logger
}

// newTurnToolCallLimiterMiddleware 构造限流中间件。limiter 为 nil 或未启用时
// 返回空 ToolMiddleware（调用方据此跳过挂载）。
func newTurnToolCallLimiterMiddleware(limiter *TurnToolCallLimiter, logger *zap.Logger) compose.ToolMiddleware {
	if limiter == nil || !limiter.Enabled() {
		return compose.ToolMiddleware{}
	}
	m := &turnToolCallLimiterMiddleware{limiter: limiter, logger: logger}
	return compose.ToolMiddleware{
		Invokable:  m.invokable(),
		Streamable: m.streamable(),
	}
}

// turnIDFromContext 从 ctx 提取 turnID。优先用 MCP conversationID
// （由 handler 在 taskCtx 上注入）；缺失时退化为 "global"。
func turnIDFromContext(ctx context.Context) string {
	if cid := strings.TrimSpace(mcpConversationIDFromContextEino(ctx)); cid != "" {
		return cid
	}
	return "global"
}

// blockedToolResult 构造超限时的合成工具结果。双语提示模型结束本轮。
func blockedToolResult(toolName string, current, limit int) string {
	return fmt.Sprintf(
		"[Turn Tool Call Limit] Tool '%s' was not executed: this turn has reached the tool call limit (%d/%d).\n"+
			"Please summarize current progress and end this turn; you may resume tool calls in the next turn.\n\n"+
			"[单轮工具调用上限] 工具 '%s' 未执行：本轮工具调用已达上限（%d/%d）。\n"+
			"请总结当前进展并结束本轮；可在下一轮继续工具调用。",
		toolName, current, limit,
		toolName, current, limit,
	)
}

func (m *turnToolCallLimiterMiddleware) invokable() compose.InvokableToolMiddleware {
	return func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
		return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
			if m == nil || m.limiter == nil || !m.limiter.Enabled() || input == nil {
				return next(ctx, input)
			}
			turnID := turnIDFromContext(ctx)
			callID := input.CallID
			allowed, current, limit := m.limiter.CheckAndIncrement(turnID, callID)
			if allowed {
				return next(ctx, input)
			}
			metrics.RecordTurnToolCallDropped()
			if m.logger != nil {
				m.logger.Warn("turn 工具调用限流：超限拦截",
					zap.String("toolName", input.Name),
					zap.String("turnID", turnID),
					zap.Int("current", current),
					zap.Int("limit", limit),
				)
			}
			return &compose.ToolOutput{Result: blockedToolResult(input.Name, current, limit)}, nil
		}
	}
}

func (m *turnToolCallLimiterMiddleware) streamable() compose.StreamableToolMiddleware {
	return func(next compose.StreamableToolEndpoint) compose.StreamableToolEndpoint {
		return func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
			if m == nil || m.limiter == nil || !m.limiter.Enabled() || input == nil {
				return next(ctx, input)
			}
			turnID := turnIDFromContext(ctx)
			callID := input.CallID
			allowed, current, limit := m.limiter.CheckAndIncrement(turnID, callID)
			if allowed {
				return next(ctx, input)
			}
			metrics.RecordTurnToolCallDropped()
			if m.logger != nil {
				m.logger.Warn("turn 工具调用限流：超限拦截（流式）",
					zap.String("toolName", input.Name),
					zap.String("turnID", turnID),
					zap.Int("current", current),
					zap.Int("limit", limit),
				)
			}
			return &compose.StreamToolOutput{
				Result: schema.StreamReaderFromArray([]string{blockedToolResult(input.Name, current, limit)}),
			}, nil
		}
	}
}

// turnToolCallLimiterReset 用 conversationID 作 Reset key。
// 每轮新消息开始时由 run loop 调用以清零计数。
// 兼容 adk.Runner 与 turn loop 两种 runner：在 messages 入口处 Reset。
//
// 注：本函数在 run loop 调用，不在中间件链内。见 eino_adk_run_loop.go 接入。
func turnToolCallLimiterReset(limiter *TurnToolCallLimiter, conversationID string) {
	if limiter == nil {
		return
	}
	limiter.Reset(conversationID)
}
