package multiagent

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"cyberstrike-ai/internal/agent"
	"cyberstrike-ai/internal/config"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func chatToolCallsToSchema(tcs []agent.ToolCall) []schema.ToolCall {
	if len(tcs) == 0 {
		return nil
	}
	out := make([]schema.ToolCall, 0, len(tcs))
	for _, tc := range tcs {
		if strings.TrimSpace(tc.ID) == "" {
			continue
		}
		argsStr := ""
		if tc.Function.Arguments != nil {
			b, err := json.Marshal(tc.Function.Arguments)
			if err == nil {
				argsStr = string(b)
			}
		}
		// Some OpenAI-compatible gateways require `function.arguments` to exist
		// on every assistant tool_call message. When args are empty, omitempty may
		// drop the field during serialization and cause "missing field arguments"
		// on the next turn history replay.
		if strings.TrimSpace(argsStr) == "" {
			argsStr = "{}"
		}
		typ := tc.Type
		if typ == "" {
			typ = "function"
		}
		out = append(out, schema.ToolCall{
			ID:   tc.ID,
			Type: typ,
			Function: schema.FunctionCall{
				Name:      tc.Function.Name,
				Arguments: argsStr,
			},
		})
	}
	return out
}

// historyToMessages 将已保存的 model-facing 轨迹转为 Eino ADK 消息。
// 新轨迹应已是模型实际看到的内容；对旧版本遗留的超大 tool 正文再做一次上限规范化，
// 防止原始工具输出通过 last_react/checkpoint 绕过 reduction。
func historyToMessages(history []agent.ChatMessage, appCfg *config.Config, mwCfg *config.MultiAgentEinoMiddlewareConfig) []adk.Message {
	toolContentMax := config.MultiAgentEinoMiddlewareConfig{}.ReductionMaxLengthForTruncEffective()
	userContentMaxRunes := config.MultiAgentEinoMiddlewareConfig{}.LatestUserMessageMaxRunesEffective()
	if mwCfg != nil {
		toolContentMax = mwCfg.ReductionMaxLengthForTruncEffective()
		userContentMaxRunes = mwCfg.LatestUserMessageMaxRunesEffective()
	}
	if appCfg != nil {
		userContentMaxRunes = minPositiveInt(userContentMaxRunes, modelFacingRuneBudget(appCfg.OpenAI.MaxTotalTokens, 0.20))
	}
	if len(history) == 0 {
		return nil
	}
	raw := make([]adk.Message, 0, len(history))
	for _, h := range history {
		role := strings.ToLower(strings.TrimSpace(h.Role))
		switch role {
		case "user":
			if strings.TrimSpace(h.Content) != "" {
				content := h.Content
				if !h.ModelFacingTrace {
					content = normalizeRestoredUserContent(content, userContentMaxRunes)
				}
				raw = append(raw, schema.UserMessage(content))
			}
		case "assistant":
			toolSchema := chatToolCallsToSchema(h.ToolCalls)
			hasRC := strings.TrimSpace(h.ReasoningContent) != ""
			if len(toolSchema) > 0 || strings.TrimSpace(h.Content) != "" || hasRC {
				am := schema.AssistantMessage(h.Content, toolSchema)
				if hasRC {
					am.ReasoningContent = strings.TrimSpace(h.ReasoningContent)
				}
				raw = append(raw, am)
			}
		case "tool":
			if strings.TrimSpace(h.ToolCallID) == "" && strings.TrimSpace(h.Content) == "" {
				continue
			}
			var opts []schema.ToolMessageOption
			if tn := strings.TrimSpace(h.ToolName); tn != "" {
				opts = append(opts, schema.WithToolName(tn))
			}
			content := h.Content
			if !h.ModelFacingTrace || (toolContentMax > 0 && len(content) > toolContentMax) {
				content = normalizeRestoredToolContent(content, toolContentMax)
			}
			raw = append(raw, schema.ToolMessage(content, h.ToolCallID, opts...))
		default:
			continue
		}
	}
	return raw
}

func normalizeRestoredUserContent(content string, maxRunes int) string {
	if maxRunes <= 0 || utf8.RuneCountInString(content) <= maxRunes {
		return content
	}
	runes := []rune(content)
	const marker = "\n\n...[historical user input normalized to the model-facing budget]...\n\n"
	markerRunes := []rune(marker)
	budget := maxRunes - len(markerRunes)
	if budget <= 0 {
		end := maxRunes
		if end > len(markerRunes) {
			end = len(markerRunes)
		}
		return string(markerRunes[:end])
	}
	head := budget / 2
	tail := budget - head
	return string(runes[:head]) + marker + string(runes[len(runes)-tail:])
}

func normalizeRestoredToolContent(content string, maxBytes int) string {
	if maxBytes <= 0 || len(content) <= maxBytes {
		return content
	}
	const marker = "\n\n...[legacy tool output discarded during model-facing history migration]...\n\n"
	budget := maxBytes - len(marker)
	if budget <= 0 {
		return marker
	}
	head := budget / 2
	tail := budget - head
	for head > 0 && !utf8.RuneStart(content[head]) {
		head--
	}
	tailStart := len(content) - tail
	for tailStart < len(content) && !utf8.RuneStart(content[tailStart]) {
		tailStart++
	}
	return content[:head] + marker + content[tailStart:]
}

// mergeStreamingToolCallFragments 将流式多帧的 ToolCall 按 index 合并 arguments（与 schema.concatToolCalls 行为一致）。
func mergeStreamingToolCallFragments(fragments []schema.ToolCall) []schema.ToolCall {
	if len(fragments) == 0 {
		return nil
	}
	m, err := schema.ConcatMessages([]*schema.Message{{ToolCalls: fragments}})
	if err != nil || m == nil {
		return fragments
	}
	return m.ToolCalls
}

// mergeMessageToolCalls 非流式路径上若仍带分片式 tool_calls，合并后再上报 UI。
func mergeMessageToolCalls(msg *schema.Message) *schema.Message {
	if msg == nil || len(msg.ToolCalls) == 0 {
		return msg
	}
	m, err := schema.ConcatMessages([]*schema.Message{msg})
	if err != nil || m == nil {
		return msg
	}
	out := *msg
	out.ToolCalls = m.ToolCalls
	return &out
}

// toolCallStableID 用于流式阶段去重；OpenAI 流式常先给 index 后补 id。
func toolCallStableID(tc schema.ToolCall) string {
	if tc.ID != "" {
		return tc.ID
	}
	if tc.Index != nil {
		return fmt.Sprintf("idx:%d", *tc.Index)
	}
	return ""
}

// toolCallDisplayName returns the visible tool name once the model stream has
// produced a concrete function name. Anonymous stream fragments are filtered
// before progress emission instead of being guessed as task calls.
func toolCallDisplayName(tc schema.ToolCall) string {
	if n := strings.TrimSpace(tc.Function.Name); n != "" {
		return n
	}
	if n := strings.TrimSpace(tc.Type); n != "" && !strings.EqualFold(n, "function") {
		return n
	}
	return ""
}

// toolCallsSignatureFlush 用于去重键；无 id/index 时用占位 pos，避免流末帧缺 id 时整条工具事件丢失。
func toolCallsSignatureFlush(msg *schema.Message) string {
	if msg == nil || len(msg.ToolCalls) == 0 {
		return ""
	}
	visible := filterVisibleToolCallsForProgress(msg.ToolCalls)
	if len(visible) == 0 {
		return ""
	}
	parts := make([]string, 0, len(visible))
	for i, tc := range visible {
		id := toolCallStableID(tc)
		if id == "" {
			id = fmt.Sprintf("pos:%d", i)
		}
		name := toolCallDisplayName(tc)
		if name == "" {
			continue
		}
		parts = append(parts, id+"|"+name)
	}
	if len(parts) == 0 {
		return ""
	}
	sort.Strings(parts)
	return strings.Join(parts, ";")
}

// toolCallsRichSignature 用于去重：同一次流式已上报后，紧随其后的非流式消息常带相同 tool_calls。
func toolCallsRichSignature(msg *schema.Message) string {
	base := toolCallsSignatureFlush(msg)
	if base == "" {
		return ""
	}
	visible := filterVisibleToolCallsForProgress(msg.ToolCalls)
	parts := make([]string, 0, len(visible))
	for _, tc := range visible {
		id := toolCallStableID(tc)
		arg := tc.Function.Arguments
		if len(arg) > 240 {
			arg = arg[:240]
		}
		parts = append(parts, id+":"+arg)
	}
	sort.Strings(parts)
	return base + "|" + strings.Join(parts, ";")
}

func einoMainIterationKey(agentName, orchestratorName string) string {
	key := strings.TrimSpace(agentName)
	if key == "" {
		key = strings.TrimSpace(orchestratorName)
	}
	if key == "" {
		return "_main"
	}
	return key
}

func tryEmitToolCallsOnce(
	msg *schema.Message,
	agentName, orchestratorName, conversationID, orchMode string,
	progress func(string, string, interface{}),
	seen map[string]struct{},
	subAgentToolStep, mainAgentToolStep map[string]int,
	markPending func(toolCallPendingInfo),
) {
	if msg == nil || len(msg.ToolCalls) == 0 || progress == nil || seen == nil {
		return
	}
	if toolCallsSignatureFlush(msg) == "" {
		return
	}
	sig := agentName + "\x1e" + toolCallsRichSignature(msg)
	if _, ok := seen[sig]; ok {
		return
	}
	if idSig := toolCallsStableIDSignature(msg); idSig != "" {
		idKey := agentName + "\x1eids\x1e" + idSig
		if _, ok := seen[idKey]; ok {
			return
		}
		seen[idKey] = struct{}{}
	}
	seen[sig] = struct{}{}
	emitToolCallsFromMessage(msg, agentName, orchestratorName, conversationID, orchMode, progress, subAgentToolStep, mainAgentToolStep, markPending)
}

func toolCallsStableIDSignature(msg *schema.Message) string {
	if msg == nil || len(msg.ToolCalls) == 0 {
		return ""
	}
	visible := filterVisibleToolCallsForProgress(msg.ToolCalls)
	ids := make([]string, 0, len(visible))
	for _, tc := range visible {
		id := strings.TrimSpace(tc.ID)
		if id == "" {
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return ""
	}
	sort.Strings(ids)
	return strings.Join(ids, ";")
}

func emitToolCallsFromMessage(
	msg *schema.Message,
	agentName, orchestratorName, conversationID, orchMode string,
	progress func(string, string, interface{}),
	subAgentToolStep, mainAgentToolStep map[string]int,
	markPending func(toolCallPendingInfo),
) {
	if msg == nil || len(msg.ToolCalls) == 0 || progress == nil {
		return
	}
	visibleToolCalls := filterVisibleToolCallsForProgress(msg.ToolCalls)
	if len(visibleToolCalls) == 0 {
		return
	}
	if subAgentToolStep == nil {
		subAgentToolStep = make(map[string]int)
	}
	isSubToolRound := agentName != "" && agentName != orchestratorName
	if isSubToolRound {
		subAgentToolStep[agentName]++
		n := subAgentToolStep[agentName]
		progress("iteration", "", map[string]interface{}{
			"iteration":      n,
			"einoScope":      "sub",
			"einoRole":       "sub",
			"einoAgent":      agentName,
			"conversationId": conversationID,
			"source":         "eino",
		})
	} else if mainAgentToolStep != nil {
		key := einoMainIterationKey(agentName, orchestratorName)
		mainAgentToolStep[key]++
		n := mainAgentToolStep[key]
		// 第 1 轮已在主代理进入时发出；此后每次工具批次对应新一轮 ReAct（与子代理按工具计步一致）。
		if n > 1 {
			progress("iteration", "", map[string]interface{}{
				"iteration":      n,
				"einoScope":      "main",
				"einoRole":       "orchestrator",
				"einoAgent":      agentName,
				"orchestration":  orchMode,
				"conversationId": conversationID,
				"source":         "eino",
			})
		}
	}
	role := "orchestrator"
	if isSubToolRound {
		role = "sub"
	}
	progress("tool_calls_detected", fmt.Sprintf("检测到 %d 个工具调用", len(visibleToolCalls)), map[string]interface{}{
		"count":          len(visibleToolCalls),
		"conversationId": conversationID,
		"source":         "eino",
		"einoAgent":      agentName,
		"einoRole":       role,
	})
	for idx, tc := range visibleToolCalls {
		argStr := strings.TrimSpace(tc.Function.Arguments)
		if argStr == "" && len(tc.Extra) > 0 {
			if b, mErr := json.Marshal(tc.Extra); mErr == nil {
				argStr = string(b)
			}
		}
		var argsObj map[string]interface{}
		if argStr != "" {
			if uErr := json.Unmarshal([]byte(argStr), &argsObj); uErr != nil || argsObj == nil {
				argsObj = map[string]interface{}{"_raw": argStr}
			}
		}
		display := toolCallDisplayName(tc)
		toolCallID := tc.ID
		if toolCallID == "" && tc.Index != nil {
			// Stream indexes restart from zero for every model turn. Include a
			// process-wide sequence so pending/result de-duplication cannot collide
			// with an earlier batch in the same agent run.
			toolCallID = fmt.Sprintf("eino-stream-%d-%d", fallbackToolCallSequence.Add(1), *tc.Index)
		}
		// Record visible pending tool calls for later tool_result correlation / recovery flushing.
		if markPending != nil && toolCallID != "" {
			markPending(toolCallPendingInfo{
				ToolCallID: toolCallID,
				ToolName:   display,
				Arguments:  argsObj,
				EinoAgent:  agentName,
				EinoRole:   role,
			})
		}
		progress("tool_call", fmt.Sprintf("正在调用工具: %s", display), map[string]interface{}{
			"toolName":       display,
			"arguments":      argStr,
			"argumentsObj":   argsObj,
			"toolCallId":     toolCallID,
			"index":          idx + 1,
			"total":          len(visibleToolCalls),
			"conversationId": conversationID,
			"source":         "eino",
			"einoAgent":      agentName,
			"einoRole":       role,
		})
	}
}

func filterVisibleToolCallsForProgress(calls []schema.ToolCall) []schema.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]schema.ToolCall, 0, len(calls))
	for _, tc := range calls {
		if _, ok := modelOutputRecoveryFromToolCall(tc); ok {
			continue
		}
		if toolCallDisplayName(tc) == "" {
			continue
		}
		out = append(out, tc)
	}
	return out
}

// dedupeRepeatedParagraphs 去掉完全相同的连续/重复段落，缓解多代理各自复述同一列表。
