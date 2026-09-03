package security

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"cyberstrike-ai/internal/capability"
	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/metrics"
	"cyberstrike-ai/internal/mcp"
	"cyberstrike-ai/internal/tooloutput"

	"github.com/creack/pty"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// ToolOutputCallback 用于在工具执行过程中把 stdout/stderr 增量推给上层（SSE）。
// 通过 context 传递，避免修改 MCP ToolHandler 签名导致的“写死工具”问题。
type ToolOutputCallback func(chunk string)

type toolOutputCallbackCtxKey struct{}

// ToolOutputCallbackCtxKey 是 context 中的 key，供 Agent 写入回调，Executor 读取并流式回调。
var ToolOutputCallbackCtxKey = toolOutputCallbackCtxKey{}

// Executor 安全工具执行器
type Executor struct {
	config                  *config.SecurityConfig
	toolIndex               map[string]*config.ToolConfig // 工具索引，用于 O(1) 查找
	mcpServer               *mcp.Server
	logger                  *zap.Logger
	shellNoOutputTimeoutSec int // execute/exec 无新输出空闲秒数；0=默认 300；-1=关闭（见 SetShellNoOutputTimeoutSeconds）
	toolOutputMaxBytes      int
	spillRootDir            string
	shellSafeEnabled        bool // shellsafe 元字符拒绝开关（默认 true）；详见 ShellSafeParse
	// hitlWhitelist 判定 HIGH_IMPACT 工具是否在 HITL 免审批白名单（白名单内不打标）。
	// nil 时保守标记所有 HIGH_IMPACT 工具。
	hitlWhitelist hitlWhitelistChecker
	// auditRecorder 在 HIGH_IMPACT 工具执行时写一条 platform audit 记录；nil 时仅日志。
	auditRecorder highImpactAuditRecorder
	// projectScope J4：按 projectID 解析会话级授权 Scope，工具执行前硬拦越界目标。
	// nil 时跳过 project scope 校验（向后兼容）。
	projectScope projectScopeResolver
}

// SetHITLWhitelist / SetHighImpactAuditRecorder / SetProjectScopeResolver 见 executor_policy.go

// NewExecutor 创建新的执行器
func NewExecutor(cfg *config.SecurityConfig, mcpServer *mcp.Server, logger *zap.Logger) *Executor {
	executor := &Executor{
		config:           cfg,
		toolIndex:        make(map[string]*config.ToolConfig),
		mcpServer:        mcpServer,
		logger:           logger,
		shellSafeEnabled: true, // shellsafe 默认开启；可经 SetShellSafeEnabled(false) 关停
	}
	// 构建工具索引
	executor.buildToolIndex()
	return executor
}

// SetShellSafeEnabled 启用或关闭 shellsafe 命令元字符拒绝。运行态热可调。
func (e *Executor) SetShellSafeEnabled(enabled bool) {
	e.shellSafeEnabled = enabled
}

// SetShellNoOutputTimeoutSeconds 配置 exec 工具无输出空闲终止（与 agent.shell_no_output_timeout_seconds 一致）。
func (e *Executor) SetShellNoOutputTimeoutSeconds(sec int) {
	e.shellNoOutputTimeoutSec = sec
}

// SetToolOutputMaxBytes limits stdout/stderr retained and streamed by exec-like
// tools. It should stay aligned with MCP result normalization so every channel
// sees the same bounded payload. Oversized full output is spilled to disk first.
func (e *Executor) SetToolOutputMaxBytes(maxBytes int) {
	e.toolOutputMaxBytes = maxBytes
}

// SetToolOutputSpillRoot sets the reduction-compatible root for spilling full
// exec stdout/stderr when the in-memory bound is exceeded (empty → tmp/reduction).
func (e *Executor) SetToolOutputSpillRoot(rootDir string) {
	e.spillRootDir = strings.TrimSpace(rootDir)
}

func (e *Executor) wrapToolOutputCallback(ctx context.Context, cb ToolOutputCallback) ToolOutputCallback {
	executionID := mcp.MCPExecutionIDFromContext(ctx)
	if e == nil || e.mcpServer == nil || strings.TrimSpace(executionID) == "" {
		return cb
	}
	return func(chunk string) {
		if chunk != "" {
			e.mcpServer.AppendToolExecutionPartialOutput(executionID, chunk)
		}
		if cb != nil {
			cb(chunk)
		}
	}
}

func (e *Executor) spillOptsFromContext(ctx context.Context) tooloutput.SpillOpts {
	root := ""
	if e != nil {
		root = e.spillRootDir
	}
	opts := tooloutput.SpillOpts{RootDir: root}
	if ctx != nil {
		opts.ConversationID = mcp.MCPConversationIDFromContext(ctx)
		opts.ProjectID = mcp.MCPProjectIDFromContext(ctx)
		opts.ExecutionID = mcp.MCPExecutionIDFromContext(ctx)
	}
	if opts.ExecutionID == "" {
		opts.ExecutionID = uuid.NewString()
	}
	return opts
}

// buildToolIndex 构建工具索引，将 O(n) 查找优化为 O(1)
func (e *Executor) buildToolIndex() {
	e.toolIndex = make(map[string]*config.ToolConfig)
	for i := range e.config.Tools {
		if e.config.Tools[i].Enabled {
			e.toolIndex[e.config.Tools[i].Name] = &e.config.Tools[i]
		}
	}
	e.logger.Debug("工具索引构建完成",
		zap.Int("totalTools", len(e.config.Tools)),
		zap.Int("enabledTools", len(e.toolIndex)),
	)
}

// ExecuteTool 执行安全工具
func (e *Executor) ExecuteTool(ctx context.Context, toolName string, args map[string]interface{}) (result *mcp.ToolResult, err error) {
	// 可观测性：统一在 defer 里记录工具执行计数 + 耗时，覆盖所有出口
	// （exec / internal / capability provider / scope 拦截 / 工具未找到）。
	// status 用 "success"/"failure"：err != nil 或返回的 ToolResult.IsError=true 算 failure。
	start := time.Now()
	defer func() {
		status := "success"
		if err != nil {
			status = "failure"
		} else if result != nil && result.IsError {
			status = "failure"
		}
		metrics.RecordToolExecution(toolName, status)
		metrics.RecordToolCallDuration(toolName, status, time.Since(start).Seconds())
	}()

	e.logger.Debug("ExecuteTool被调用",
		zap.String("toolName", toolName),
		zap.Any("args", args),
	)

	// HIGH_IMPACT 第二道标记闸：破坏性工具集命中时，在执行前先记一条 audit，
	// 并在返回的 ToolResult 上打 high_impact=true 标记。不阻断执行——真正阻断
	// 走 HITL 审批流程（已有机制）。白名单内工具（如元工具）不打标，避免噪音。
	highImpactRisk, highImpactHit := IsHighImpactTool(toolName)
	if highImpactHit {
		convID := mcp.MCPConversationIDFromContext(ctx)
		whitelisted := e.isToolWhitelisted(convID, toolName)
		if !whitelisted {
			e.logger.Warn("HIGH_IMPACT 工具执行（第二道标记闸）",
				zap.String("toolName", toolName),
				zap.String("risk", highImpactRisk),
				zap.String("conversationId", convID),
				zap.Bool("hitlWhitelisted", false),
			)
			if e.auditRecorder != nil {
				e.auditRecorder.RecordHighImpactTool(e.currentActor(ctx), convID, toolName, highImpactRisk)
			}
		}
	}

	// 特殊处理：exec工具直接执行系统命令
	if toolName == "exec" {
		e.logger.Debug("执行exec工具")
		result, err := e.executeSystemCommand(ctx, args)
		e.markHighImpact(result, highImpactHit, highImpactRisk)
		return result, err
	}

	// 使用索引查找工具配置（O(1) 查找）
	toolConfig, exists := e.toolIndex[toolName]
	if !exists {
		e.logger.Error("工具未找到或未启用",
			zap.String("toolName", toolName),
			zap.Int("totalTools", len(e.config.Tools)),
			zap.Int("enabledTools", len(e.toolIndex)),
		)
		return nil, fmt.Errorf("工具 %s 未找到或未启用", toolName)
	}

	// scope 全链路接入：工具 yaml 声明了 scope 时，校验目标（target/host/url/ip/domain 参数）是否越界。
	// 越界返回错误 ToolResult（不执行）。nil scope=不限制（向后兼容，现有 106+ 工具不受影响）。
	if toolConfig == nil {
		// exec 路径：toolConfig 为空，scope 不适用（exec 的目标由 shellsafe/HITL 管控）
	} else if toolConfig.Scope != nil {
		if host, port, found := ExtractTarget(args); found {
			ts := &TargetScope{CIDRs: toolConfig.Scope.CIDRs, Domains: toolConfig.Scope.Domains, Ports: toolConfig.Scope.Ports, Excluded: toolConfig.Scope.Excluded}
			if allowed, reason := ts.Allows(host, port); !allowed {
				e.logger.Warn("工具目标越界被 scope 拦截", zap.String("toolName", toolName), zap.String("host", host), zap.Int("port", port), zap.String("reason", reason))
				return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "工具目标越界被 scope 拦截: " + reason}}, IsError: true}, nil
			}
		}
	}

	// J4 project 级授权边界硬闸：会话绑定的 project 声明了 scope_json 时，工具目标
	// 必须落在授权范围内（targets 内且不在 exclude）。与工具 yaml scope 叠加校验：
	// 任一不通过即拦。project scope 解析器未注入或 project 无 scope_json 时不限制（向后兼容）。
	if e.projectScope != nil {
		projectID := mcp.MCPProjectIDFromContext(ctx)
		if projectID != "" {
			ps := e.projectScope.ResolveProjectScope(projectID)
			if host, port, found := ExtractTarget(args); found {
				if allowed, reason := ps.Allows(host, port); !allowed {
					e.logger.Warn("工具目标越界被 project scope 拦截",
						zap.String("toolName", toolName),
						zap.String("host", host),
						zap.Int("port", port),
						zap.String("projectId", projectID),
						zap.String("reason", reason),
					)
					return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "工具目标越界被项目授权范围拦截: " + reason}}, IsError: true}, nil
				}
			}
		}
	}

	// 特殊处理：exec工具直接执行系统命令
	e.logger.Debug("找到工具配置",
		zap.String("toolName", toolName),
		zap.String("command", toolConfig.Command),
		zap.Strings("args", toolConfig.Args),
	)

	// Capability Provider 生命周期（J5）：破坏性工具 subset（如 modify-file）注册了
	// provider 时走 plan→validate→execute→rollback→collect_artifacts 完整生命周期；
	// 其余工具走原路径（向后兼容）。execute 失败自动 Rollback。
	if cap := capability.GetProvider(toolName); cap != nil && cap.Supports(toolName) {
		result, err := e.executeCapabilityProvider(ctx, toolName, args)
		e.markHighImpact(result, highImpactHit, highImpactRisk)
		return result, err
	}
	// 特殊处理：内部工具（command 以 "internal:" 开头）
	if strings.HasPrefix(toolConfig.Command, "internal:") {
		e.logger.Debug("执行内部工具",
			zap.String("toolName", toolName),
			zap.String("command", toolConfig.Command),
		)
		result, err := e.executeInternalTool(ctx, toolName, toolConfig.Command, args)
		e.markHighImpact(result, highImpactHit, highImpactRisk)
		return result, err
	}

	// 构建命令 - 根据工具类型使用不同的参数格式
	cmdArgs := e.buildCommandArgs(toolName, toolConfig, args)

	e.logger.Debug("构建命令参数完成",
		zap.String("toolName", toolName),
		zap.Strings("cmdArgs", cmdArgs),
		zap.Int("argsCount", len(cmdArgs)),
	)

	// 验证命令参数
	if len(cmdArgs) == 0 {
		e.logger.Warn("命令参数为空",
			zap.String("toolName", toolName),
			zap.Any("inputArgs", args),
		)
		return &mcp.ToolResult{
			Content: []mcp.Content{
				{
					Type: "text",
					Text: fmt.Sprintf("错误: 工具 %s 缺少必需的参数。接收到的参数: %v", toolName, args),
				},
			},
			IsError: true,
		}, nil
	}

	// 执行命令
	cmd := exec.CommandContext(ctx, toolConfig.Command, cmdArgs...)
	applyDefaultTerminalEnv(cmd)
	attachNonInteractiveStdin(cmd)
	_ = prepareShellCmdSession(cmd)

	e.logger.Debug("执行安全工具",
		zap.String("tool", toolName),
		zap.Strings("args", cmdArgs),
	)

	var output string
	var execErr error
	spill := e.spillOptsFromContext(ctx)
	// 如果上层提供了 stdout/stderr 增量回调，或当前处于 MCP execution 中，则边执行边读取并回调。
	if cb, ok := ctx.Value(ToolOutputCallbackCtxKey).(ToolOutputCallback); (ok && cb != nil) || mcp.MCPExecutionIDFromContext(ctx) != "" {
		cb = e.wrapToolOutputCallback(ctx, cb)
		output, execErr = streamCommandOutput(ctx, cmd, cb, ResolveShellNoOutputTimeoutSeconds(e.shellNoOutputTimeoutSec), e.toolOutputMaxBytes, spill)
		if execErr != nil && shouldRetryWithPTY(output) {
			e.logger.Info("检测到工具需要 TTY，使用 PTY 重试",
				zap.String("tool", toolName),
			)
			cmd2 := exec.CommandContext(ctx, toolConfig.Command, cmdArgs...)
			applyDefaultTerminalEnv(cmd2)
			_ = prepareShellCmdSession(cmd2)
			output, execErr = runCommandWithPTY(ctx, cmd2, cb, e.toolOutputMaxBytes, spill)
		}
	} else {
		// 非流式：内存缓冲 + ctx 取消杀进程组；行为对齐原 CombinedOutput，避免双流管道 fan-in 死锁。
		output, execErr = combinedOutputCancellableWithLimit(ctx, cmd, e.toolOutputMaxBytes, spill)
		if execErr != nil && shouldRetryWithPTY(output) {
			e.logger.Info("检测到工具需要 TTY，使用 PTY 重试",
				zap.String("tool", toolName),
			)
			cmd2 := exec.CommandContext(ctx, toolConfig.Command, cmdArgs...)
			applyDefaultTerminalEnv(cmd2)
			_ = prepareShellCmdSession(cmd2)
			output, execErr = runCommandWithPTY(ctx, cmd2, nil, e.toolOutputMaxBytes, spill)
		}
	}
	if execErr != nil {
		// 检查退出码是否在允许列表中
		exitCode := getExitCode(execErr)
		if exitCode != nil && toolConfig.AllowedExitCodes != nil {
			for _, allowedCode := range toolConfig.AllowedExitCodes {
				if *exitCode == allowedCode {
					e.logger.Debug("工具执行完成（退出码在允许列表中）",
						zap.String("tool", toolName),
						zap.Int("exitCode", *exitCode),
						zap.String("output", string(output)),
					)
					return e.markHighImpact(&mcp.ToolResult{
						Content: []mcp.Content{
							{
								Type: "text",
								Text: string(output),
							},
						},
						IsError: false,
					}, highImpactHit, highImpactRisk), nil
				}
			}
		}

		e.logger.Error("工具执行失败",
			zap.String("tool", toolName),
			zap.Error(execErr),
			zap.Int("exitCode", getExitCodeValue(execErr)),
			zap.String("output", string(output)),
		)
		return e.markHighImpact(&mcp.ToolResult{
			Content: []mcp.Content{
				{
					Type: "text",
					Text: fmt.Sprintf("工具执行失败: %v\n输出: %s", err, string(output)),
				},
			},
			IsError: true,
		}, highImpactHit, highImpactRisk), nil
	}

	e.logger.Debug("工具执行成功",
		zap.String("tool", toolName),
		zap.String("output", string(output)),
	)

	return e.markHighImpact(&mcp.ToolResult{
		Content: []mcp.Content{
			{
				Type: "text",
				Text: string(output),
			},
		},
		IsError: false,
	}, highImpactHit, highImpactRisk), nil
}

// RegisterTools 注册工具到MCP服务器
func (e *Executor) RegisterTools(mcpServer *mcp.Server) {
	e.logger.Debug("开始注册工具",
		zap.Int("totalTools", len(e.config.Tools)),
		zap.Int("enabledTools", len(e.toolIndex)),
	)

	// 重新构建索引（以防配置更新）
	e.buildToolIndex()

	for i, toolConfig := range e.config.Tools {
		if !toolConfig.Enabled {
			e.logger.Debug("跳过未启用的工具",
				zap.String("tool", toolConfig.Name),
			)
			continue
		}

		// 创建工具配置的副本，避免闭包问题
		toolName := toolConfig.Name
		toolConfigCopy := toolConfig

		// 根据配置决定暴露给 AI/API 的描述：short_description 或 description
		useFullDescription := strings.TrimSpace(strings.ToLower(e.config.ToolDescriptionMode)) == "full"
		shortDesc := toolConfigCopy.ShortDescription
		if shortDesc == "" {
			// 如果没有简短描述，从详细描述中提取第一行或前10000个字符
			desc := toolConfigCopy.Description
			if len(desc) > 10000 {
				if idx := strings.Index(desc, "\n"); idx > 0 && idx < 10000 {
					shortDesc = strings.TrimSpace(desc[:idx])
				} else {
					shortDesc = desc[:10000] + "..."
				}
			} else {
				shortDesc = desc
			}
		}
		if useFullDescription {
			shortDesc = "" // 使用 description 时清空 ShortDescription，下游会回退到 Description
		}

		tool := mcp.Tool{
			Name:             toolConfigCopy.Name,
			Description:      toolConfigCopy.Description,
			ShortDescription: shortDesc,
			InputSchema:      e.buildInputSchema(&toolConfigCopy),
		}

		handler := func(ctx context.Context, args map[string]interface{}) (*mcp.ToolResult, error) {
			e.logger.Debug("工具handler被调用",
				zap.String("toolName", toolName),
				zap.Any("args", args),
			)
			return e.ExecuteTool(ctx, toolName, args)
		}

		mcpServer.RegisterTool(tool, handler)
		e.logger.Debug("注册安全工具成功",
			zap.String("tool", toolConfigCopy.Name),
			zap.String("command", toolConfigCopy.Command),
			zap.Int("index", i),
		)
	}

	e.logger.Debug("工具注册完成",
		zap.Int("registeredCount", len(e.config.Tools)),
	)
}

// command1 & command2 不算完全后台（command2 仍在前台执行）。
func IsBackgroundShellCommand(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	positions := findStandaloneAmpersandPositions(command)
	if len(positions) == 0 {
		return false
	}
	last := positions[len(positions)-1]
	afterAmpersand := strings.TrimSpace(command[last+1:])
	if afterAmpersand != "" {
		return false
	}
	beforeAmpersand := strings.TrimSpace(command[:last])
	return beforeAmpersand != ""
}

// executeSystemCommand 执行系统命令
func (e *Executor) executeSystemCommand(ctx context.Context, args map[string]interface{}) (*mcp.ToolResult, error) {
	// 获取命令
	command, ok := args["command"].(string)
	if !ok {
		return &mcp.ToolResult{
			Content: []mcp.Content{
				{
					Type: "text",
					Text: "错误: 缺少command参数",
				},
			},
			IsError: true,
		}, nil
	}

	if command == "" {
		return &mcp.ToolResult{
			Content: []mcp.Content{
				{
					Type: "text",
					Text: "错误: command参数不能为空",
				},
			},
			IsError: true,
		}, nil
	}

	// 安全检查：记录执行的命令
	e.logger.Warn("执行系统命令",
		zap.String("command", command),
	)

	command = PrepareShellCommandForExecute(command)

	// 获取shell类型（可选，默认为sh）
	shell := "sh"
	if s, ok := args["shell"].(string); ok && s != "" {
		shell = s
	}

	// 获取工作目录（可选）
	workDir := ""
	if wd, ok := args["workdir"].(string); ok && wd != "" {
		workDir = wd
	}

	// shellsafe 纵深防御：拒绝引号外的 shell 元字符（| > < & ; ` $( 换行）。
	// 确实需要这些字符的调用方必须用 `sh -c "..."` 作为单引号参数显式包裹。
	// 可经 security.shell_safe_enabled 关停（默认 true）。
	if e.shellSafeEnabled {
		if _, err := ShellSafeParse(command); err != nil {
			e.logger.Warn("shellsafe 拒绝命令", zap.String("command", command), zap.Error(err))
			return nil, fmt.Errorf("命令被 shellsafe 拒绝: %w", err)
		}
	}

	// 检测是否为后台命令（包含 & 符号，但不在引号内）
	isBackground := IsBackgroundShellCommand(command)

	// 构建命令
	var cmd *exec.Cmd
	if workDir != "" {
		cmd = exec.CommandContext(ctx, shell, "-c", command)
		cmd.Dir = workDir
	} else {
		cmd = exec.CommandContext(ctx, shell, "-c", command)
	}
	ConfigureShellCmdForAgentExecute(cmd)

	// 执行命令
	e.logger.Info("执行系统命令",
		zap.String("command", command),
		zap.String("shell", shell),
		zap.String("workdir", workDir),
		zap.Bool("isBackground", isBackground),
	)

	// 如果是后台命令，使用特殊处理来获取实际的后台进程PID
	if isBackground {
		// 移除命令末尾的 & 符号
		commandWithoutAmpersand := strings.TrimSuffix(strings.TrimSpace(command), "&")
		commandWithoutAmpersand = strings.TrimSpace(commandWithoutAmpersand)

		// 构建新命令：后台作业重定向标准流后 echo $pid（与 RedirectBackgroundJobStdio 一致）。
		pidCommand := RedirectBackgroundJobStdio(commandWithoutAmpersand+" &") + " pid=$!; echo $pid"

		// 创建新命令来获取PID
		var pidCmd *exec.Cmd
		if workDir != "" {
			pidCmd = exec.CommandContext(ctx, shell, "-c", pidCommand)
			pidCmd.Dir = workDir
		} else {
			pidCmd = exec.CommandContext(ctx, shell, "-c", pidCommand)
		}
		ConfigureShellCmdForAgentExecute(pidCmd)

		// 获取stdout管道
		stdout, err := pidCmd.StdoutPipe()
		if err != nil {
			e.logger.Error("创建stdout管道失败",
				zap.String("command", command),
				zap.Error(err),
			)
			// 如果创建管道失败，使用shell进程的PID作为fallback
			if err := pidCmd.Start(); err != nil {
				return &mcp.ToolResult{
					Content: []mcp.Content{
						{
							Type: "text",
							Text: fmt.Sprintf("后台命令启动失败: %v", err),
						},
					},
					IsError: true,
				}, nil
			}
			pid := pidCmd.Process.Pid
			go pidCmd.Wait() // 在后台等待，避免僵尸进程
			return &mcp.ToolResult{
				Content: []mcp.Content{
					{
						Type: "text",
						Text: fmt.Sprintf("后台命令已启动\n命令: %s\n进程ID: %d (可能不准确，获取PID失败)\n\n注意: 后台进程将继续运行，不会等待其完成。", command, pid),
					},
				},
				IsError: false,
			}, nil
		}

		// 启动命令
		if err := pidCmd.Start(); err != nil {
			stdout.Close()
			e.logger.Error("后台命令启动失败",
				zap.String("command", command),
				zap.Error(err),
			)
			return &mcp.ToolResult{
				Content: []mcp.Content{
					{
						Type: "text",
						Text: fmt.Sprintf("后台命令启动失败: %v", err),
					},
				},
				IsError: true,
			}, nil
		}

		// 读取第一行输出（PID）
		reader := bufio.NewReader(stdout)
		pidLine, err := reader.ReadString('\n')
		stdout.Close()

		var actualPid int
		if err != nil && err != io.EOF {
			e.logger.Warn("读取后台进程PID失败",
				zap.String("command", command),
				zap.Error(err),
			)
			// 如果读取失败，使用shell进程的PID
			actualPid = pidCmd.Process.Pid
		} else {
			// 解析PID
			pidStr := strings.TrimSpace(pidLine)
			if parsedPid, err := strconv.Atoi(pidStr); err == nil {
				actualPid = parsedPid
			} else {
				e.logger.Warn("解析后台进程PID失败",
					zap.String("command", command),
					zap.String("pidLine", pidStr),
					zap.Error(err),
				)
				// 如果解析失败，使用shell进程的PID
				actualPid = pidCmd.Process.Pid
			}
		}

		// 在goroutine中等待shell进程，避免僵尸进程
		go func() {
			if err := pidCmd.Wait(); err != nil {
				e.logger.Debug("后台命令shell进程执行完成",
					zap.String("command", command),
					zap.Error(err),
				)
			}
		}()

		e.logger.Info("后台命令已启动",
			zap.String("command", command),
			zap.Int("actualPid", actualPid),
		)

		return &mcp.ToolResult{
			Content: []mcp.Content{
				{
					Type: "text",
					Text: fmt.Sprintf("后台命令已启动\n命令: %s\n进程ID: %d\n\n注意: 后台进程将继续运行，不会等待其完成。", command, actualPid),
				},
			},
			IsError: false,
		}, nil
	}

	// 非后台命令：等待输出
	var output string
	var err error
	spill := e.spillOptsFromContext(ctx)
	// 若上层提供工具输出增量回调，或当前处于 MCP execution 中，则边执行边流式读取。
	if cb, ok := ctx.Value(ToolOutputCallbackCtxKey).(ToolOutputCallback); (ok && cb != nil) || mcp.MCPExecutionIDFromContext(ctx) != "" {
		cb = e.wrapToolOutputCallback(ctx, cb)
		output, err = streamCommandOutput(ctx, cmd, cb, ResolveShellNoOutputTimeoutSeconds(e.shellNoOutputTimeoutSec), e.toolOutputMaxBytes, spill)
		if err != nil && shouldRetryWithPTY(output) {
			e.logger.Info("检测到系统命令需要 TTY，使用 PTY 重试")
			cmd2 := exec.CommandContext(ctx, shell, "-c", command)
			if workDir != "" {
				cmd2.Dir = workDir
			}
			ConfigureShellCmdForAgentExecute(cmd2)
			output, err = runCommandWithPTY(ctx, cmd2, cb, e.toolOutputMaxBytes, spill)
		}
	} else {
		output, err = combinedOutputCancellableWithLimit(ctx, cmd, e.toolOutputMaxBytes, spill)
		if err != nil && shouldRetryWithPTY(output) {
			e.logger.Info("检测到系统命令需要 TTY，使用 PTY 重试")
			cmd2 := exec.CommandContext(ctx, shell, "-c", command)
			if workDir != "" {
				cmd2.Dir = workDir
			}
			ConfigureShellCmdForAgentExecute(cmd2)
			output, err = runCommandWithPTY(ctx, cmd2, nil, e.toolOutputMaxBytes, spill)
		}
	}
	if err != nil {
		e.logger.Error("系统命令执行失败",
			zap.String("command", command),
			zap.Error(err),
			zap.String("output", string(output)),
		)
		return &mcp.ToolResult{
			Content: []mcp.Content{
				{
					Type: "text",
					Text: FormatCommandFailureFromErr(err, output),
				},
			},
			IsError: true,
		}, nil
	}

	e.logger.Info("系统命令执行成功",
		zap.String("command", command),
		zap.String("output_length", fmt.Sprintf("%d", len(output))),
	)

	return &mcp.ToolResult{
		Content: []mcp.Content{
			{
				Type: "text",
				Text: string(output),
			},
		},
		IsError: false,
	}, nil
}

// combinedOutputCancellable 行为对齐 cmd.CombinedOutput（stdout/stderr 写入内存缓冲），
// 但在 ctx 取消时 terminateCmdTree 终止整棵进程树。
// 非流式路径不使用双流管道 fan-in，避免 stderr 撑满管道缓冲区时与 stdout 互相阻塞导致死锁。
// 无输出空闲检测由上层 agent.tool_timeout_minutes 兜底，不改变原 CombinedOutput 语义。
func combinedOutputCancellable(ctx context.Context, cmd *exec.Cmd) (string, error) {
	return combinedOutputCancellableWithLimit(ctx, cmd, 0, tooloutput.SpillOpts{})
}

func combinedOutputCancellableWithLimit(ctx context.Context, cmd *exec.Cmd, maxBytes int, spill tooloutput.SpillOpts) (string, error) {
	var tee *tooloutput.Tee
	if maxBytes > 0 {
		tee = tooloutput.NewTee(spill)
		defer func() { _ = tee.Close() }()
	}
	stdoutBuf := newBoundedOutputCollector(maxBytes, tee)
	stderrBuf := newBoundedOutputCollector(maxBytes, tee)
	cmd.Stdout = stdoutBuf
	cmd.Stderr = stderrBuf

	session, err := StartShellSession(cmd)
	if err != nil {
		return "", err
	}

	done := make(chan error, 1)
	go func() {
		done <- session.Wait()
	}()

	stopWatch := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			TerminateShellCmdSession(session)
		case <-stopWatch:
		}
	}()
	defer close(stopWatch)

	var waitErr error
	select {
	case waitErr = <-done:
	case <-ctx.Done():
		waitErr = <-done
		return finalizeJoinedBoundedOutputs(stdoutBuf, stderrBuf, maxBytes, tee), ctx.Err()
	}
	return finalizeJoinedBoundedOutputs(stdoutBuf, stderrBuf, maxBytes, tee), waitErr
}

func joinCommandOutput(stdout, stderr string) string {
	if stderr == "" {
		return stdout
	}
	if stdout == "" {
		return stderr
	}
	return stdout + stderr
}

type boundedOutputCollector struct {
	builder   strings.Builder
	maxBytes  int
	seenBytes int
	truncated bool
	tee       *tooloutput.Tee
}

func newBoundedOutputCollector(maxBytes int, tee *tooloutput.Tee) *boundedOutputCollector {
	return &boundedOutputCollector{maxBytes: maxBytes, tee: tee}
}

func (b *boundedOutputCollector) Write(p []byte) (int, error) {
	b.WriteStringLimited(string(p))
	return len(p), nil
}

func (b *boundedOutputCollector) WriteStringLimited(s string) string {
	if b == nil {
		return ""
	}
	if b.tee != nil {
		_, _ = b.tee.Write([]byte(s))
	}
	if b.maxBytes <= 0 {
		b.seenBytes += len(s)
		b.builder.WriteString(s)
		return s
	}
	b.seenBytes += len(s)
	if b.builder.Len() >= b.maxBytes {
		b.truncated = true
		return ""
	}
	remaining := b.maxBytes - b.builder.Len()
	if len(s) <= remaining {
		b.builder.WriteString(s)
		return s
	}
	kept := truncateStringBytes(s, remaining)
	b.builder.WriteString(kept)
	b.truncated = true
	return kept
}

func (b *boundedOutputCollector) String() string {
	if b == nil {
		return ""
	}
	return b.builder.String()
}

func finalizeJoinedBoundedOutputs(stdout, stderr *boundedOutputCollector, maxBytes int, tee *tooloutput.Tee) string {
	if tee != nil {
		_ = tee.Close()
	}
	truncated := (stdout != nil && stdout.truncated) || (stderr != nil && stderr.truncated)
	seen := 0
	if stdout != nil {
		seen += stdout.seenBytes
	}
	if stderr != nil {
		seen += stderr.seenBytes
	}
	joined := joinCommandOutput(
		func() string {
			if stdout == nil {
				return ""
			}
			return stdout.String()
		}(),
		func() string {
			if stderr == nil {
				return ""
			}
			return stderr.String()
		}(),
	)
	if maxBytes > 0 && !truncated && len(joined) > maxBytes {
		truncated = true
		seen = len(joined)
	}
	path := ""
	if tee != nil {
		path = tee.Path()
	}
	if truncated && maxBytes > 0 {
		if path != "" {
			return tooloutput.FormatPersistedFromFile(path, seen, maxBytes)
		}
		if len(joined) > maxBytes {
			return truncateStringBytes(joined, maxBytes)
		}
		return joined
	}
	if path != "" {
		_ = os.Remove(path)
	}
	if maxBytes > 0 && len(joined) > maxBytes {
		return truncateStringBytes(joined, maxBytes)
	}
	return joined
}

func finalizeBoundedOutput(collector *boundedOutputCollector, maxBytes int, tee *tooloutput.Tee) string {
	if tee != nil {
		_ = tee.Close()
	}
	if collector == nil {
		return ""
	}
	path := ""
	if tee != nil {
		path = tee.Path()
	}
	if collector.truncated && maxBytes > 0 {
		if path != "" {
			return tooloutput.FormatPersistedFromFile(path, collector.seenBytes, maxBytes)
		}
		return truncateStringBytes(collector.String(), maxBytes)
	}
	if path != "" {
		_ = os.Remove(path)
	}
	out := collector.String()
	if maxBytes > 0 && len(out) > maxBytes {
		return tooloutput.BoundWithSpill(out, maxBytes, tooloutput.SpillOpts{})
	}
	return out
}

func limitOutputString(s string, maxBytes int, spill tooloutput.SpillOpts) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	return tooloutput.BoundWithSpill(s, maxBytes, spill)
}

func truncateStringBytes(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	if cut <= 0 {
		return ""
	}
	return s[:cut]
}

// streamCommandOutput 以“边读边回调”的方式读取命令 stdout/stderr。
// 使用定长块读取，避免按行读取在无换行输出时永久阻塞；ctx 取消时终止进程树。
func streamCommandOutput(ctx context.Context, cmd *exec.Cmd, cb ToolOutputCallback, noOutputSec int, maxBytes int, spill tooloutput.SpillOpts) (string, error) {
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		_ = stdoutPipe.Close()
		return "", err
	}
	session, err := StartShellSession(cmd)
	if err != nil {
		_ = stdoutPipe.Close()
		_ = stderrPipe.Close()
		return "", err
	}

	stopWatch := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			TerminateShellCmdSession(session)
		case <-stopWatch:
		}
	}()
	defer close(stopWatch)

	chunks := make(chan string, 64)
	var wg sync.WaitGroup
	readFn := func(r io.Reader) {
		defer wg.Done()
		buf := make([]byte, 8192)
		for {
			n, readErr := r.Read(buf)
			if n > 0 {
				chunks <- string(buf[:n])
			}
			if readErr != nil {
				return
			}
		}
	}

	wg.Add(2)
	go readFn(stdoutPipe)
	go readFn(stderrPipe)

	go func() {
		wg.Wait()
		close(chunks)
	}()

	tee := (*tooloutput.Tee)(nil)
	if maxBytes > 0 {
		tee = tooloutput.NewTee(spill)
		defer func() { _ = tee.Close() }()
	}
	outBuilder := newBoundedOutputCollector(maxBytes, tee)
	var deltaBuilder strings.Builder
	lastFlush := time.Now()

	flush := func() {
		if deltaBuilder.Len() == 0 {
			return
		}
		if cb != nil {
			cb(deltaBuilder.String())
		}
		deltaBuilder.Reset()
		lastFlush = time.Now()
	}

	idleWatch := NewShellInactivityWatch(noOutputSec)
	if idleWatch != nil {
		defer idleWatch.Stop()
	}

	fireInactivity := func() {
		TerminateShellCmdSession(session)
		msg := ShellNoOutputTimeoutMessage(idleWatch.Sec)
		msg = outBuilder.WriteStringLimited(msg)
		if cb != nil {
			cb(msg)
		}
		_ = session.Wait()
	}

chunksLoop:
	for {
		var idleCh <-chan struct{}
		if idleWatch != nil {
			idleCh = idleWatch.Expired
		}
		select {
		case <-ctx.Done():
			TerminateShellCmdSession(session)
			flush()
			_ = session.Wait()
			return outBuilder.String(), ctx.Err()
		case <-idleCh:
			fireInactivity()
			return finalizeBoundedOutput(outBuilder, maxBytes, tee), fmt.Errorf("shell inactivity timeout (%ds)", idleWatch.Sec)
		case chunk, ok := <-chunks:
			if !ok {
				break chunksLoop
			}
			if chunk != "" && idleWatch != nil {
				idleWatch.Bump()
			}
			keptChunk := outBuilder.WriteStringLimited(chunk)
			deltaBuilder.WriteString(keptChunk)
			if deltaBuilder.Len() >= 2048 || time.Since(lastFlush) >= 200*time.Millisecond {
				flush()
			}
		}
	}
	flush()

	// 等待命令结束，返回最终退出状态
	waitErr := session.Wait()
	return finalizeBoundedOutput(outBuilder, maxBytes, tee), waitErr
}

// applyDefaultTerminalEnv 为外部工具补齐常见的终端环境变量。
// 注意：这不会创建 TTY，只是减少某些工具在非交互环境下的“奇怪排版/检测失败”。
func applyDefaultTerminalEnv(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	// 仅在未显式设置 Env 时，继承当前进程环境
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = ApplyNonInteractivePagerEnv(cmd.Env)
	// 如果用户已设置 TERM/COLUMNS/LINES，则不覆盖
	has := func(k string) bool {
		prefix := k + "="
		for _, e := range cmd.Env {
			if strings.HasPrefix(e, prefix) {
				return true
			}
		}
		return false
	}
	if !has("TERM") {
		cmd.Env = append(cmd.Env, "TERM=xterm-256color")
	}
	if !has("COLUMNS") {
		cmd.Env = append(cmd.Env, "COLUMNS=256")
	}
	if !has("LINES") {
		cmd.Env = append(cmd.Env, "LINES=40")
	}
}

func shouldRetryWithPTY(output string) bool {
	o := strings.ToLower(output)
	// autorecon / python termios 常见报错
	if strings.Contains(o, "inappropriate ioctl for device") {
		return true
	}
	if strings.Contains(o, "termios.error") {
		return true
	}
	// 兜底：stdin 不是 tty
	if strings.Contains(o, "not a tty") {
		return true
	}
	return false
}

// runCommandWithPTY 为子进程分配 PTY，适配需要交互式终端的工具（如 autorecon）。
// 若 cb != nil，将持续回调增量输出（用于 SSE）。
func runCommandWithPTY(ctx context.Context, cmd *exec.Cmd, cb ToolOutputCallback, maxBytes int, spill tooloutput.SpillOpts) (string, error) {
	if runtime.GOOS == "windows" {
		// PTY 方案为类 Unix；Windows 走原逻辑
		if cb != nil {
			return streamCommandOutput(ctx, cmd, cb, 0, maxBytes, spill)
		}
		_ = prepareShellCmdSession(cmd)
		return combinedOutputCancellableWithLimit(ctx, cmd, maxBytes, spill)
	}

	_ = prepareShellCmdSession(cmd)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return "", err
	}
	defer func() { _ = ptmx.Close() }()

	rootPID := 0
	if cmd.Process != nil {
		rootPID = cmd.Process.Pid
	}

	// ctx 取消时尽快终止子进程
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = ptmx.Close() // 触发读退出
			terminateProcessGroup(rootPID, cmd)
		case <-done:
		}
	}()
	defer close(done)

	tee := (*tooloutput.Tee)(nil)
	if maxBytes > 0 {
		tee = tooloutput.NewTee(spill)
		defer func() { _ = tee.Close() }()
	}
	outBuilder := newBoundedOutputCollector(maxBytes, tee)
	var deltaBuilder strings.Builder
	lastFlush := time.Now()
	flush := func() {
		if cb == nil || deltaBuilder.Len() == 0 {
			deltaBuilder.Reset()
			lastFlush = time.Now()
			return
		}
		cb(deltaBuilder.String())
		deltaBuilder.Reset()
		lastFlush = time.Now()
	}

	buf := make([]byte, 4096)
	for {
		n, readErr := ptmx.Read(buf)
		if n > 0 {
			chunk := string(buf[:n])
			// 统一换行为 \n，避免前端错位
			chunk = strings.ReplaceAll(chunk, "\r\n", "\n")
			chunk = strings.ReplaceAll(chunk, "\r", "\n")
			keptChunk := outBuilder.WriteStringLimited(chunk)
			deltaBuilder.WriteString(keptChunk)
			if deltaBuilder.Len() >= 2048 || time.Since(lastFlush) >= 200*time.Millisecond {
				flush()
			}
		}
		if readErr != nil {
			break
		}
	}
	flush()

	waitErr := cmd.Wait()
	return finalizeBoundedOutput(outBuilder, maxBytes, tee), waitErr
}


// executeCapabilityProvider 执行 Capability Provider 完整生命周期（J5）：
// plan→validate→execute→rollback→collect_artifacts。ExecuteTool 与 executeInternalTool
// 的 internal:capability 分支共用，确保 modify-file 不论从哪条路径进入都走完整生命周期。
func (e *Executor) executeCapabilityProvider(ctx context.Context, toolName string, args map[string]interface{}) (*mcp.ToolResult, error) {
	cap := capability.GetProvider(toolName)
	if cap == nil || !cap.Supports(toolName) {
		// 未注册 provider：退化为原 internal tool 行为（向后兼容），不阻断。
		return nil, fmt.Errorf("工具 %s 未注册 capability provider", toolName)
	}
	plan, perr := cap.Plan(args)
	if perr != nil {
		return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "Capability Plan 失败: " + perr.Error()}}, IsError: true}, nil
	}
	if verr := cap.Validate(args); verr != nil {
		return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "Capability Validate 失败: " + verr.Error()}}, IsError: true}, nil
	}
	result, xerr := cap.Execute(ctx, args)
	if xerr != nil {
		// Execute 暂存在 args 里的备份路径回填到 plan，让 Rollback 真正可执行
		//（否则 plan.BackupPath 为空，Rollback 恒失败成死代码）。
		if bp, ok := args["_backup_path"].(string); ok && bp != "" {
			plan.BackupPath = bp
		}
		if rberr := cap.Rollback(ctx, plan); rberr != nil {
			e.logger.Error("Capability Rollback 失败", zap.Error(rberr))
		}
		return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "执行失败已回滚: " + xerr.Error()}}, IsError: true}, nil
	}
	if result == nil {
		result = &mcp.ToolResult{}
	}
	// CollectArtifacts：把证据回写到 result.Content。
	// ToolResult 无 Metadata 字段，故追加为文本段（保留 provider 原结果 + 证据并列）。
	if arts, aerr := cap.CollectArtifacts(plan); aerr != nil {
		e.logger.Warn("Capability CollectArtifacts 失败", zap.String("toolName", toolName), zap.Error(aerr))
	} else if len(arts) > 0 {
		e.logger.Info("Capability Artifacts", zap.Int("count", len(arts)), zap.String("toolName", toolName))
		artJSON, _ := json.Marshal(arts)
		extra := fmt.Sprintf("\n备份证据: %s", string(artJSON))
		if len(result.Content) > 0 {
			result.Content[0].Text += extra
		} else {
			result.Content = []mcp.Content{{Type: "text", Text: extra}}
		}
	}
	return result, nil
}

// executeInternalTool 执行内部工具（不执行外部命令）
func (e *Executor) executeInternalTool(ctx context.Context, toolName string, command string, args map[string]interface{}) (*mcp.ToolResult, error) {
	internalToolType := strings.TrimPrefix(command, "internal:")
	e.logger.Warn("未知的内部工具",
		zap.String("toolName", toolName),
		zap.String("internalToolType", internalToolType),
	)
	return &mcp.ToolResult{
		Content: []mcp.Content{
			{
				Type: "text",
				Text: fmt.Sprintf("错误: 未知的内部工具类型: %s", internalToolType),
			},
		},
		IsError: true,
	}, nil
}

// getExitCode 从错误中提取退出码，如果不是ExitError则返回nil
func getExitCode(err error) *int {
	if err == nil {
		return nil
	}
	if exitError, ok := err.(*exec.ExitError); ok {
		if exitError.ProcessState != nil {
			exitCode := exitError.ExitCode()
			return &exitCode
		}
	}
	return nil
}

// getExitCodeValue 从错误中提取退出码值，如果不是ExitError则返回-1
func getExitCodeValue(err error) int {
	if code := getExitCode(err); code != nil {
		return *code
	}
	return -1
}
