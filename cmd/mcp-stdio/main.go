package main

import (
	"cyberstrike-ai/internal/capability"
	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/logger"
	"cyberstrike-ai/internal/mcp"
	"cyberstrike-ai/internal/security"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"go.uber.org/zap"
)

func main() {
	var configPath = flag.String("config", "config.yaml", "配置文件路径")
	flag.Parse()

	// 加载配置
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		os.Exit(1)
	}

	// 初始化日志（stdio 模式下使用 stderr 输出日志，避免干扰 JSON-RPC 通信）
	log := logger.New(cfg.Log.Level, "stderr")

	// 创建MCP服务器
	mcpServer := mcp.NewServer(log.Logger)

	// 创建安全工具执行器
	executor := security.NewExecutor(&cfg.Security, mcpServer, log.Logger)

	// 缺口3 注册缺失：注册 modify-file 的 Capability Provider，让破坏性工具
	// 在 stdio MCP 模式下也走 plan→validate→execute→rollback→collect_artifacts
	// 完整生命周期（与 web/server 模式一致）。
	capability.NewModifyFileProvider(filepath.Join(os.TempDir(), "capability-backup"))

	// 注册工具
	executor.RegisterTools(mcpServer)
	mcp.RegisterExecutionControlTools(mcpServer, nil)

	log.Logger.Info("MCP服务器（stdio模式）已启动，等待消息...")

	// 运行 stdio 循环
	if err := mcpServer.HandleStdio(); err != nil {
		log.Logger.Error("MCP服务器运行失败", zap.Error(err))
		os.Exit(1)
	}
}
