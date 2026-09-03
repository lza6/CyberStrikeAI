// Package multiagent
package multiagent

import (
	"context"
	"fmt"
	"strings"

	"cyberstrike-ai/internal/agent"
	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/einomcp"
	"cyberstrike-ai/internal/multiagent/coordkit"
	"cyberstrike-ai/internal/project"
	"cyberstrike-ai/internal/security"

	localbk "github.com/cloudwego/eino-ext/adk/backend/local"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// coordinatorRootArgs bundles the dependencies captured in RunDeepAgent that
// the coordinator root agent needs to build its one-shot coordinator and
// on-demand worker agents. It is a snapshot of the live wiring (model factory,
// middleware, MCP binder, exec monitors, scope guard) so the coordinator can
// construct workers per-assignee exactly the way RunDeepAgent constructs deep
// sub-agents.
type coordinatorRootArgs struct {
	appCfg                   *config.Config
	ma                       *config.MultiAgentConfig
	ag                       *agent.Agent
	db                       *database.DB
	logger                   *zap.Logger
	conversationID           string
	projectID                string
	orchInstruction          string
	orchestratorName         string
	effectiveSubs            []config.MultiAgentSubConfig
	holder                   *einomcp.ConversationHolder
	agenticLoc               *localbk.Local
	agenticSkillsRoot        string
	agenticSkillMW           adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage]
	agenticModelFactory      einoAgenticChatModelFactoryFunc
	agenticModelRetryCfg     *adk.TypedModelRetryConfig[*schema.AgenticMessage]
	agenticModelFailoverCfg  *adk.ModelFailoverConfig[*schema.AgenticMessage]
	turnToolCallLimiter      *TurnToolCallLimiter
	toolInvokeNotify         *einomcp.ToolInvokeNotifyHolder
	mcpExecBinder            *MCPExecutionBinder
	einoExecBegin            func(toolCallID, command string) string
	einoExecAppendPartial    func(executionID, toolCallID, chunk string)
	einoExecRegisterCancel   func(executionID string, cancel context.CancelFunc)
	einoExecUnregisterCancel func(executionID string)
	einoExecFinish           func(executionID, toolCallID, command, stdout string, success bool, invokeErr error)
	executeScopeGuard        *security.ExecuteScopeGuard
	recorder                 func(id, toolCallID string)
	progress                 func(eventType, message string, data interface{})
	modelFacingTrace         *modelFacingTraceHolder
}

// einoAgenticChatModelFactoryFunc is the signature of newEinoAgenticChatModelFactory's
// returned factory. Aliased here so the coordinator file does not need to reach into
// the factory implementation details.
type einoAgenticChatModelFactoryFunc = einoAgenticModelConfigFactory

// newCoordinatorRootAgent builds the adk.Agent that drives the coordinator
// orchestration mode. It wires the CoordinatorRunner (decompose → dispatch →
// synthesize) behind a thin adk.Agent adapter so it slots into the existing
// runEinoADKAgentLoop plumbing. The coordinator agent itself is a one-shot
// ChatModelAgent with the coordinator instruction; workers are built on
// demand per assignee via buildCoordinatorWorkerAgent, which mirrors the
// RunDeepAgent sub-agent construction.
//
// Design note: the coordinator mode intentionally reuses the deep sub-agent
// builder machinery (tools, middleware, summarization, tool-call limiters,
// scope guard) rather than inventing a parallel pipeline, so J6 gains the
// new decomposition/synthesis behavior without duplicating J4/J5 scope guards.
func newCoordinatorRootAgent(ctx context.Context, a *coordinatorRootArgs) (adk.Agent, error) {
	if a == nil || a.appCfg == nil || a.ma == nil || a.ag == nil {
		return nil, fmt.Errorf("coordinator root: args/config/agent is nil")
	}

	knownAgents := make([]string, 0, len(a.effectiveSubs))
	for _, sub := range a.effectiveSubs {
		id := strings.TrimSpace(sub.ID)
		if id != "" {
			knownAgents = append(knownAgents, id)
		}
	}

	// Coordinator agent factory: a one-shot ChatModelAgent with the coordinator
	// instruction and no tools. It only reasons over text (decompose + synth).
	agentFactory := func(ctx context.Context, instruction string) (adk.Agent, error) {
		coordModel, err := a.agenticModelFactory(ctx, a.appCfg.OpenAI, einoModelModeNormal)
		if err != nil {
			return nil, fmt.Errorf("coordinator model: %w", err)
		}
		return newEinoAgenticChatModelAgentAdapter(ctx, einoAgenticChatModelAgentConfig{
			Name:          a.orchestratorName,
			Description:   "Coordinator: decompose goal into a title-DAG, dispatch workers, synthesize results.",
			Instruction:   instruction,
			GenModelInput: literalAgenticInstructionGenModelInput,
			Model:         coordModel,
			MaxIterations: 3,
			Handlers: appendEinoAgenticChatModelTailMiddlewares(nil, einoChatModelTailConfig{
				logger:           a.logger,
				phase:            "coordinator",
				trace:            a.modelFacingTrace,
				modelName:        a.appCfg.OpenAI.Model,
				maxTotalTokens:   a.appCfg.OpenAI.MaxTotalTokens,
				toolMaxBytes:     toolMaxBytesFromMW(&a.ma.EinoMiddleware),
				conversationID:   a.conversationID,
				middlewareConfig: &a.ma.EinoMiddleware,
			}),
			ModelRetryConfig:    a.agenticModelRetryCfg,
			ModelFailoverConfig: a.agenticModelFailoverCfg,
		})
	}

	// Worker factory: build a one-shot worker agent for `assignee` using the
	// same construction as RunDeepAgent's sub-agent loop. The assignee must be
	// a known sub-agent id; otherwise the factory returns an error so the
	// CoordinatorRunner marks that task failed.
	workerFactory := func(ctx context.Context, assignee string) (adk.Agent, error) {
		sub, ok := findSubByID(a.effectiveSubs, assignee)
		if !ok {
			return nil, fmt.Errorf("unknown assignee %q (not in sub_agents)", assignee)
		}
		return buildCoordinatorWorkerAgent(ctx, a, sub)
	}

	runner, err := NewCoordinatorRunner(CoordinatorConfig{
		AgentFactory:  agentFactory,
		WorkerFactory: workerFactory,
		KnownAgents:   knownAgents,
		Logger:        a.logger,
		Bus:           coordkit.NewMessageBus(),
	})
	if err != nil {
		return nil, err
	}
	return newCoordinatorAgentAdapter(runner, a), nil
}

// findSubByID returns the MultiAgentSubConfig with the given id, case-insensitive.
func findSubByID(subs []config.MultiAgentSubConfig, id string) (config.MultiAgentSubConfig, bool) {
	target := strings.TrimSpace(id)
	for _, s := range subs {
		if strings.EqualFold(strings.TrimSpace(s.ID), target) {
			return s, true
		}
	}
	return config.MultiAgentSubConfig{}, false
}

// buildCoordinatorWorkerAgent constructs a one-shot worker agent for a single
// sub-agent config, mirroring the RunDeepAgent sub-agent loop (runner.go
// ~line 185-310) so the worker carries the same tools, middleware, scope
// guard and tool-call limiter as a deep sub-agent.
func buildCoordinatorWorkerAgent(ctx context.Context, a *coordinatorRootArgs, sub config.MultiAgentSubConfig) (adk.Agent, error) {
	id := strings.TrimSpace(sub.ID)
	if id == "" {
		return nil, fmt.Errorf("worker: empty sub id")
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
	if bind != "" && a.appCfg.Roles != nil {
		if r, ok := a.appCfg.Roles[bind]; ok && r.Enabled {
			if len(roleTools) == 0 && len(r.Tools) > 0 {
				roleTools = r.Tools
			}
		}
	}

	subModel, err := a.agenticModelFactory(ctx, a.appCfg.OpenAI, einoModelModeNormal)
	if err != nil {
		return nil, fmt.Errorf("worker %q AgenticModel: %w", id, err)
	}

	subDefs := a.ag.ToolsForRole(roleTools)
	subTools, err := einomcp.ToolsFromDefinitions(a.ag, a.holder, subDefs, a.recorder, nil, a.toolInvokeNotify, id)
	if err != nil {
		return nil, fmt.Errorf("worker %q tools: %w", id, err)
	}

	subToolsForCfg, subPre, subToolSearchActive, err := prependEinoAgenticMiddlewares(ctx, &a.ma.EinoMiddleware, einoMWSub, subTools, a.agenticLoc, a.agenticSkillsRoot, a.conversationID, a.projectID, a.logger)
	if err != nil {
		return nil, fmt.Errorf("worker %q eino middleware: %w", id, err)
	}

	subMax := resolveMaxIterations(a.appCfg, sub.MaxIterations)

	subSumMw, err := newEinoAgenticSummarizationMiddleware(ctx, subModel, a.appCfg, &a.ma.EinoMiddleware, a.conversationID, a.db, a.projectID, a.logger)
	if err != nil {
		return nil, fmt.Errorf("worker %q summarization: %w", id, err)
	}

	var subHandlers []adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage]
	if len(subPre) > 0 {
		subHandlers = append(subHandlers, subPre...)
	}
	if a.agenticSkillMW != nil {
		// J6 K7 审计修复（HIGH）：与 deep 子代理路径（runner.go subFs 分支）等价，
		// coordinator worker 也必须挂 filesystem 中间件并传入 executeScopeGuard，
		// 否则 J4 项目 scope 硬闸对 coordinator worker 的 Eino 文件工具失效。
		// 条件与 runner.go 一致：agenticFSTools && agenticLoc != nil（经 agenticSkillMW
		// 非空近似，且此处再显式校验 agenticLoc）。
		if a.agenticLoc != nil {
			subFs, fsErr := subAgentAgenticFilesystemMiddleware(ctx, a.agenticLoc, a.toolInvokeNotify, id, a.conversationID, a.projectID, a.ma.EinoMiddleware.ReductionRootDir, toolMaxBytesFromMW(&a.ma.EinoMiddleware), a.mcpExecBinder, a.einoExecBegin, a.einoExecAppendPartial, a.einoExecRegisterCancel, a.einoExecUnregisterCancel, a.einoExecFinish, agentToolTimeoutMinutes(a.appCfg), agentToolWaitTimeoutSeconds(a.appCfg), agentShellNoOutputTimeoutSeconds(a.appCfg), nil, a.executeScopeGuard)
			if fsErr != nil {
				return nil, fmt.Errorf("worker %q filesystem 中间件: %w", id, fsErr)
			}
			subHandlers = append(subHandlers, subFs)
		}
		subHandlers = append(subHandlers, a.agenticSkillMW)
	}
	subHandlers = appendEinoAgenticChatModelTailMiddlewares(subHandlers, einoChatModelTailConfig{
		logger:               a.logger,
		phase:                "coordinator_worker:" + id,
		agenticSummarization: subSumMw,
		modelName:            a.appCfg.OpenAI.Model,
		maxTotalTokens:       a.appCfg.OpenAI.MaxTotalTokens,
		toolMaxBytes:         toolMaxBytesFromMW(&a.ma.EinoMiddleware),
		conversationID:       a.conversationID,
		middlewareConfig:     &a.ma.EinoMiddleware,
		trace:                a.modelFacingTrace,
	})

	subInstrFinal := project.AppendVisionImageAnalysisIfReady(instr, a.appCfg.Vision.Ready())
	subInstrFinal = injectToolNamesOnlyInstruction(ctx, subInstrFinal, subTools, subToolSearchActive)

	worker, err := newEinoAgenticChatModelAgent(ctx, einoAgenticChatModelAgentConfig{
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
					}, einoTurnLimiterMiddlewares(a.turnToolCallLimiter, a.logger)...),
					softRecoveryToolMiddleware(),
				),
			},
			EmitInternalEvents: true,
		},
		MaxIterations:       subMax,
		Handlers:            subHandlers,
		ModelRetryConfig:    a.agenticModelRetryCfg,
		ModelFailoverConfig: a.agenticModelFailoverCfg,
	})
	if err != nil {
		return nil, fmt.Errorf("worker %q: %w", id, err)
	}
	return newEinoAgenticMessageAgentAdapter(worker), nil
}

// coordinatorAgentAdapter wraps a CoordinatorRunner behind the adk.Agent
// interface so the existing runEinoADKAgentLoop drives it like any other
// root agent. It extracts the user's goal from the last message, runs the
// coordinator, and emits a single AgentEvent carrying the synthesis text.
type coordinatorAgentAdapter struct {
	runner *CoordinatorRunner
	args   *coordinatorRootArgs
}

func newCoordinatorAgentAdapter(runner *CoordinatorRunner, a *coordinatorRootArgs) *coordinatorAgentAdapter {
	return &coordinatorAgentAdapter{runner: runner, args: a}
}

func (c *coordinatorAgentAdapter) Name(context.Context) string { return c.args.orchestratorName }
func (c *coordinatorAgentAdapter) Description(context.Context) string {
	return "Coordinator orchestration root (J6)"
}

func (c *coordinatorAgentAdapter) Run(ctx context.Context, input *adk.AgentInput, _ ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	goal := ""
	if len(input.Messages) > 0 {
		goal = input.Messages[len(input.Messages)-1].Content
	}
	res, runErr := c.runner.Run(ctx, goal)

	var ev *adk.AgentEvent
	if runErr != nil {
		ev = &adk.AgentEvent{
			Err: runErr,
		}
	} else {
		msg := schema.AssistantMessage(res.Response, nil)
		ev = &adk.AgentEvent{
			Output: &adk.AgentOutput{
				MessageOutput: &adk.MessageVariant{
					Message: msg,
				},
			},
		}
		if c.args.progress != nil {
			c.args.progress("progress", res.Response, map[string]interface{}{
				"einoAgent":      true,
				"einoRole":       "orchestrator",
				"einoScope":      "main",
				"orchestration":  "coordinator",
				"conversationId": c.args.conversationID,
				"source":         "eino",
			})
		}
	}
	iterator, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	go func() {
		defer generator.Close()
		generator.Send(ev)
	}()
	return iterator
}

func (c *coordinatorAgentAdapter) Resume(context.Context, *adk.ResumeInfo, ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	// Coordinator runs to completion in one shot; resume is not supported.
	iterator, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	go func() {
		defer generator.Close()
	}()
	return iterator
}

// _ keeps tool/compose imports referenced for the worker construction above.
var _ tool.BaseTool = (*adk.ExitTool)(nil)
