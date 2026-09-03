package multiagent

import (
	"context"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

type einoAssistantStreamEventHandlerConfig struct {
	Context                   context.Context
	ConversationID            string
	OrchMode                  string
	Progress                  func(eventType, message string, data interface{})
	Logger                    *zap.Logger
	SnapshotMCPIDs            func() []string
	StreamsMainAssistant      func(agent string) bool
	EinoRoleTag               func(agent string) string
	RunProgress               *einoRunProgressTracker
	StdoutSuppressor          *einoExecuteStdoutSuppressor
	AssistantOutput           *einoAssistantOutputAccumulator
	RunMessages               *einoRunMessageAccumulator
	Usage                     *einoRunUsageAccumulator
	ToolCallCompletion        *einoStreamToolCallCompletionHandler
	NextMainStreamID          func() string
	NextReasoningStreamID     func() string
	NextSubAgentReplyStreamID func() string
	// StuckDetector K9/P2-2：流式完成观测。nil=未启用（no-op，向后兼容）。
	// 主代理流式输出完成时调 ObserveStreamComplete(content, toolCalls)，
	// 让 sameOutputRepeat/monologue 在流式场景下当轮生效（不再等物质化消息晚一轮）。
	StuckDetector StuckDetectorAdapter
}

type einoAssistantStreamEventHandler struct {
	ctx                       context.Context
	conversationID            string
	orchMode                  string
	progress                  func(eventType, message string, data interface{})
	logger                    *zap.Logger
	snapshotMCPIDs            func() []string
	streamsMainAssistant      func(agent string) bool
	einoRoleTag               func(agent string) string
	runProgress               *einoRunProgressTracker
	stdoutSuppressor          *einoExecuteStdoutSuppressor
	assistantOutput           *einoAssistantOutputAccumulator
	runMessages               *einoRunMessageAccumulator
	usage                     *einoRunUsageAccumulator
	toolCallCompletion        *einoStreamToolCallCompletionHandler
	nextMainStreamID          func() string
	nextReasoningStreamID     func() string
	nextSubAgentReplyStreamID func() string
	stuckDetector             StuckDetectorAdapter
}

func newEinoAssistantStreamEventHandler(cfg einoAssistantStreamEventHandlerConfig) *einoAssistantStreamEventHandler {
	if cfg.Context == nil {
		cfg.Context = context.Background()
	}
	if cfg.SnapshotMCPIDs == nil {
		cfg.SnapshotMCPIDs = func() []string { return nil }
	}
	if cfg.StreamsMainAssistant == nil {
		cfg.StreamsMainAssistant = func(string) bool { return true }
	}
	if cfg.EinoRoleTag == nil {
		cfg.EinoRoleTag = func(string) string { return "" }
	}
	if cfg.NextMainStreamID == nil {
		cfg.NextMainStreamID = func() string { return "eino-main" }
	}
	if cfg.NextReasoningStreamID == nil {
		cfg.NextReasoningStreamID = func() string { return "eino-reasoning" }
	}
	if cfg.NextSubAgentReplyStreamID == nil {
		cfg.NextSubAgentReplyStreamID = func() string { return "eino-sub-reply" }
	}
	return &einoAssistantStreamEventHandler{
		ctx:                       cfg.Context,
		conversationID:            cfg.ConversationID,
		orchMode:                  cfg.OrchMode,
		progress:                  cfg.Progress,
		logger:                    cfg.Logger,
		snapshotMCPIDs:            cfg.SnapshotMCPIDs,
		streamsMainAssistant:      cfg.StreamsMainAssistant,
		einoRoleTag:               cfg.EinoRoleTag,
		runProgress:               cfg.RunProgress,
		stdoutSuppressor:          cfg.StdoutSuppressor,
		assistantOutput:           cfg.AssistantOutput,
		runMessages:               cfg.RunMessages,
		usage:                     cfg.Usage,
		toolCallCompletion:        cfg.ToolCallCompletion,
		nextMainStreamID:          cfg.NextMainStreamID,
		nextReasoningStreamID:     cfg.NextReasoningStreamID,
		nextSubAgentReplyStreamID: cfg.NextSubAgentReplyStreamID,
		stuckDetector:             cfg.StuckDetector,
	}
}

func (h *einoAssistantStreamEventHandler) Handle(mv *adk.MessageVariant, agentName string) (handled bool, recvErr error) {
	if h == nil || mv == nil || !mv.IsStreaming || mv.MessageStream == nil || mv.Role == schema.Tool {
		return false, nil
	}
	mainStreamID := h.nextMainStreamID()
	mainEmitter := newEinoMainResponseStreamEmitter(
		h.conversationID, h.orchMode, agentName, mainStreamID, h.mainIteration(agentName), h.progress, h.snapshotMCPIDs,
	)
	reasoningEmitter := newEinoReasoningStreamEmitter(
		h.conversationID,
		h.orchMode,
		agentName,
		h.einoRoleTag(agentName),
		h.progress,
		h.nextReasoningStreamID,
	)
	var toolStreamFragments []schema.ToolCall
	var streamUsage *schema.TokenUsage
	subReplyEmitter := newEinoSubAgentReplyEmitter(
		h.conversationID,
		agentName,
		h.progress,
		h.nextSubAgentReplyStreamID,
	)
	mainAssistantStream := newEinoMainAssistantStreamHandler(einoMainAssistantStreamHandlerConfig{
		AgentName:        agentName,
		Emitter:          mainEmitter,
		StdoutSuppressor: h.stdoutSuppressor,
		AssistantOutput:  h.assistantOutput,
		RunMessages:      h.runMessages,
	})
	recvErr = recvEinoSchemaMessageStreamWithContext(h.ctx, mv.MessageStream, 8, func(chunk *schema.Message) {
		reasoningEmitter.EmitDelta(chunk.ReasoningContent)
		if chunk.Content != "" {
			if h.streamsMainAssistant(agentName) {
				mainAssistantStream.EmitDelta(chunk.Content)
			} else if !h.streamsMainAssistant(agentName) {
				subReplyEmitter.EmitDelta(chunk.Content)
			}
		}
		if len(chunk.ToolCalls) > 0 {
			toolStreamFragments = append(toolStreamFragments, chunk.ToolCalls...)
		}
		if chunk.ResponseMeta != nil && chunk.ResponseMeta.Usage != nil {
			streamUsage = maxEinoTokenUsage(streamUsage, chunk.ResponseMeta.Usage)
		}
	})
	if recvErr != nil && !isEinoVoluntaryCancelErr(recvErr) && h.logger != nil {
		h.logger.Warn("eino stream recv error, flushing incomplete stream",
			zap.Error(recvErr),
			zap.String("agent", agentName),
			zap.Int("toolFragments", len(toolStreamFragments)))
	}
	reasoningEmitter.Finish()
	streamBody := ""
	if h.streamsMainAssistant(agentName) {
		streamBody = mainAssistantStream.Finish()
	}
	subReplyEmitter.Finish()
	if h.toolCallCompletion != nil {
		h.toolCallCompletion.Complete(toolStreamFragments, agentName)
	}
	if h.usage != nil {
		h.usage.AddUsage(streamUsage)
	}
	// K9/P2-2：主代理流式完成即观测（与 ObserveMaterialized 共用 StuckDetector 逻辑）。
	// run loop 的 ObserveMaterialized 仍保留（兜底非流式路径），二者共用 per-conversation
	// 计数：同一次输出两次观测会被 detector 计为连续两次相同输出（见接入说明）。
	// 注意：物质化消息与流式完成通常成对出现，detector 的 sameOutputRepeat 以
	// 观测次数为准——为避免双计导致阈值提前触发，此处仅在"流式事件"路径观测，
	// run loop 对已流式处理的轮次跳过 ObserveMaterialized（见 eino_adk_run_loop.go）。
	if h.stuckDetector != nil {
		if ev := h.stuckDetector.ObserveStreamComplete(streamBody, toolStreamFragments); ev != nil {
			PublishStuckEvent(ev)
		}
	}
	return true, recvErr
}

func (h *einoAssistantStreamEventHandler) mainIteration(agentName string) int {
	if h == nil || h.runProgress == nil {
		return 0
	}
	return h.runProgress.MainIteration(agentName)
}
