package multiagent

import (
	"context"

	"cyberstrike-ai/internal/mcp"

	"github.com/cloudwego/eino/compose"
	"go.uber.org/zap"
)

// mcpConversationIDFromContextEino 桥接到 mcp.MCPConversationIDFromContext。
// 独立小函数便于限流中间件文件保持自洽，且单点替换便于测试 mock。
func mcpConversationIDFromContextEino(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	return mcp.MCPConversationIDFromContext(ctx)
}

// einoTurnLimiterMiddlewares 返回限流中间件切片。limiter 未启用时返回空切片，
// 便于调用方用 ... 展开零开销挂载。
// 中间件顺序：放在 hitl 之后、softRecovery 之前——HITL 审批优先（人审可拒绝），
// 限流作为审批通过后的"卡死防线"，softRecovery 兜底其余错误。
func einoTurnLimiterMiddlewares(limiter *TurnToolCallLimiter, logger *zap.Logger) []compose.ToolMiddleware {
	if limiter == nil || !limiter.Enabled() {
		return nil
	}
	return []compose.ToolMiddleware{newTurnToolCallLimiterMiddleware(limiter, logger)}
}

