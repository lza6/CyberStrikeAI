package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"cyberstrike-ai/internal/ctxsandbox"
)

// ctxExecuteToolName is the agent-facing tool name for the "think in code"
// sandbox runner. Declared locally rather than in builtin/constants.go so
// this file is fully self-contained and does not touch the concurrently-
// edited constants list.
const ctxExecuteToolName = "ctx_execute"

// ctxExecuteToolShort is the short description shown in the tool list.
const ctxExecuteToolShort = "sandbox 内执行命令，大输出自动索引只回 verdict（省 token）"

// RegisterCtxExecuteTool wires the context-mode "think in code" sandbox
// engine (internal/ctxsandbox) as a builtin MCP tool. The tool runs a
// command, and — when output exceeds the intent threshold — indexes it and
// returns only a BM25 verdict (matching section titles + previews), so the
// model never loads raw large outputs into its context budget. This mirrors
// context-mode's ctx_execute (server.ts:1647-2036).
//
// The engine's Index store is injected by the caller; in a CGO-enabled build
// this would be an FTS5-backed store, otherwise an in-memory one. The
// spillRoot configures the workspace under which sandboxed runs execute.
//
// Registration is idempotent: re-registering replaces the prior handler.
func RegisterCtxExecuteTool(server *Server, engine *ctxsandbox.Engine) {
	if server == nil || engine == nil {
		return
	}
	server.RegisterTool(Tool{
		Name:             ctxExecuteToolName,
		ShortDescription: ctxExecuteToolShort,
		Description: "在 sandbox 工作区内执行命令；stdout 超过 intent 阈值时自动落索引，只返回匹配的 section 标题+首行预览（verdict），" +
			"避免大输出灌入模型上下文。大输出（>100KB）强制索引并只回指针，用 ctx_search 取回。" +
			"典型场景：nmap 全端口扫描、subdomain 枚举、爬虫 DOM 等大输出工具的结果裁剪。" +
			"参数：command（数组，必填）、intent（字符串，可选，描述你想定位什么）、timeout_seconds（可选，上限 600）。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"command": map[string]interface{}{
					"type": "array",
					"items": map[string]interface{}{"type": "string"},
					"description": "要执行的命令及参数数组，如 [\"nmap\",\"-sT\",\"-p-\",\"192.168.1.0/24\"]",
				},
				"intent": map[string]interface{}{
					"type": "string",
					"description": "你想从输出里定位什么（如 \"22 端口\" 或 \"开放 web 服务\"）。输出超过 5KB 时，" +
						"工具会把全文索引并只返回匹配 intent 的 section 标题+预览（verdict）；不填则小输出直返、大输出落索引返指针。",
				},
				"timeout_seconds": map[string]interface{}{
					"type": "number",
					"description": "超时秒数，默认 60，上限 600；超时返回已捕获的部分输出",
				},
			},
			"required": []string{"command"},
		},
	}, func(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
		cmdRaw, ok := args["command"]
		if !ok || cmdRaw == nil {
			return textToolResult("command 必填（命令数组）", true), nil
		}
		cmd, ok := cmdRaw.([]interface{})
		if !ok || len(cmd) == 0 {
			return textToolResult("command 必须是非空字符串数组", true), nil
		}
		parts := make([]string, 0, len(cmd))
		for _, c := range cmd {
			s, _ := c.(string)
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			parts = append(parts, s)
		}
		if len(parts) == 0 {
			return textToolResult("command 数组无有效元素", true), nil
		}

		intent := strings.TrimSpace(stringArg(args, "intent"))
		timeout := time.Duration(intArg(args, "timeout_seconds", 60, 600)) * time.Second
		if timeout <= 0 {
			timeout = 60 * time.Second
		}
		if timeout > 10*time.Minute {
			timeout = 10 * time.Minute
		}

		runCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		res, err := engine.Run(runCtx, parts, intent)
		if err != nil {
			return textToolResult(fmt.Sprintf("ctx_execute 执行失败: %v", err), true), nil
		}
		// Annotate with provenance so downstream audit knows output was spilled.
		header := ""
		if res.Indexed {
			header = fmt.Sprintf("[ctx_execute] 已索引 %d 字节输出 → %s（仅返回 verdict/指针）\n",
				res.BytesIn, res.Path)
		} else if res.BytesIn > 0 {
			header = fmt.Sprintf("[ctx_execute] 原始输出 %d 字节，直返\n", res.BytesIn)
		}
		text := header + res.Text
		return textToolResult(text, false), nil
	})
}

// IsCtxExecuteTool reports whether name is the ctx_execute tool. Exposed so
// downstream (executor policy, audit) can recognise the tool without a hard
// dependency on this registration file's constant.
func IsCtxExecuteTool(name string) bool {
	return name == ctxExecuteToolName
}
