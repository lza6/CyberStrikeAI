// Package multiagent 使用 CloudWeGo Eino adk/prebuilt（deep / plan_execute / supervisor）编排多代理，MCP 工具经 einomcp 桥接到现有 Agent。
package multiagent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"cyberstrike-ai/internal/agent"
	"cyberstrike-ai/internal/agents"
	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/einomcp"
	"cyberstrike-ai/internal/project"
	"cyberstrike-ai/internal/reasoning"
	"cyberstrike-ai/internal/security"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/adk/prebuilt/supervisor"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// RunResult 与单 Agent 循环结果字段对齐，便于复用存储与 SSE 收尾逻辑。
type RunResult struct {
	Response             string
	MCPExecutionIDs      []string
	LastAgentTraceInput  string // 已序列化的消息带（JSON）：原生循环或 Eino 均写入，供续跑/攻击链等恢复上下文
	LastAgentTraceOutput string // 本轮助手侧对外展示文本（摘要或最终回复）
	Finalized            bool
	Status               string
	CompletionReason     string
	EvidenceVerified     bool
	EvidenceRefs         []string
	PendingExecutionIDs  []string
	MissingChecks        []string
}

// toolCallPendingInfo tracks a tool_call emitted to the UI so we can later
// correlate tool_result events (even when the framework omits ToolCallID) and
// avoid leaving the UI stuck in "running" state on recoverable errors.
type toolCallPendingInfo struct {
	ToolCallID string
	ToolName   string
	Arguments  map[string]interface{}
	EinoAgent  string
	EinoRole   string
}

var fallbackToolCallSequence atomic.Uint64

// RunDeepAgent 使用 Eino 多代理预置编排执行一轮对话（deep / plan_execute / supervisor；流式事件通过 progress 回调输出）。
// orchestrationOverride 非空时优先（如聊天/WebShell 请求体）；否则用 multi_agent.orchestration（遗留 yaml）；皆空则按 deep。
// reasoningClient 来自 ChatRequest.reasoning；可为 nil（机器人/批量等走全局 openai.reasoning）。
func RunDeepAgent(
	ctx context.Context,
	appCfg *config.Config,
	ma *config.MultiAgentConfig,
	ag *agent.Agent,
	db *database.DB,
	logger *zap.Logger,
	conversationID string,
	projectID string,
	userMessage string,
	history []agent.ChatMessage,
	roleTools []string,
	progress func(eventType, message string, data interface{}),
	agentsMarkdownDir string,
	orchestrationOverride string,
	reasoningClient *reasoning.ClientIntent,
	systemPromptExtra string,
) (*RunResult, error) {
	if appCfg == nil || ma == nil || ag == nil {
		return nil, fmt.Errorf("multiagent: 配置或 Agent 为空")
	}

	runtimeUserMessage := prepareLatestUserMessageForModel(userMessage, appCfg, &ma.EinoMiddleware, conversationID, logger)

	// J4：构建 execute 授权范围闸（Eino 内置 execute 工具专用，与 executor project scope 同源）。
	// 仅 project 绑定且声明 scope_json 时生效；其余情况 CheckExecute 直接放行（向后兼容）。
	executeScopeGuard := newExecuteScopeGuard(db, projectID, logger)

	effectiveSubs := ma.SubAgents
	var markdownLoad *agents.MarkdownDirLoad
	var orch *agents.OrchestratorMarkdown
	if strings.TrimSpace(agentsMarkdownDir) != "" {
		load, merr := agents.LoadMarkdownAgentsDir(agentsMarkdownDir)
		if merr != nil {
			if logger != nil {
				logger.Warn("加载 agents 目录 Markdown 失败，沿用 config 中的 sub_agents", zap.Error(merr))
			}
		} else {
			markdownLoad = load
			effectiveSubs = agents.MergeYAMLAndMarkdown(ma.SubAgents, load.SubAgents)
			orch = load.Orchestrator
		}
	}
	orchMode := config.NormalizeMultiAgentOrchestration(ma.Orchestration)
	if o := strings.TrimSpace(orchestrationOverride); o != "" {
		orchMode = config.NormalizeMultiAgentOrchestration(o)
	}
	if orchMode != "plan_execute" && ma.WithoutGeneralSubAgent && len(effectiveSubs) == 0 {
		return nil, fmt.Errorf("multi_agent.without_general_sub_agent 为 true 时，必须在 multi_agent.sub_agents 或 agents 目录 Markdown 中配置至少一个子代理")
	}
	if orchMode == "supervisor" && len(effectiveSubs) == 0 {
		return nil, fmt.Errorf("multi_agent.orchestration=supervisor 时需至少配置一个子代理（sub_agents 或 agents 目录 Markdown）")
	}
	// J6/K7 审计修复（MEDIUM）：coordinator 模式 worker 由 effectiveSubs 按需构造，
	// 无子代理时分解结果无 assignee 可用，错误会延迟到运行中段（LoadSpecs "no task specs"）
	// 才暴露。与 supervisor 同等前置校验，快速失败。
	if orchMode == "coordinator" && len(effectiveSubs) == 0 {
		return nil, fmt.Errorf("multi_agent.orchestration=coordinator 时需至少配置一个子代理（sub_agents 或 agents 目录 Markdown）")
	}
	if orchMode == "supervisor" && len(effectiveSubs) == 1 && progress != nil {
		progress("progress", "Supervisor 是专家路由模式；当前仅 1 个子代理，专家路由空间有限，仍会继续执行。", map[string]interface{}{
			"conversationId": conversationID,
			"source":         "eino",
			"orchestration":  orchMode,
			"kind":           "supervisor_boundary_hint",
		})
	}

	agenticLoc, agenticSkillMW, agenticFSTools, agenticSkillsRoot, einoErr := prepareEinoAgenticSkills(ctx, appCfg.SkillsDir, ma, logger)
	if einoErr != nil {
		return nil, einoErr
	}

	holder := &einomcp.ConversationHolder{}
	holder.Set(conversationID)

	var mcpIDsMu sync.Mutex
	var mcpIDs []string
	mcpExecBinder := NewMCPExecutionBinder()

	// J7：单轮工具调用限流器。limit>0 时启用，挂为 ToolsNode 中间件并在每轮
	// 新消息入口 Reset。移植自 strix TurnToolCallLimiter：防退化生成排队大量
	// 工具调用卡死 agent。limit<=0（含负数显式关闭）时不创建。
	turnToolCallLimiter := NewTurnToolCallLimiter(ma.TurnToolCallLimitEffective())
	recorder := func(id, toolCallID string) {
		if id == "" {
			return
		}
		mcpExecBinder.Bind(toolCallID, id)
		mcpIDsMu.Lock()
		mcpIDs = append(mcpIDs, id)
		mcpIDsMu.Unlock()
	}
	einoExecBegin, einoExecAppendPartial, einoExecRegisterCancel, einoExecUnregisterCancel, einoExecFinish := newEinoExecuteMonitorCallbacks(ctx, ag, recorder)

	// 与单代理流式一致：在 response_start / response_delta 的 data 中带当前 mcpExecutionIds，供主聊天绑定复制与展示。
	snapshotMCPIDs := func() []string {
		mcpIDsMu.Lock()
		defer mcpIDsMu.Unlock()
		out := make([]string, len(mcpIDs))
		copy(out, mcpIDs)
		return out
	}

	toolInvokeNotify := einomcp.NewToolInvokeNotifyHolder()
	mainDefs := ag.ToolsForRole(roleTools)

	baseHTTPClient := newEinoBaseHTTPClient()
	modelFactory := newEinoToolCallingChatModelFactory(baseHTTPClient, reasoningClient, logger)
	agenticModelFactory := newEinoAgenticChatModelFactory(baseHTTPClient, reasoningClient, logger)
	agenticModelRetryCfg := newEinoAgenticModelRetryConfig(&ma.EinoMiddleware, logger, "multiagent")
	agenticModelFailoverCfg, err := newEinoAgenticModelFailoverConfig(ctx, appCfg, &ma.EinoMiddleware, einoModelModeNormal, agenticModelFactory, logger, "multiagent", progress, orchMode, conversationID)
	if err != nil {
		return nil, err
	}
	logEinoAgenticModelGate(
		logger,
		"multiagent",
		orchMode,
		evaluateEinoAgenticModelGate(agenticModelGateFactory(agenticModelFactory, appCfg.OpenAI, einoModelModeNormal), einoAgenticRuntimeSupportV0914()),
	)

	deepMaxIter := agentMaxIterations(appCfg)

	var subAgents []adk.TypedAgent[*schema.AgenticMessage]
	var supervisorSubAgents []adk.Agent
	if orchMode != "plan_execute" {
		subAgents = make([]adk.TypedAgent[*schema.AgenticMessage], 0, len(effectiveSubs))
		supervisorSubAgents = make([]adk.Agent, 0, len(effectiveSubs))
		for _, sub := range effectiveSubs {
			id := strings.TrimSpace(sub.ID)
			if id == "" {
				return nil, fmt.Errorf("multi_agent.sub_agents 中存在空的 id")
			}
			name := strings.TrimSpace(sub.Name)
			if name == "" {
				name = id
			}
			desc := strings.TrimSpace(sub.Description)
			if desc == "" {
				desc = fmt.Sprintf("Specialist agent %s for penetration testing workflow.", id)
			}
			instr := strings.TrimSpace(sub.Instruction)
			if instr == "" {
				instr = "你是 CyberStrikeAI 中的专业子代理，在授权渗透测试场景下协助完成用户委托的子任务。优先使用可用工具获取证据，回答简洁专业。"
			}

			roleTools := sub.RoleTools
			bind := strings.TrimSpace(sub.BindRole)
			if bind != "" && appCfg.Roles != nil {
				if r, ok := appCfg.Roles[bind]; ok && r.Enabled {
					if len(roleTools) == 0 && len(r.Tools) > 0 {
						roleTools = r.Tools
					}
				}
			}

			subModel, err := agenticModelFactory(ctx, appCfg.OpenAI, einoModelModeNormal)
			if err != nil {
				return nil, fmt.Errorf("子代理 %q AgenticModel: %w", id, err)
			}

			subDefs := ag.ToolsForRole(roleTools)
			subTools, err := einomcp.ToolsFromDefinitions(ag, holder, subDefs, recorder, nil, toolInvokeNotify, id)
			if err != nil {
				return nil, fmt.Errorf("子代理 %q 工具: %w", id, err)
			}

			subToolsForCfg, subPre, subToolSearchActive, err := prependEinoAgenticMiddlewares(ctx, &ma.EinoMiddleware, einoMWSub, subTools, agenticLoc, agenticSkillsRoot, conversationID, projectID, logger)
			if err != nil {
				return nil, fmt.Errorf("子代理 %q eino 中间件: %w", id, err)
			}

			subMax := resolveMaxIterations(appCfg, sub.MaxIterations)

			subSumMw, err := newEinoAgenticSummarizationMiddleware(ctx, subModel, appCfg, &ma.EinoMiddleware, conversationID, db, projectID, logger)
			if err != nil {
				return nil, fmt.Errorf("子代理 %q agentic summarization 中间件: %w", id, err)
			}

			var subHandlers []adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage]
			if len(subPre) > 0 {
				subHandlers = append(subHandlers, subPre...)
			}
			if agenticSkillMW != nil {
				if agenticFSTools && agenticLoc != nil {
					subFs, fsErr := subAgentAgenticFilesystemMiddleware(ctx, agenticLoc, toolInvokeNotify, id, conversationID, projectID, ma.EinoMiddleware.ReductionRootDir, toolMaxBytesFromMW(&ma.EinoMiddleware), mcpExecBinder, einoExecBegin, einoExecAppendPartial, einoExecRegisterCancel, einoExecUnregisterCancel, einoExecFinish, agentToolTimeoutMinutes(appCfg), agentToolWaitTimeoutSeconds(appCfg), agentShellNoOutputTimeoutSeconds(appCfg), nil, executeScopeGuard)
					if fsErr != nil {
						return nil, fmt.Errorf("子代理 %q filesystem 中间件: %w", id, fsErr)
					}
					subHandlers = append(subHandlers, subFs)
				}
				subHandlers = append(subHandlers, agenticSkillMW)
			}
			subHandlers = appendEinoAgenticChatModelTailMiddlewares(subHandlers, einoChatModelTailConfig{
				logger:               logger,
				phase:                "sub_agent:" + id,
				agenticSummarization: subSumMw,
				modelName:            appCfg.OpenAI.Model,
				maxTotalTokens:       appCfg.OpenAI.MaxTotalTokens,
				toolMaxBytes:         toolMaxBytesFromMW(&ma.EinoMiddleware),
				conversationID:       conversationID,
				middlewareConfig:     &ma.EinoMiddleware,
			})

			subInstrFinal := project.AppendVisionImageAnalysisIfReady(instr, appCfg.Vision.Ready())
			subInstrFinal = injectToolNamesOnlyInstruction(ctx, subInstrFinal, subTools, subToolSearchActive)
			if logger != nil {
				subNames := collectToolNames(ctx, subTools)
				mountedNames := collectToolNames(ctx, subToolsForCfg)
				logger.Info("eino tool-name injection",
					zap.String("scope", "sub_agent"),
					zap.String("agent", id),
					zap.Int("tool_names", len(subNames)),
					zap.Int("mounted_tool_names", len(mountedNames)),
					zap.Bool("tool_search_middleware", subToolSearchActive),
				)
			}
			sa, err := newEinoAgenticChatModelAgent(ctx, einoAgenticChatModelAgentConfig{
				Name:          id,
				Description:   desc,
				Instruction:   subInstrFinal,
				GenModelInput: literalAgenticInstructionGenModelInput,
				Model:         subModel,
				ToolsConfig: adk.ToolsConfig{
					ToolsNodeConfig: compose.ToolsNodeConfig{
						Tools:               subToolsForCfg,
						UnknownToolsHandler: einomcp.UnknownToolReminderHandler(),
						ToolCallMiddlewares: append(
							append([]compose.ToolMiddleware{
								modelOutputExecutionGuardMiddleware(),
								localToolRBACMiddleware(),
								hitlToolCallMiddleware(),
							}, einoTurnLimiterMiddlewares(turnToolCallLimiter, logger)...),
							softRecoveryToolMiddleware(),
						),
					},
					EmitInternalEvents: true,
				},
				MaxIterations:       subMax,
				Handlers:            subHandlers,
				ModelRetryConfig:    agenticModelRetryCfg,
				ModelFailoverConfig: agenticModelFailoverCfg,
			})
			if err != nil {
				return nil, fmt.Errorf("子代理 %q: %w", id, err)
			}
			subAgents = append(subAgents, sa)
			if adapted := newEinoAgenticMessageAgentAdapter(sa); adapted != nil {
				supervisorSubAgents = append(supervisorSubAgents, adapted)
			}
		}
	}

	modelFacingTrace := newModelFacingTraceHolder()

	// 与 deep.Config.Name / supervisor 主代理 Name 一致。
	orchestratorName := "cyberstrike-deep"
	orchDescription := "Coordinates specialist agents and MCP tools for authorized security testing."
	orchInstruction, orchMeta := resolveMainOrchestratorInstruction(orchMode, ma, markdownLoad)
	if orchMeta != nil {
		if strings.TrimSpace(orchMeta.EinoName) != "" {
			orchestratorName = strings.TrimSpace(orchMeta.EinoName)
		}
		if d := strings.TrimSpace(orchMeta.Description); d != "" {
			orchDescription = d
		}
	} else if orchMode == "deep" && orch != nil {
		if strings.TrimSpace(orch.EinoName) != "" {
			orchestratorName = strings.TrimSpace(orch.EinoName)
		}
		if d := strings.TrimSpace(orch.Description); d != "" {
			orchDescription = d
		}
	}

	mainTools, err := einomcp.ToolsFromDefinitions(ag, holder, mainDefs, recorder, nil, toolInvokeNotify, orchestratorName)
	if err != nil {
		return nil, err
	}
	var mainToolsForCfg []tool.BaseTool
	var mainToolSearchActive bool
	var mainAgenticOrchestratorPre []adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage]
	mainToolsForCfg, mainAgenticOrchestratorPre, mainToolSearchActive, err = prependEinoAgenticMiddlewares(ctx, &ma.EinoMiddleware, einoMWMain, mainTools, agenticLoc, agenticSkillsRoot, conversationID, projectID, logger)
	if err != nil {
		return nil, err
	}

	orchInstruction = project.AppendSystemPromptBlock(orchInstruction, systemPromptExtra)
	orchInstruction = project.AppendVisionImageAnalysisIfReady(orchInstruction, appCfg.Vision.Ready())
	orchInstruction = injectToolNamesOnlyInstruction(ctx, orchInstruction, mainTools, mainToolSearchActive)
	if logger != nil {
		mainNames := collectToolNames(ctx, mainTools)
		mountedNames := collectToolNames(ctx, mainToolsForCfg)
		logger.Info("eino tool-name injection",
			zap.String("scope", "orchestrator"),
			zap.String("orchestration", orchMode),
			zap.Int("tool_names", len(mainNames)),
			zap.Int("mounted_tool_names", len(mountedNames)),
			zap.Bool("tool_search_middleware", mainToolSearchActive),
		)
	}

	supInstr := strings.TrimSpace(orchInstruction)
	if orchMode == "supervisor" {
		var sb strings.Builder
		if supInstr != "" {
			sb.WriteString(supInstr)
			sb.WriteString("\n\n")
		}
		sb.WriteString("你是监督协调者：可将任务通过 transfer 工具委派给下列专家子代理（使用其在系统中的 Agent 名称）。专家列表：")
		for _, sa := range subAgents {
			if sa == nil {
				continue
			}
			sb.WriteString("\n- ")
			sb.WriteString(sa.Name(ctx))
		}
		sb.WriteString("\n\nSupervisor 是专家路由模式：仅当任务确实需要不同专家分工时才 transfer；简单查询、单步工具调用或无需专业分流的任务由你直接完成。避免在同一子代理之间反复 transfer；除非有新的、具体的补充目标。专家返回后，你必须自行汇总、裁剪、校验证据，再用 exit 交付最终答案。")
		sb.WriteString("\n\n当你已完成用户目标或需要将最终结论交付用户时，使用 exit 工具结束。")
		supInstr = sb.String()
	}

	var deepBackend filesystem.Backend
	var deepShell filesystem.StreamingShell
	if agenticLoc != nil && agenticFSTools {
		deepBackend = agenticLoc
		deepShell = &einoStreamingShellWrap{
			inner:                   security.NewEinoStreamingShell(),
			invokeNotify:            toolInvokeNotify,
			einoAgentName:           orchestratorName,
			outputChunk:             nil,
			beginMonitor:            einoExecBegin,
			appendPartialMonitor:    einoExecAppendPartial,
			registerCancelMonitor:   einoExecRegisterCancel,
			unregisterCancelMonitor: einoExecUnregisterCancel,
			finishMonitor:           einoExecFinish,
			toolTimeoutMinutes:      agentToolTimeoutMinutes(appCfg),
			toolWaitTimeoutSeconds:  agentToolWaitTimeoutSeconds(appCfg),
			shellNoOutputTimeoutSec: agentShellNoOutputTimeoutSeconds(appCfg),
			scopeGuard:              executeScopeGuard,
		}
	}

	var mainModel model.AgenticModel
	var mainSumMw adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage]
	if orchMode != "plan_execute" {
		mainModel, err = agenticModelFactory(ctx, appCfg.OpenAI, einoModelModeNormal)
		if err != nil {
			return nil, fmt.Errorf("多代理主 AgenticModel: %w", err)
		}
		mainSumMw, err = newEinoAgenticSummarizationMiddleware(ctx, mainModel, appCfg, &ma.EinoMiddleware, conversationID, db, projectID, logger)
		if err != nil {
			return nil, fmt.Errorf("多代理主 agentic summarization 中间件: %w", err)
		}
	}

	// noNestedTaskMiddleware 必须在最外层（最先拦截），防止 skill 或其他中间件内部触发 task 调用绕过检测。
	deepHandlers := []adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage]{newNoNestedAgenticTaskMiddleware()}
	var taskBlackboardSupplement string
	if appCfg.Project.Enabled && db != nil {
		if pid := strings.TrimSpace(projectID); pid != "" {
			if block, err := project.BuildFactIndexBlock(db, pid, appCfg.Project); err == nil {
				taskBlackboardSupplement = strings.TrimSpace(block)
			}
		}
	}

	// J4：构建 execute 授权范围闸（Eino 内置 execute 工具专用，与 executor project scope 同源）。
	// 仅 project 绑定且声明 scope_json 时生效；其余情况 CheckExecute 直接放行（向后兼容）。
	// executeScopeGuard 已在函数前部构建（sub_agent 循环前），此处复用。
	if mw := newAgenticTaskContextEnrichMiddleware(runtimeUserMessage, history, ma.SubAgentUserContextMaxRunesEffective(), taskBlackboardSupplement); mw != nil {
		deepHandlers = append(deepHandlers, mw)
	}
	if len(mainAgenticOrchestratorPre) > 0 {
		deepHandlers = append(deepHandlers, mainAgenticOrchestratorPre...)
	}
	if agenticSkillMW != nil {
		deepHandlers = append(deepHandlers, agenticSkillMW)
	}
	deepHandlers = appendEinoAgenticChatModelTailMiddlewares(deepHandlers, einoChatModelTailConfig{
		logger:               logger,
		phase:                "deep_orchestrator",
		agenticSummarization: mainSumMw,
		modelName:            appCfg.OpenAI.Model,
		maxTotalTokens:       appCfg.OpenAI.MaxTotalTokens,
		toolMaxBytes:         toolMaxBytesFromMW(&ma.EinoMiddleware),
		conversationID:       conversationID,
		trace:                modelFacingTrace,
		middlewareConfig:     &ma.EinoMiddleware,
	})

	supHandlers := []adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage]{}
	if len(mainAgenticOrchestratorPre) > 0 {
		supHandlers = append(supHandlers, mainAgenticOrchestratorPre...)
	}
	if agenticSkillMW != nil {
		supHandlers = append(supHandlers, agenticSkillMW)
	}
	supHandlers = appendEinoAgenticChatModelTailMiddlewares(supHandlers, einoChatModelTailConfig{
		logger:               logger,
		phase:                "supervisor_orchestrator",
		agenticSummarization: mainSumMw,
		modelName:            appCfg.OpenAI.Model,
		maxTotalTokens:       appCfg.OpenAI.MaxTotalTokens,
		toolMaxBytes:         toolMaxBytesFromMW(&ma.EinoMiddleware),
		conversationID:       conversationID,
		trace:                modelFacingTrace,
		middlewareConfig:     &ma.EinoMiddleware,
	})

	mainToolsCfg := adk.ToolsConfig{
		ToolsNodeConfig: compose.ToolsNodeConfig{
			Tools:               mainToolsForCfg,
			UnknownToolsHandler: einomcp.UnknownToolReminderHandler(),
			ToolCallMiddlewares: append(
				append([]compose.ToolMiddleware{
					modelOutputExecutionGuardMiddleware(),
					localToolRBACMiddleware(),
					hitlToolCallMiddleware(),
				}, einoTurnLimiterMiddlewares(turnToolCallLimiter, logger)...),
				softRecoveryToolMiddleware(),
			),
		},
		EmitInternalEvents: true,
	}

	deepAgenticOutKey, agenticTaskGen := deepAgenticExtrasFromConfig(ma)

	var da adk.Agent
	switch orchMode {
	case "plan_execute":
		peMainModel, perr := modelFactory(ctx, appCfg.OpenAI, einoModelModePlanner)
		if perr != nil {
			return nil, fmt.Errorf("plan_execute 规划模型: %w", perr)
		}
		if logger != nil {
			logger.Info("plan_execute: planner/replanner 使用无 reasoning 的独立 ChatModel（ToolChoiceForced 兼容）",
				zap.String("model", appCfg.OpenAI.Model),
			)
		}
		execModel, perr := modelFactory(ctx, appCfg.OpenAI, einoModelModeNormal)
		if perr != nil {
			return nil, fmt.Errorf("plan_execute 执行器模型: %w", perr)
		}
		agenticExecModel, perr := agenticModelFactory(ctx, appCfg.OpenAI, einoModelModeNormal)
		if perr != nil {
			return nil, fmt.Errorf("plan_execute 执行器 AgenticModel: %w", perr)
		}
		planRewriteSumMw, perr := newEinoSummarizationMiddleware(ctx, execModel, appCfg, &ma.EinoMiddleware, conversationID, db, projectID, logger)
		if perr != nil {
			return nil, fmt.Errorf("plan_execute planner/replanner summarization: %w", perr)
		}
		var peFsMw adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage]
		if agenticSkillMW != nil && agenticFSTools && agenticLoc != nil {
			peFsMw, err = subAgentAgenticFilesystemMiddleware(ctx, agenticLoc, toolInvokeNotify, "executor", conversationID, projectID, ma.EinoMiddleware.ReductionRootDir, toolMaxBytesFromMW(&ma.EinoMiddleware), mcpExecBinder, einoExecBegin, einoExecAppendPartial, einoExecRegisterCancel, einoExecUnregisterCancel, einoExecFinish, agentToolTimeoutMinutes(appCfg), agentToolWaitTimeoutSeconds(appCfg), agentShellNoOutputTimeoutSeconds(appCfg), nil, executeScopeGuard)
			if err != nil {
				return nil, fmt.Errorf("plan_execute agentic filesystem 中间件: %w", err)
			}
		}
		peRoot, perr := NewPlanExecuteRoot(ctx, &PlanExecuteRootArgs{
			MainToolCallingModel: peMainModel,
			AgenticExecModel:     agenticExecModel,
			OrchInstruction:      orchInstruction,
			ToolsCfg:             mainToolsCfg,
			ExecMaxIter:          deepMaxIter,
			LoopMaxIter:          ma.PlanExecuteLoopMaxIterations,
			AppCfg:               appCfg,
			MwCfg:                &ma.EinoMiddleware,
			ConversationID:       conversationID,
			DB:                   db,
			ProjectID:            projectID,
			Logger:               logger,
			ModelName:            appCfg.OpenAI.Model,
			// 与 Deep/Supervisor 主代理同源：typed patch / reduction / toolsearch / plantask（见 buildPlanExecuteAgenticExecutorHandlers）。
			AgenticExecPreMiddlewares:   mainAgenticOrchestratorPre,
			AgenticSkillMiddleware:      agenticSkillMW,
			AgenticFilesystemMiddleware: peFsMw,
			ModelFacingTrace:            modelFacingTrace,
			PlannerReplannerRewriteHandlers: appendEinoChatModelTailMiddlewares(nil, einoChatModelTailConfig{
				logger:           logger,
				phase:            "plan_execute_planner_replanner",
				summarization:    planRewriteSumMw,
				modelName:        appCfg.OpenAI.Model,
				maxTotalTokens:   appCfg.OpenAI.MaxTotalTokens,
				toolMaxBytes:     toolMaxBytesFromMW(&ma.EinoMiddleware),
				conversationID:   conversationID,
				skipTrace:        true,
				middlewareConfig: &ma.EinoMiddleware,
			}),
			AgenticModelRetryConfig:    agenticModelRetryCfg,
			AgenticModelFailoverConfig: agenticModelFailoverCfg,
		})
		if perr != nil {
			return nil, perr
		}
		da = peRoot
	case "supervisor":
		supCfg := einoAgenticChatModelAgentConfig{
			Name:                orchestratorName,
			Description:         orchDescription,
			Instruction:         supInstr,
			GenModelInput:       literalAgenticInstructionGenModelInput,
			Model:               mainModel,
			ToolsConfig:         mainToolsCfg,
			MaxIterations:       deepMaxIter,
			Handlers:            supHandlers,
			Exit:                &adk.ExitTool{},
			ModelRetryConfig:    agenticModelRetryCfg,
			ModelFailoverConfig: agenticModelFailoverCfg,
		}
		if deepAgenticOutKey != "" {
			supCfg.OutputKey = deepAgenticOutKey
		}
		superChat, serr := newEinoAgenticChatModelAgentAdapter(ctx, supCfg)
		if serr != nil {
			return nil, fmt.Errorf("supervisor agentic 主代理: %w", serr)
		}
		supRoot, serr := supervisor.New(ctx, &supervisor.Config{
			Supervisor: superChat,
			SubAgents:  supervisorSubAgents,
		})
		if serr != nil {
			return nil, fmt.Errorf("supervisor.New: %w", serr)
		}
		da = supRoot
	default:
		dcfg := &deep.TypedConfig[*schema.AgenticMessage]{
			Name:                   orchestratorName,
			Description:            orchDescription,
			ChatModel:              mainModel,
			Instruction:            orchInstruction,
			SubAgents:              subAgents,
			WithoutGeneralSubAgent: ma.WithoutGeneralSubAgent,
			WithoutWriteTodos:      ma.WithoutWriteTodos,
			MaxIteration:           deepMaxIter,
			Backend:                deepBackend,
			StreamingShell:         deepShell,
			Handlers:               deepHandlers,
			ToolsConfig:            mainToolsCfg,
			ModelRetryConfig:       agenticModelRetryCfg,
			ModelFailoverConfig:    agenticModelFailoverCfg,
		}
		if deepAgenticOutKey != "" {
			dcfg.OutputKey = deepAgenticOutKey
		}
		if agenticTaskGen != nil {
			dcfg.TaskToolDescriptionGenerator = agenticTaskGen
		}
		dDeep, derr := deep.NewTyped[*schema.AgenticMessage](ctx, dcfg)
		if derr != nil {
			return nil, fmt.Errorf("deep.NewTyped[AgenticMessage]: %w", derr)
		}
		da = newEinoAgenticMessageAgentAdapter(dDeep)
	}

	// J6/K5: coordinator 模式不构造 deep/supervisor/planexecute 预置根，而是用
	// CoordinatorRunner 把 goal 分解为 title-DAG、并发 dispatch 子代理、再综合。
	// da 已由 switch 赋值；此处仅在 orchMode == "coordinator" 时替换为 coordinator 根 agent。
	if orchMode == "coordinator" {
		coordRoot, cerr := newCoordinatorRootAgent(ctx, &coordinatorRootArgs{
			appCfg:                   appCfg,
			ma:                       ma,
			ag:                       ag,
			db:                       db,
			logger:                   logger,
			conversationID:           conversationID,
			projectID:                projectID,
			orchInstruction:          orchInstruction,
			orchestratorName:         orchestratorName,
			effectiveSubs:            effectiveSubs,
			holder:                   holder,
			agenticLoc:               agenticLoc,
			agenticSkillsRoot:        agenticSkillsRoot,
			agenticSkillMW:           agenticSkillMW,
			agenticModelFactory:      agenticModelFactory,
			agenticModelRetryCfg:     agenticModelRetryCfg,
			agenticModelFailoverCfg:  agenticModelFailoverCfg,
			turnToolCallLimiter:      turnToolCallLimiter,
			toolInvokeNotify:         toolInvokeNotify,
			mcpExecBinder:            mcpExecBinder,
			einoExecBegin:            einoExecBegin,
			einoExecAppendPartial:    einoExecAppendPartial,
			einoExecRegisterCancel:   einoExecRegisterCancel,
			einoExecUnregisterCancel: einoExecUnregisterCancel,
			einoExecFinish:           einoExecFinish,
			executeScopeGuard:        executeScopeGuard,
			recorder:                 recorder,
			progress:                 progress,
			modelFacingTrace:         modelFacingTrace,
		})
		if cerr != nil {
			return nil, fmt.Errorf("coordinator root agent: %w", cerr)
		}
		da = coordRoot
	}

	baseMsgs := historyToMessages(history, appCfg, &ma.EinoMiddleware)
	baseMsgs = appendUserMessageIfNeeded(baseMsgs, runtimeUserMessage)

	streamsMainAssistant := func(agent string) bool {
		if orchMode == "plan_execute" {
			return planExecuteStreamsMainAssistant(agent)
		}
		return agent == "" || agent == orchestratorName
	}
	einoRoleTag := func(agent string) string {
		if orchMode == "plan_execute" {
			return planExecuteEinoRoleTag(agent)
		}
		if streamsMainAssistant(agent) {
			return "orchestrator"
		}
		return "sub"
	}

	return runEinoADKAgentLoop(ctx, &einoADKRunLoopArgs{
		OrchMode:             orchMode,
		OrchestratorName:     orchestratorName,
		ConversationID:       conversationID,
		ProjectID:            projectID,
		Progress:             progress,
		Logger:               logger,
		SnapshotMCPIDs:       snapshotMCPIDs,
		StreamsMainAssistant: streamsMainAssistant,
		EinoRoleTag:          einoRoleTag,
		// Chat history recovery is intentionally centralized in last_react_*.
		// ADK checkpoints are a second persisted model-state channel and make
		// stale-context bugs hard to reason about across user turns.
		CheckpointDir:           "",
		RunRetryMaxAttempts:     RunRetryMaxAttemptsFromConfig(&ma.EinoMiddleware),
		RunRetryMaxBackoffSec:   int(einoRunRetryMaxBackoffFromConfig(&ma.EinoMiddleware).Seconds()),
		McpIDsMu:                &mcpIDsMu,
		McpIDs:                  &mcpIDs,
		FilesystemMonitorAgent:  ag,
		FilesystemMonitorRecord: recorder,
		MCPExecutionBinder:      mcpExecBinder,
		ToolInvokeNotify:        toolInvokeNotify,
		DA:                      da,
		ModelFacingTrace:        modelFacingTrace,
		EinoCallbacks:           &ma.EinoCallbacks,
		MaxTotalTokens:          appCfg.OpenAI.MaxTotalTokens,
		ToolMaxBytes:            toolMaxBytesFromMW(&ma.EinoMiddleware),
		ModelName:               appCfg.OpenAI.Model,
		MiddlewareConfig:        &ma.EinoMiddleware,
		TurnToolCallLimiter:     turnToolCallLimiter,
		EmptyResponseMessage: "(Eino multi-agent orchestration completed but no assistant text was captured. Check process details or logs.) " +
			"（Eino 多代理编排已完成，但未捕获到助手文本输出。请查看过程详情或日志。）",
	}, baseMsgs)
}
