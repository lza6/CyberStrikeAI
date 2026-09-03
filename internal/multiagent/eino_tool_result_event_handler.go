package multiagent

import (
	"context"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

type einoToolResultEventHandlerConfig struct {
	Context          context.Context
	Logger           *zap.Logger
	RunMessages      *einoRunMessageAccumulator
	Emitter          *einoToolResultProgressEmitter
	ConfirmRecovery  func()
	// K9：StuckDetector 适配器（工具错误观测）。nil=未启用（no-op）。
	// 物质化工具结果错误时调 ObserveToolError，触发 sameErrorRepeat 阈值时
	// 由 run loop 发布到 blackboard → reactions（agent-stuck 规则）。
	StuckDetector StuckDetectorAdapter
}

type einoToolResultEventHandler struct {
	ctx              context.Context
	logger           *zap.Logger
	runMessages      *einoRunMessageAccumulator
	emitter          *einoToolResultProgressEmitter
	confirmRecovery  func()
	stuckDetector    StuckDetectorAdapter
}

func newEinoToolResultEventHandler(cfg einoToolResultEventHandlerConfig) *einoToolResultEventHandler {
	if cfg.Context == nil {
		cfg.Context = context.Background()
	}
	return &einoToolResultEventHandler{
		ctx:             cfg.Context,
		logger:          cfg.Logger,
		runMessages:     cfg.RunMessages,
		emitter:         cfg.Emitter,
		confirmRecovery: cfg.ConfirmRecovery,
		stuckDetector:   cfg.StuckDetector,
	}
}

func (h *einoToolResultEventHandler) HandleStreaming(mv *adk.MessageVariant, agentName string) bool {
	if h == nil || mv == nil || !mv.IsStreaming || mv.MessageStream == nil || mv.Role != schema.Tool {
		return false
	}
	defaultName := strings.TrimSpace(mv.ToolName)
	msgs, recvErr := recvSchemaToolResultMessages(h.ctx, mv.MessageStream)
	if isEinoVoluntaryCancelErr(recvErr) && len(msgs) == 0 {
		msgs = []*schema.Message{schema.ToolMessage("已中断并继续，当前工具调用已停止。", "", schema.WithToolName(defaultName))}
	}
	if len(msgs) == 0 {
		msgs = []*schema.Message{schema.ToolMessage("", "", schema.WithToolName(defaultName))}
	}
	for _, msg := range msgs {
		if msg == nil {
			continue
		}
		toolName := strings.TrimSpace(msg.ToolName)
		if toolName == "" {
			toolName = defaultName
		}
		content := msg.Content
		if isEinoVoluntaryCancelErr(recvErr) && strings.TrimSpace(content) == "" {
			content = "已中断并继续，当前工具调用已停止。"
		}
		isErr := einoToolResultIsError(toolName, content) || isEinoVoluntaryCancelErr(recvErr)
		content = einoToolResultBody(content)
		toolCallID := strings.TrimSpace(msg.ToolCallID)
		if toolCallID != "" && h.runMessages != nil {
			h.runMessages.AppendToolMessage(content, toolCallID, schema.WithToolName(toolName))
		}
		if h.emitter != nil {
			h.emitter.Emit(h.ctx, toolName, content, toolCallID, isErr, agentName)
		}
		// K9：工具错误观测（sameErrorRepeat）。recon 白名单豁免由 detector 内部判定。
		if isErr && h.stuckDetector != nil {
			if ev := h.stuckDetector.ObserveToolError(toolName, content); ev != nil {
				PublishStuckEvent(ev)
			}
		}
		if recvErr != nil && !isEinoVoluntaryCancelErr(recvErr) && h.logger != nil {
			h.logger.Warn("eino tool result stream recv error",
				zap.Error(recvErr),
				zap.String("agent", agentName),
				zap.String("tool", toolName),
				zap.String("toolCallId", toolCallID))
		}
	}
	if recvErr == nil && h.confirmRecovery != nil {
		h.confirmRecovery()
	}
	return true
}

func (h *einoToolResultEventHandler) HandleMaterialized(mv *adk.MessageVariant, msg adk.Message, agentName string) bool {
	if h == nil || mv == nil || msg == nil || (mv.Role != schema.Tool && msg.Role != schema.Tool) {
		return false
	}
	toolName := msg.ToolName
	if toolName == "" {
		toolName = mv.ToolName
	}
	content := msg.Content
	isErr := einoToolResultIsError(toolName, content)
	content = einoToolResultBody(content)
	toolCallID := strings.TrimSpace(msg.ToolCallID)
	if h.emitter != nil {
		h.emitter.Emit(h.ctx, toolName, content, toolCallID, isErr, agentName)
	}
	// K9：物质化工具结果错误观测（sameErrorRepeat）。
	if isErr && h.stuckDetector != nil {
		if ev := h.stuckDetector.ObserveToolError(toolName, content); ev != nil {
			PublishStuckEvent(ev)
		}
	}
	return true
}
