package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"cyberstrike-ai/internal/authctx"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/handler"
	"cyberstrike-ai/internal/mcp"
	"cyberstrike-ai/internal/mcp/builtin"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

func registerWebshellTools(mcpServer *mcp.Server, db *database.DB, webshellHandler *handler.WebShellHandler, logger *zap.Logger) {
	if db == nil || webshellHandler == nil {
		logger.Warn("跳过 WebShell 工具注册：db 或 webshellHandler 为空")
		return
	}

	// webshell_exec
	execTool := mcp.Tool{
		Name:             builtin.ToolWebshellExec,
		Description:      "在指定的 WebShell 连接上执行一条系统命令，返回命令的标准输出。connection_id 由用户在 AI 助手上下文中选定。",
		ShortDescription: "在 WebShell 连接上执行命令",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"connection_id": map[string]interface{}{
					"type":        "string",
					"description": "WebShell 连接 ID（如 ws_xxx）",
				},
				"command": map[string]interface{}{
					"type":        "string",
					"description": "要执行的系统命令",
				},
			},
			"required": []string{"connection_id", "command"},
		},
	}
	execHandler := func(ctx context.Context, args map[string]interface{}) (*mcp.ToolResult, error) {
		cid, _ := args["connection_id"].(string)
		cmd, _ := args["command"].(string)
		if cid == "" || cmd == "" {
			return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "connection_id 和 command 均为必填"}}, IsError: true}, nil
		}
		conn, err := db.GetWebshellConnection(cid)
		if err != nil || conn == nil {
			return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "未找到该 WebShell 连接或查询失败"}}, IsError: true}, nil
		}
		output, ok, errMsg := webshellHandler.ExecWithConnection(conn, cmd)
		if errMsg != "" {
			return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: errMsg}}, IsError: true}, nil
		}
		if !ok {
			return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "HTTP 非 200，输出:\n" + output}}, IsError: false}, nil
		}
		return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: output}}, IsError: false}, nil
	}
	mcpServer.RegisterTool(execTool, execHandler)

	// webshell_file_list
	listTool := mcp.Tool{
		Name:             builtin.ToolWebshellFileList,
		Description:      "在指定 WebShell 连接上列出目录内容。path 默认为当前目录（.）。",
		ShortDescription: "在 WebShell 上列出目录",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"connection_id": map[string]interface{}{"type": "string", "description": "WebShell 连接 ID"},
				"path":          map[string]interface{}{"type": "string", "description": "目录路径，默认 ."},
			},
			"required": []string{"connection_id"},
		},
	}
	listHandler := func(ctx context.Context, args map[string]interface{}) (*mcp.ToolResult, error) {
		cid, _ := args["connection_id"].(string)
		path, _ := args["path"].(string)
		if cid == "" {
			return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "connection_id 必填"}}, IsError: true}, nil
		}
		conn, err := db.GetWebshellConnection(cid)
		if err != nil || conn == nil {
			return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "未找到该 WebShell 连接"}}, IsError: true}, nil
		}
		output, ok, errMsg := webshellHandler.FileOpWithConnection(conn, "list", path, "", "")
		if errMsg != "" {
			return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: errMsg}}, IsError: true}, nil
		}
		return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: output}}, IsError: !ok}, nil
	}
	mcpServer.RegisterTool(listTool, listHandler)

	// webshell_file_read
	readTool := mcp.Tool{
		Name:             builtin.ToolWebshellFileRead,
		Description:      "在指定 WebShell 连接上读取文件内容。",
		ShortDescription: "在 WebShell 上读取文件",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"connection_id": map[string]interface{}{"type": "string", "description": "WebShell 连接 ID"},
				"path":          map[string]interface{}{"type": "string", "description": "文件路径"},
			},
			"required": []string{"connection_id", "path"},
		},
	}
	readHandler := func(ctx context.Context, args map[string]interface{}) (*mcp.ToolResult, error) {
		cid, _ := args["connection_id"].(string)
		path, _ := args["path"].(string)
		if cid == "" || path == "" {
			return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "connection_id 和 path 必填"}}, IsError: true}, nil
		}
		conn, err := db.GetWebshellConnection(cid)
		if err != nil || conn == nil {
			return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "未找到该 WebShell 连接"}}, IsError: true}, nil
		}
		output, ok, errMsg := webshellHandler.FileOpWithConnection(conn, "read", path, "", "")
		if errMsg != "" {
			return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: errMsg}}, IsError: true}, nil
		}
		return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: output}}, IsError: !ok}, nil
	}
	mcpServer.RegisterTool(readTool, readHandler)

	// webshell_file_write
	writeTool := mcp.Tool{
		Name:             builtin.ToolWebshellFileWrite,
		Description:      "在指定 WebShell 连接上写入文件内容（会覆盖已有文件）。",
		ShortDescription: "在 WebShell 上写入文件",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"connection_id": map[string]interface{}{"type": "string", "description": "WebShell 连接 ID"},
				"path":          map[string]interface{}{"type": "string", "description": "文件路径"},
				"content":       map[string]interface{}{"type": "string", "description": "要写入的内容"},
			},
			"required": []string{"connection_id", "path", "content"},
		},
	}
	writeHandler := func(ctx context.Context, args map[string]interface{}) (*mcp.ToolResult, error) {
		cid, _ := args["connection_id"].(string)
		path, _ := args["path"].(string)
		content, _ := args["content"].(string)
		if cid == "" || path == "" {
			return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "connection_id 和 path 必填"}}, IsError: true}, nil
		}
		conn, err := db.GetWebshellConnection(cid)
		if err != nil || conn == nil {
			return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "未找到该 WebShell 连接"}}, IsError: true}, nil
		}
		output, ok, errMsg := webshellHandler.FileOpWithConnection(conn, "write", path, content, "")
		if errMsg != "" {
			return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: errMsg}}, IsError: true}, nil
		}
		if !ok {
			return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "写入可能失败，输出:\n" + output}}, IsError: false}, nil
		}
		return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "写入成功\n" + output}}, IsError: false}, nil
	}
	mcpServer.RegisterTool(writeTool, writeHandler)

	logger.Debug("WebShell 工具注册成功")
}

// registerWebshellManagementTools 注册 WebShell 连接管理 MCP 工具
func registerWebshellManagementTools(mcpServer *mcp.Server, db *database.DB, webshellHandler *handler.WebShellHandler, logger *zap.Logger) {
	if db == nil {
		logger.Warn("跳过 WebShell 管理工具注册：db 为空")
		return
	}
	projectIDFromToolArgs := func(ctx context.Context, args map[string]interface{}) string {
		projectID, _ := args["project_id"].(string)
		projectID = strings.TrimSpace(projectID)
		if projectID == "" {
			projectID = strings.TrimSpace(mcp.MCPProjectIDFromContext(ctx))
		}
		return projectID
	}
	explicitProjectIDFromToolArgs := func(args map[string]interface{}) string {
		projectID, _ := args["project_id"].(string)
		return strings.TrimSpace(projectID)
	}
	authorizeWebshellToolProject := func(principal authctx.Principal, permission, projectID string) *mcp.ToolResult {
		projectID = strings.TrimSpace(projectID)
		if projectID == "" {
			return nil
		}
		if projectID == database.ProjectFilterUnbound {
			return nil
		}
		if !db.UserCanAccessResource(principal.UserID, principal.ScopeFor(permission), "project", projectID) {
			return &mcp.ToolResult{
				Content: []mcp.Content{{Type: "text", Text: "无权访问项目: " + projectID}},
				IsError: true,
			}
		}
		return nil
	}

	// manage_webshell_list - 列出所有 webshell 连接
	listTool := mcp.Tool{
		Name:             builtin.ToolManageWebshellList,
		Description:      "列出已保存的 WebShell 连接，返回连接ID、URL、类型、所属项目、备注等信息。默认按当前对话项目边界过滤：项目对话看本项目，未绑定项目的对话看未绑定连接；显式传 project_id 时按指定项目过滤。",
		ShortDescription: "列出所有 WebShell 连接",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"project_id": map[string]interface{}{
					"type":        "string",
					"description": "项目 ID；不填时在项目会话中默认使用当前项目。",
				},
			},
		},
	}
	listHandler := func(ctx context.Context, args map[string]interface{}) (*mcp.ToolResult, error) {
		connections := []database.WebShellConnection{}
		var err error
		if principal, ok := authctx.PrincipalFromContext(ctx); ok {
			projectID := explicitProjectIDFromToolArgs(args)
			if projectID == "" {
				projectID = mcpEffectiveProjectFilter(ctx, db)
			}
			if result := authorizeWebshellToolProject(principal, "webshell:read", projectID); result != nil {
				return result, nil
			}
			connections, err = db.ListWebshellConnectionsForAccess(principal.UserID, principal.ScopeFor("webshell:read"), projectID)
		} else {
			return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "缺少认证身份"}}, IsError: true}, nil
		}
		if err != nil {
			return &mcp.ToolResult{
				Content: []mcp.Content{{Type: "text", Text: "获取连接列表失败: " + err.Error()}},
				IsError: true,
			}, nil
		}
		if len(connections) == 0 {
			return &mcp.ToolResult{
				Content: []mcp.Content{{Type: "text", Text: "暂无 WebShell 连接"}},
				IsError: false,
			}, nil
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("找到 %d 个 WebShell 连接：\n\n", len(connections)))
		for _, conn := range connections {
			sb.WriteString(fmt.Sprintf("ID: %s\n", conn.ID))
			sb.WriteString(fmt.Sprintf("  URL: %s\n", conn.URL))
			sb.WriteString(fmt.Sprintf("  类型: %s\n", conn.Type))
			sb.WriteString(fmt.Sprintf("  请求方式: %s\n", conn.Method))
			sb.WriteString(fmt.Sprintf("  命令参数: %s\n", conn.CmdParam))
			if conn.ProjectID != "" {
				sb.WriteString(fmt.Sprintf("  项目ID: %s\n", conn.ProjectID))
			} else {
				sb.WriteString("  项目: 未绑定\n")
			}
			if conn.Remark != "" {
				sb.WriteString(fmt.Sprintf("  备注: %s\n", conn.Remark))
			}
			sb.WriteString(fmt.Sprintf("  创建时间: %s\n", conn.CreatedAt.Format("2006-01-02 15:04:05")))
			sb.WriteString("\n")
		}
		return &mcp.ToolResult{
			Content: []mcp.Content{{Type: "text", Text: sb.String()}},
			IsError: false,
		}, nil
	}
	mcpServer.RegisterTool(listTool, listHandler)

	// manage_webshell_add - 添加新的 webshell 连接
	addTool := mcp.Tool{
		Name:             builtin.ToolManageWebshellAdd,
		Description:      "添加新的 WebShell 连接到管理系统。支持 PHP、ASP、ASPX、JSP 等类型的一句话木马。",
		ShortDescription: "添加 WebShell 连接",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"url": map[string]interface{}{
					"type":        "string",
					"description": "Shell 地址，如 http://target.com/shell.php（必填）",
				},
				"password": map[string]interface{}{
					"type":        "string",
					"description": "连接密码/密钥，如冰蝎/蚁剑的连接密码",
				},
				"type": map[string]interface{}{
					"type":        "string",
					"description": "Shell 类型：php、asp、aspx、jsp，默认为 php",
					"enum":        []string{"php", "asp", "aspx", "jsp"},
				},
				"method": map[string]interface{}{
					"type":        "string",
					"description": "请求方式：GET 或 POST，默认为 POST",
					"enum":        []string{"GET", "POST"},
				},
				"cmd_param": map[string]interface{}{
					"type":        "string",
					"description": "命令参数名，不填默认为 cmd",
				},
				"remark": map[string]interface{}{
					"type":        "string",
					"description": "备注，便于识别的备注名",
				},
			},
			"required": []string{"url"},
		},
	}
	addHandler := func(ctx context.Context, args map[string]interface{}) (*mcp.ToolResult, error) {
		urlStr, _ := args["url"].(string)
		if urlStr == "" {
			return &mcp.ToolResult{
				Content: []mcp.Content{{Type: "text", Text: "错误: url 参数必填"}},
				IsError: true,
			}, nil
		}

		password, _ := args["password"].(string)
		shellType, _ := args["type"].(string)
		if shellType == "" {
			shellType = "php"
		}
		method, _ := args["method"].(string)
		if method == "" {
			method = "post"
		}
		cmdParam, _ := args["cmd_param"].(string)
		if cmdParam == "" {
			cmdParam = "cmd"
		}
		remark, _ := args["remark"].(string)
		principal, ok := authctx.PrincipalFromContext(ctx)
		if !ok {
			return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "缺少认证身份"}}, IsError: true}, nil
		}
		projectID := projectIDFromToolArgs(ctx, args)
		if result := authorizeWebshellToolProject(principal, "webshell:write", projectID); result != nil {
			return result, nil
		}

		// 生成连接ID
		connID := "ws_" + strings.ReplaceAll(uuid.New().String(), "-", "")[:12]
		conn := &database.WebShellConnection{
			ID:        connID,
			URL:       urlStr,
			Password:  password,
			Type:      strings.ToLower(shellType),
			Method:    strings.ToLower(method),
			CmdParam:  cmdParam,
			Remark:    remark,
			ProjectID: projectID,
			CreatedAt: time.Now(),
		}

		if err := db.CreateWebshellConnection(conn); err != nil {
			return &mcp.ToolResult{
				Content: []mcp.Content{{Type: "text", Text: "添加 WebShell 连接失败: " + err.Error()}},
				IsError: true,
			}, nil
		}
		_ = db.SetResourceOwner("webshell", conn.ID, principal.UserID)
		_ = db.AssignResourceToUser(principal.UserID, "webshell", conn.ID)
		projectLine := "项目: 未绑定"
		if conn.ProjectID != "" {
			projectLine = "项目ID: " + conn.ProjectID
		}

		return &mcp.ToolResult{
			Content: []mcp.Content{{
				Type: "text",
				Text: fmt.Sprintf("WebShell 连接添加成功！\n\n连接ID: %s\nURL: %s\n类型: %s\n请求方式: %s\n命令参数: %s\n%s", conn.ID, conn.URL, conn.Type, conn.Method, conn.CmdParam, projectLine),
			}},
			IsError: false,
		}, nil
	}
	mcpServer.RegisterTool(addTool, addHandler)

	// manage_webshell_update - 更新 webshell 连接
	updateTool := mcp.Tool{
		Name:             builtin.ToolManageWebshellUpdate,
		Description:      "更新已存在的 WebShell 连接信息。",
		ShortDescription: "更新 WebShell 连接",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"connection_id": map[string]interface{}{
					"type":        "string",
					"description": "要更新的 WebShell 连接 ID（必填）",
				},
				"url": map[string]interface{}{
					"type":        "string",
					"description": "新的 Shell 地址",
				},
				"password": map[string]interface{}{
					"type":        "string",
					"description": "新的连接密码/密钥",
				},
				"type": map[string]interface{}{
					"type":        "string",
					"description": "新的 Shell 类型：php、asp、aspx、jsp",
					"enum":        []string{"php", "asp", "aspx", "jsp"},
				},
				"method": map[string]interface{}{
					"type":        "string",
					"description": "新的请求方式：GET 或 POST",
					"enum":        []string{"GET", "POST"},
				},
				"cmd_param": map[string]interface{}{
					"type":        "string",
					"description": "新的命令参数名",
				},
				"remark": map[string]interface{}{
					"type":        "string",
					"description": "新的备注",
				},
				"project_id": map[string]interface{}{
					"type":        "string",
					"description": "新的所属项目 ID；传空字符串可取消绑定。",
				},
			},
			"required": []string{"connection_id"},
		},
	}
	updateHandler := func(ctx context.Context, args map[string]interface{}) (*mcp.ToolResult, error) {
		connID, _ := args["connection_id"].(string)
		if connID == "" {
			return &mcp.ToolResult{
				Content: []mcp.Content{{Type: "text", Text: "错误: connection_id 参数必填"}},
				IsError: true,
			}, nil
		}

		// 获取现有连接
		existing, err := db.GetWebshellConnection(connID)
		if err != nil || existing == nil {
			return &mcp.ToolResult{
				Content: []mcp.Content{{Type: "text", Text: "未找到指定的 WebShell 连接: " + connID}},
				IsError: true,
			}, nil
		}

		// 更新字段（如果提供了新值）
		if urlStr, ok := args["url"].(string); ok && urlStr != "" {
			existing.URL = urlStr
		}
		if password, ok := args["password"].(string); ok {
			existing.Password = password
		}
		if shellType, ok := args["type"].(string); ok && shellType != "" {
			existing.Type = strings.ToLower(shellType)
		}
		if method, ok := args["method"].(string); ok && method != "" {
			existing.Method = strings.ToLower(method)
		}
		if cmdParam, ok := args["cmd_param"].(string); ok && cmdParam != "" {
			existing.CmdParam = cmdParam
		}
		if remark, ok := args["remark"].(string); ok {
			existing.Remark = remark
		}
		if projectID, ok := args["project_id"].(string); ok {
			projectID = strings.TrimSpace(projectID)
			if projectID != "" {
				principal, ok := authctx.PrincipalFromContext(ctx)
				if !ok {
					return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "缺少认证身份"}}, IsError: true}, nil
				}
				if result := authorizeWebshellToolProject(principal, "webshell:write", projectID); result != nil {
					return result, nil
				}
			}
			existing.ProjectID = projectID
		}

		if err := db.UpdateWebshellConnection(existing); err != nil {
			return &mcp.ToolResult{
				Content: []mcp.Content{{Type: "text", Text: "更新 WebShell 连接失败: " + err.Error()}},
				IsError: true,
			}, nil
		}

		return &mcp.ToolResult{
			Content: []mcp.Content{{
				Type: "text",
				Text: fmt.Sprintf("WebShell 连接更新成功！\n\n连接ID: %s\nURL: %s\n类型: %s\n请求方式: %s\n命令参数: %s\n项目ID: %s\n备注: %s", existing.ID, existing.URL, existing.Type, existing.Method, existing.CmdParam, existing.ProjectID, existing.Remark),
			}},
			IsError: false,
		}, nil
	}
	mcpServer.RegisterTool(updateTool, updateHandler)

	// manage_webshell_delete - 删除 webshell 连接
	deleteTool := mcp.Tool{
		Name:             builtin.ToolManageWebshellDelete,
		Description:      "删除指定的 WebShell 连接。",
		ShortDescription: "删除 WebShell 连接",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"connection_id": map[string]interface{}{
					"type":        "string",
					"description": "要删除的 WebShell 连接 ID（必填）",
				},
			},
			"required": []string{"connection_id"},
		},
	}
	deleteHandler := func(ctx context.Context, args map[string]interface{}) (*mcp.ToolResult, error) {
		connID, _ := args["connection_id"].(string)
		if connID == "" {
			return &mcp.ToolResult{
				Content: []mcp.Content{{Type: "text", Text: "错误: connection_id 参数必填"}},
				IsError: true,
			}, nil
		}

		if err := db.DeleteWebshellConnection(connID); err != nil {
			return &mcp.ToolResult{
				Content: []mcp.Content{{Type: "text", Text: "删除 WebShell 连接失败: " + err.Error()}},
				IsError: true,
			}, nil
		}

		return &mcp.ToolResult{
			Content: []mcp.Content{{
				Type: "text",
				Text: fmt.Sprintf("WebShell 连接 %s 已成功删除", connID),
			}},
			IsError: false,
		}, nil
	}
	mcpServer.RegisterTool(deleteTool, deleteHandler)

	// manage_webshell_test - 测试 webshell 连接
	testTool := mcp.Tool{
		Name:             builtin.ToolManageWebshellTest,
		Description:      "测试指定的 WebShell 连接是否可用，会尝试执行一个简单的命令（如 whoami 或 dir）。",
		ShortDescription: "测试 WebShell 连接",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"connection_id": map[string]interface{}{
					"type":        "string",
					"description": "要测试的 WebShell 连接 ID（必填）",
				},
				"command": map[string]interface{}{
					"type":        "string",
					"description": "测试命令，默认为 whoami（Linux）或 dir（Windows）",
				},
			},
			"required": []string{"connection_id"},
		},
	}
	testHandler := func(ctx context.Context, args map[string]interface{}) (*mcp.ToolResult, error) {
		connID, _ := args["connection_id"].(string)
		if connID == "" {
			return &mcp.ToolResult{
				Content: []mcp.Content{{Type: "text", Text: "错误: connection_id 参数必填"}},
				IsError: true,
			}, nil
		}

		// 获取连接
		conn, err := db.GetWebshellConnection(connID)
		if err != nil || conn == nil {
			return &mcp.ToolResult{
				Content: []mcp.Content{{Type: "text", Text: "未找到指定的 WebShell 连接: " + connID}},
				IsError: true,
			}, nil
		}

		// 确定测试命令
		testCmd, _ := args["command"].(string)
		if testCmd == "" {
			// 根据 shell 类型选择默认命令
			if conn.Type == "asp" || conn.Type == "aspx" {
				testCmd = "dir"
			} else {
				testCmd = "whoami"
			}
		}

		// 执行测试命令
		output, ok, errMsg := webshellHandler.ExecWithConnection(conn, testCmd)
		if errMsg != "" {
			return &mcp.ToolResult{
				Content: []mcp.Content{{Type: "text", Text: fmt.Sprintf("连接测试失败！\n\n连接ID: %s\nURL: %s\n错误: %s", connID, conn.URL, errMsg)}},
				IsError: true,
			}, nil
		}

		if !ok {
			return &mcp.ToolResult{
				Content: []mcp.Content{{Type: "text", Text: fmt.Sprintf("连接测试失败！HTTP 非 200\n\n连接ID: %s\nURL: %s\n输出: %s", connID, conn.URL, output)}},
				IsError: true,
			}, nil
		}

		return &mcp.ToolResult{
			Content: []mcp.Content{{
				Type: "text",
				Text: fmt.Sprintf("连接测试成功！\n\n连接ID: %s\nURL: %s\n类型: %s\n\n测试命令: %s\n输出结果:\n%s", connID, conn.URL, conn.Type, testCmd, output),
			}},
			IsError: false,
		}, nil
	}
	mcpServer.RegisterTool(testTool, testHandler)

	logger.Debug("WebShell 管理工具注册成功")
}

// initializeKnowledge 初始化知识库组件（用于动态初始化）
