package app

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cyberstrike-ai/internal/agent"
	"cyberstrike-ai/internal/audit"
	"cyberstrike-ai/internal/blackboard"
	"cyberstrike-ai/internal/c2"
	"cyberstrike-ai/internal/capability"
	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/handler"
	"cyberstrike-ai/internal/hitl"
	"cyberstrike-ai/internal/knowledge"
	"cyberstrike-ai/internal/logger"
	"cyberstrike-ai/internal/mcp"
	"cyberstrike-ai/internal/metrics"
	"cyberstrike-ai/internal/monitor"
	"cyberstrike-ai/internal/multiagent"
	"cyberstrike-ai/internal/pluginslot"
	"cyberstrike-ai/internal/reactions"
	"cyberstrike-ai/internal/security"
	"cyberstrike-ai/internal/securityevents"
	"cyberstrike-ai/internal/skillpackage"
	"cyberstrike-ai/internal/storage"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// App 应用
type App struct {
	config             *config.Config
	logger             *logger.Logger
	router             *gin.Engine
	mcpServer          *mcp.Server
	externalMCPMgr     *mcp.ExternalMCPManager
	agent              *agent.Agent
	executor           *security.Executor
	db                 *database.DB
	knowledgeDB        *database.DB // 知识库数据库连接（如果使用独立数据库）
	auth               *security.AuthManager
	knowledgeManager   *knowledge.Manager        // 知识库管理器（用于动态初始化）
	knowledgeRetriever *knowledge.Retriever      // 知识库检索器（用于动态初始化）
	knowledgeIndexer   *knowledge.Indexer        // 知识库索引器（用于动态初始化）
	knowledgeHandler   *handler.KnowledgeHandler // 知识库处理器（用于动态初始化）
	knowledgeGraph     *knowledge.GraphService   // 知识图谱服务（LightRAG 迁移：双层检索+增量图更新；nil=未启用）
	agentHandler       *handler.AgentHandler     // Agent处理器（用于更新知识库管理器）
	robotHandler       *handler.RobotHandler     // 机器人处理器（钉钉/飞书/企业微信等）
	robotMu            sync.Mutex                // 保护机器人长连接的 cancel
	dingCancel         context.CancelFunc        // 钉钉 Stream 取消函数，用于配置变更时重启
	larkCancel         context.CancelFunc        // 飞书长连接取消函数，用于配置变更时重启
	wechatCancel       context.CancelFunc        // 微信 iLink 长轮询取消函数
	telegramCancel     context.CancelFunc        // Telegram 长轮询取消函数
	slackCancel        context.CancelFunc        // Slack Socket Mode 取消函数
	discordCancel      context.CancelFunc        // Discord Gateway 取消函数
	qqCancel           context.CancelFunc        // QQ WebSocket 取消函数
	alertCancel        context.CancelFunc        // 漏洞提醒持久化投递 worker
	c2Manager          *c2.Manager               // C2 管理器（未启用 C2 时为 nil）
	c2Watchdog         *c2.SessionWatchdog       // C2 会话看门狗
	c2WatchdogCancel   context.CancelFunc        // 看门狗取消函数
	c2Handler          *handler.C2Handler        // C2 REST（与 Manager 生命周期同步）
	auditSvc           *audit.Service
	blackboard         *blackboard.MemoryBoard // 进程内黑板（Agent 共享 findings）
	reactionsEngine    *reactions.Engine       // K2：反应式安全事件引擎（nil=未启用）
}

// New 创建新应用
func New(cfg *config.Config, log *logger.Logger, configPath string) (*App, error) {
	if err := multiagent.InitADK(); err != nil {
		return nil, fmt.Errorf("初始化 Eino ADK: %w", err)
	}

	gin.SetMode(gin.ReleaseMode)
	router := gin.Default()

	// 默认不信任任何代理，ClientIP() 直接用 TCP RemoteAddr，忽略 X-Forwarded-For。
	// 这样伪造 XFF 无法绕过登录限流，审计 IP 也可信。
	// 若部署在已知反向代理后，应在配置里显式声明该反代 IP（见 server.trusted_proxies）。
	_ = router.SetTrustedProxies(nil)

	// CORS中间件
	router.Use(corsMiddleware(cfg.Server.CORSAllowedOrigins))

	// 安全响应头：nosniff / DENY / Referrer-Policy / Permissions-Policy / CSP；HTTPS 时附加 HSTS
	router.Use(security.SecureHeaders(config.MainWebUIUsesHTTPS(&cfg.Server)))

	// 静态资源长缓存：/static/js、/static/css 等 ?v= 带版本号，内容变即版本号变，
	// 可安全长缓存（1 年 immutable），显著减少首屏后回访的静态资源往返请求。
	router.Use(security.StaticCacheHeaders())

	// Prometheus 指标：注册指标 + 暴露 /metrics 端点 + HTTP 请求计数/耗时中间件。
	// /metrics 是公开端点（不走 RBAC），生产环境应在反向代理层加 IP 白名单或 basic auth。
	if cfg.Metrics.EnabledEffective() {
		metrics.Register()
		metricsPath := cfg.Metrics.PathEffective()
		router.GET(metricsPath, gin.WrapH(metrics.Handler()))
		// HTTP 中间件：记录请求计数 + 耗时。path label 用 FullPath() 路由模板，
		// 避免把 /conversations/:id 这类参数展开成高基数 label。
		router.Use(func(c *gin.Context) {
			token := metrics.BeginHTTP(c.Request.Method, c.FullPath())
			c.Next()
			metrics.EndHTTP(token, c.Writer.Status())
		})
		log.Logger.Info("已启用 Prometheus 指标端点", zap.String("path", metricsPath))
	} else {
		log.Logger.Info("Prometheus 指标端点已关闭（metrics.enabled=false）")
	}

	// 初始化进程内黑板（Agent 共享 findings）
	board := blackboard.NewMemoryBoard(log.Logger)

	// 初始化数据库
	dbPath := cfg.Database.Path
	if dbPath == "" {
		dbPath = "data/conversations.db"
	}

	// 统一 home 目录迁移（K4 默认接入 + Critic C1 修复：迁走必读回）。
	// home 来源：YAML storage.home_dir 显式配置 > $CYBERSTRIKEAI_HOME > $HOME/.cyberstrikeai
	//（Load 尾部已把空值回退填充到 cfg.Storage.HomeDir，见 config.go K4 注释）。
	// 迁移语义：data/ 内容 move-if-missing 到 home（幂等可重试）；迁移成功后把
	// dbPath/知识库路径重定向到 <home>/<base>——与 MigrateLegacyData 的落点一致，
	// 确保"迁走的库从 home 读回"，不产生孤儿数据（Critic C1）。迁移失败回退原
	// data 目录继续跑（不阻断启动）。
	if homeDir := strings.TrimSpace(cfg.Storage.HomeDir); homeDir != "" {
		legacyDataDir := filepath.Dir(dbPath)
		if legacyDataDir == "" || legacyDataDir == "." {
			legacyDataDir = "data"
		}
		if err := storage.EnsureHome(homeDir); err != nil {
			return nil, fmt.Errorf("创建 storage home 目录失败: %w", err)
		}
		if err := storage.MigrateLegacyData(legacyDataDir, homeDir); err != nil {
			log.Logger.Warn("统一 home 目录迁移失败（继续使用原 data 目录）", zap.String("legacy", legacyDataDir), zap.String("home", homeDir), zap.Error(err))
		} else {
			log.Logger.Info("已执行统一 home 目录迁移", zap.String("legacy", legacyDataDir), zap.String("home", homeDir))
			// Critic C1 修复：迁移成功后重定向库路径到 home，与迁移落点一致。
			dbPath = filepath.Join(homeDir, filepath.Base(dbPath))
			if kbp := strings.TrimSpace(cfg.Database.KnowledgeDBPath); kbp != "" {
				cfg.Database.KnowledgeDBPath = filepath.Join(homeDir, filepath.Base(kbp))
			}
		}
	}

	// 确保目录存在
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, fmt.Errorf("创建数据库目录失败: %w", err)
	}

	db, err := database.NewDB(dbPath, log.Logger)
	if err != nil {
		return nil, fmt.Errorf("初始化数据库失败: %w", err)
	}

	// 认证管理器（数据库初始化后挂载 RBAC）
	authManager := security.NewAuthManager(cfg.Auth.SessionDurationHours)
	// 本地单机模式：免登录免 RBAC，内置 admin 全权限身份。仅供桌面版/本地部署，暴露公网前必须关闭。
	if cfg.Auth.LocalMode {
		authManager.SetLocalMode(true)
		log.Logger.Info("已启用本地免登录模式（local_mode），所有 API 以内置 admin 全权限身份执行；暴露到公网前请关闭")
		// G1 防护：local_mode 绑定非回环地址时强制改绑 127.0.0.1，防止免登录的
		// admin 全权限 API 意外暴露到局域网/公网。
		// 显式逃生口 = CYBERSTRIKE_ALLOW_NONLOOPBACK_LOCALMODE=1（语义明确的白名单变量，
		// 而非复用桌面壳的 NO_AUTO_OPEN——后者任何进程都可设，等于解除防护）。
		host := strings.ToLower(strings.TrimSpace(cfg.Server.Host))
		loopback := map[string]bool{"127.0.0.1": true, "localhost": true, "::1": true, "[::1]": true}
		explicitAllow := os.Getenv("CYBERSTRIKE_ALLOW_NONLOOPBACK_LOCALMODE") == "1"
		if host != "" && !loopback[host] && !explicitAllow {
			if explicitAllow {
				log.Logger.Warn("local_mode 绑定非回环地址：CYBERSTRIKE_ALLOW_NONLOOPBACK_LOCALMODE=1 已显式放行，请确保有网络层访问控制",
					zap.String("host", cfg.Server.Host))
			} else {
				log.Logger.Warn("local_mode 已开启但服务绑定到非回环地址，存在公网暴露风险！将强制改绑 127.0.0.1",
					zap.String("original_host", cfg.Server.Host))
			}
			cfg.Server.Host = "127.0.0.1"
		}
	}
	if generatedPassword, err := authManager.AttachRBACStore(db); err != nil {
		return nil, fmt.Errorf("初始化RBAC失败: %w", err)
	} else if generatedPassword != "" && !cfg.Auth.LocalMode {
		config.PrintBootstrapAdminPassword(generatedPassword)
	}
	for platform, userID := range cfg.Robots.ServiceAccountUserIDs() {
		user, userErr := db.GetRBACUserByID(userID)
		if userErr != nil || !user.Enabled {
			return nil, fmt.Errorf("robots.%s.auth.service_user_id 必须指向已启用的 RBAC 用户", platform)
		}
	}

	auditSvc := audit.NewService(db, cfg, log.Logger)
	audit.RegisterConversationCreateHook(auditSvc)
	auditSvc.PurgeExpired()
	audit.StartRetentionLoop(auditSvc, log.Logger)
	if err := db.PurgeWorkflowPackageLifecycle(time.Now().UTC()); err != nil {
		log.Logger.Warn("清理过期工作流包记录失败", zap.Error(err))
	}
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			if err := db.PurgeWorkflowPackageLifecycle(time.Now().UTC()); err != nil {
				log.Logger.Warn("清理过期工作流包记录失败", zap.Error(err))
			}
		}
	}()

	monitorRetention := monitor.NewService(db, cfg, log.Logger)
	monitorRetention.PurgeExpired()
	monitor.StartRetentionLoop(monitorRetention, log.Logger)

	if err := handler.NewHITLManager(db, log.Logger).EnsureSchema(); err != nil {
		log.Logger.Warn("初始化 HITL 表失败", zap.Error(err))
	}
	hitlRetention := hitl.NewService(db, cfg, log.Logger)
	hitlRetention.PurgeExpired()
	hitl.StartRetentionLoop(hitlRetention, log.Logger)

	// 创建MCP服务器（带数据库持久化）
	mcpServer := mcp.NewServerWithStorage(log.Logger, db)
	mcpServer.SetToolAuthorizer(mcpToolAuthorizer(db))
	mcpServer.ConfigureHTTPToolCallTimeoutFromAgentMinutes(cfg.Agent.ToolTimeoutMinutes)
	mcpServer.ConfigureToolWaitTimeoutSeconds(cfg.Agent.ToolWaitTimeoutSeconds)
	mcpServer.ConfigureToolResultMaxBytes(cfg.MultiAgent.EinoMiddleware.ReductionMaxLengthForTruncEffective())
	mcpServer.ConfigureToolResultSpillRoot(cfg.MultiAgent.EinoMiddleware.ReductionRootDir)

	// 创建安全工具执行器
	executor := security.NewExecutor(&cfg.Security, mcpServer, log.Logger)
	executor.SetShellNoOutputTimeoutSeconds(cfg.Agent.ShellNoOutputTimeoutSeconds)
	executor.SetToolOutputMaxBytes(cfg.MultiAgent.EinoMiddleware.ReductionMaxLengthForTruncEffective())
	executor.SetToolOutputSpillRoot(cfg.MultiAgent.EinoMiddleware.ReductionRootDir)

	// J5：注册破坏性工具的 Capability Provider 生命周期（plan/validate/execute/rollback）。
	// modify-file 等 HIGH_IMPACT 工具经 executor.ExecuteTool 时走完整生命周期；
	// Eino 内置 write_file/edit_file 由 multiagent.filesystemCapabilityGuard 映射到同一 provider。
	// 备份目录：reduction_root_dir 非空用其下 capability-backup；空则 tmp/reduction/capability-backup
	// （对齐 tooloutput.SpillOpts 的空值兜底语义，避免落在进程 CWD）。
	capabilityBackupDir := ""
	if root := strings.TrimSpace(cfg.MultiAgent.EinoMiddleware.ReductionRootDir); root != "" {
		capabilityBackupDir = filepath.Join(root, "capability-backup")
	} else {
		capabilityBackupDir = filepath.Join(os.TempDir(), "reduction", "capability-backup")
	}
	capability.NewModifyFileProvider(capabilityBackupDir)

	// 注册工具
	executor.RegisterTools(mcpServer)

	// 注册漏洞记录工具
	registerVulnerabilityTools(mcpServer, db, log.Logger)
	registerAssetTools(mcpServer, db, log.Logger)
	registerProjectFactTools(mcpServer, db, cfg, log.Logger)
	registerVisionTools(mcpServer, cfg, log.Logger)

	// 创建外部MCP管理器（使用与内部MCP服务器相同的存储）
	externalMCPMgr := mcp.NewExternalMCPManagerWithStorage(log.Logger, db)
	externalMCPMgr.SetToolAuthorizer(externalMCPToolAuthorizer())
	externalMCPMgr.ConfigureToolWaitTimeoutSeconds(cfg.Agent.ToolWaitTimeoutSeconds)
	externalMCPMgr.ConfigureToolResultMaxBytes(cfg.MultiAgent.EinoMiddleware.ReductionMaxLengthForTruncEffective())
	externalMCPMgr.ConfigureToolResultSpillRoot(cfg.MultiAgent.EinoMiddleware.ReductionRootDir)
	externalMCPMgr.ConfigureResilience(mcp.ExternalMCPResilienceConfig{
		MaxConcurrentPerServer:  cfg.Agent.ExternalMCPMaxConcurrentPerServer,
		MaxConcurrentTotal:      cfg.Agent.ExternalMCPMaxConcurrentTotal,
		CircuitFailureThreshold: cfg.Agent.ExternalMCPCircuitFailureThreshold,
		CircuitCooldown:         time.Duration(cfg.Agent.ExternalMCPCircuitCooldownSeconds) * time.Second,
	})
	mcp.RegisterExecutionControlTools(mcpServer, externalMCPMgr)
	if cfg.ExternalMCP.Servers != nil {
		externalMCPMgr.LoadConfigs(&cfg.ExternalMCP)
		// 启动所有启用的外部MCP客户端
		externalMCPMgr.StartAllEnabled()
	}

	execReconciler := monitor.NewExecutionReconciler(db, mcpServer, externalMCPMgr, log.Logger)
	execReconciler.ReconcileOnStartup()
	monitor.StartStaleRunningReconcileLoop(execReconciler, log.Logger)

	// 创建Agent
	maxIterations := cfg.Agent.MaxIterations
	if maxIterations <= 0 {
		maxIterations = 30 // 默认值
	}
	agent := agent.NewAgent(&cfg.OpenAI, &cfg.Agent, mcpServer, externalMCPMgr, log.Logger, maxIterations)
	agent.UpdateToolDescriptionMode(cfg.Security.ToolDescriptionMode)

	// 初始化知识库模块（如果启用）
	var knowledgeManager *knowledge.Manager
	var knowledgeRetriever *knowledge.Retriever
	var knowledgeIndexer *knowledge.Indexer
	var knowledgeHandler *handler.KnowledgeHandler
	var knowledgeGraph *knowledge.GraphService

	var knowledgeDBConn *database.DB
	log.Logger.Debug("检查知识库配置", zap.Bool("enabled", cfg.Knowledge.Enabled))
	if cfg.Knowledge.Enabled {
		// 确定知识库数据库路径
		knowledgeDBPath := cfg.Database.KnowledgeDBPath
		var knowledgeDB *sql.DB

		if knowledgeDBPath != "" {
			// 使用独立的知识库数据库
			// 确保目录存在
			if err := os.MkdirAll(filepath.Dir(knowledgeDBPath), 0755); err != nil {
				return nil, fmt.Errorf("创建知识库数据库目录失败: %w", err)
			}

			var err error
			knowledgeDBConn, err = database.NewKnowledgeDB(knowledgeDBPath, log.Logger)
			if err != nil {
				return nil, fmt.Errorf("初始化知识库数据库失败: %w", err)
			}
			knowledgeDB = knowledgeDBConn.DB
			log.Logger.Info("使用独立的知识库数据库", zap.String("path", knowledgeDBPath))
		} else {
			// 向后兼容：使用会话数据库
			knowledgeDB = db.DB
			log.Logger.Info("使用会话数据库存储知识库数据（建议配置knowledge_db_path以分离数据）")
		}

		// 创建知识库管理器
		knowledgeManager = knowledge.NewManager(knowledgeDB, cfg.Knowledge.BasePath, log.Logger)

		// 创建嵌入器
		// 使用OpenAI配置的API Key（如果知识库配置中没有指定）
		if cfg.Knowledge.Embedding.APIKey == "" {
			cfg.Knowledge.Embedding.APIKey = cfg.OpenAI.APIKey
		}
		if cfg.Knowledge.Embedding.BaseURL == "" {
			cfg.Knowledge.Embedding.BaseURL = cfg.OpenAI.BaseURL
		}

		embedder, err := knowledge.NewEmbedder(context.Background(), &cfg.Knowledge, &cfg.OpenAI, log.Logger)
		if err != nil {
			return nil, fmt.Errorf("初始化知识库嵌入器失败: %w", err)
		}

		// 创建检索器（Eino MultiQuery + 重排流水线）
		retrievalConfig := knowledge.RetrievalConfigFromYAML(cfg.Knowledge.Retrieval)
		knowledgeRetriever = knowledge.NewRetriever(knowledgeDB, embedder, retrievalConfig, log.Logger)
		if err := knowledge.WireRetrieverPipeline(context.Background(), knowledgeRetriever, &cfg.OpenAI); err != nil {
			return nil, fmt.Errorf("初始化知识库检索流水线失败: %w", err)
		}

		// 创建索引器（Eino Compose 链）
		knowledgeIndexer, err = knowledge.NewIndexer(context.Background(), knowledgeDB, embedder, log.Logger, &cfg.Knowledge)
		if err != nil {
			return nil, fmt.Errorf("初始化知识库索引器失败: %w", err)
		}

		// 注册知识检索工具到MCP服务器
		knowledge.RegisterKnowledgeTool(mcpServer, knowledgeRetriever, knowledgeManager, log.Logger)

		// 创建知识库API处理器
		knowledgeHandler = handler.NewKnowledgeHandler(knowledgeManager, knowledgeRetriever, knowledgeIndexer, db, log.Logger)
		knowledgeHandler.SetAudit(auditSvc)
		log.Logger.Info("知识库模块初始化完成", zap.Bool("handler_created", knowledgeHandler != nil))

		// 知识图谱（LightRAG 迁移）：图存储后端可换 + 双层检索 + 增量图更新。
		// cfg.Knowledge.Graph.Enabled 默认 false，保持纯向量检索；启用后构造 GraphService 并接入索引/检索链路。
		if cfg.Knowledge.Graph.Enabled {
			// LLM 抽取器工厂：UseLLMExtractor=true 时注入（当前无内置实现，返回 nil 走启发式兜底）。
			llmFactory := func() knowledge.LLMGraphExtractor { return nil }
			kg, gerr := knowledge.NewGraphService(
				context.Background(),
				knowledgeDB,
				cfg.Knowledge.Graph,
				embedder,
				knowledgeIndexer,
				knowledgeRetriever,
				llmFactory,
				log.Logger,
			)
			if gerr != nil {
				log.Logger.Warn("知识图谱服务初始化失败（保持纯向量检索）", zap.Error(gerr))
			} else {
				knowledgeGraph = kg
				knowledgeHandler.SetGraphService(kg)
				log.Logger.Info("知识图谱服务已启用",
					zap.String("backend", kg.Backend()),
					zap.String("default_mode", cfg.Knowledge.Graph.EffectiveDefaultSearchMode()),
					zap.Bool("llm_extractor", cfg.Knowledge.Graph.UseLLMExtractor))
			}
		}

		// 扫描知识库并建立索引（异步）
		go func() {
			itemsToIndex, err := knowledgeManager.ScanKnowledgeBase()
			if err != nil {
				log.Logger.Warn("扫描知识库失败", zap.Error(err))
				return
			}

			// 检查是否已有索引
			hasIndex, err := knowledgeIndexer.HasIndex()
			if err != nil {
				log.Logger.Warn("检查索引状态失败", zap.Error(err))
				return
			}

			if hasIndex {
				// 如果已有索引，只索引新添加或更新的项
				if len(itemsToIndex) > 0 {
					log.Logger.Info("检测到已有知识库索引，开始增量索引", zap.Int("count", len(itemsToIndex)))
					ctx := context.Background()
					consecutiveFailures := 0
					var firstFailureItemID string
					var firstFailureError error
					failedCount := 0

					for _, itemID := range itemsToIndex {
						if err := knowledgeIndexer.IndexItem(ctx, itemID); err != nil {
							failedCount++
							consecutiveFailures++

							if consecutiveFailures == 1 {
								firstFailureItemID = itemID
								firstFailureError = err
								log.Logger.Warn("索引知识项失败", zap.String("itemId", itemID), zap.Error(err))
							}

							// 如果连续失败2次，立即停止增量索引
							if consecutiveFailures >= 2 {
								log.Logger.Error("连续索引失败次数过多，立即停止增量索引",
									zap.Int("consecutiveFailures", consecutiveFailures),
									zap.Int("totalItems", len(itemsToIndex)),
									zap.String("firstFailureItemId", firstFailureItemID),
									zap.Error(firstFailureError),
								)
								break
							}
							continue
						}

						// 成功时重置连续失败计数
						if consecutiveFailures > 0 {
							consecutiveFailures = 0
							firstFailureItemID = ""
							firstFailureError = nil
						}

						// 知识图谱增量索引：向量索引成功后，补建图索引（抽取实体/关系→写图→写图向量）。
						// 失败仅 Warn，不阻断向量索引主流程（图索引是增强层，非关键路径）。
						if knowledgeGraph != nil {
							if _, gerr := knowledgeGraph.IndexItem(ctx, itemID); gerr != nil {
								log.Logger.Warn("知识图谱增量索引失败",
									zap.String("itemId", itemID), zap.Error(gerr))
							}
						}
					}
					log.Logger.Info("增量索引完成", zap.Int("totalItems", len(itemsToIndex)), zap.Int("failedCount", failedCount))
				} else {
					log.Logger.Info("检测到已有知识库索引，没有需要索引的新项或更新项")
				}
				return
			}

			// 冷启动：仅为尚无向量的知识项构建索引（与 IndexMissing 语义一致）
			log.Logger.Info("未检测到知识库索引，开始自动构建索引")
			ctx := context.Background()
			if err := knowledgeIndexer.IndexMissing(ctx); err != nil {
				log.Logger.Warn("自动构建知识库索引失败", zap.Error(err))
			}

			// 知识图谱冷启动：补齐尚无图向量的知识项（与向量索引解耦，独立跑一遍）。
			if knowledgeGraph != nil {
				go func() {
					if gerr := knowledgeGraph.IndexMissing(context.Background()); gerr != nil {
						log.Logger.Warn("知识图谱冷启动补齐失败", zap.Error(gerr))
					}
				}()
			}
		}()
	}

	// 配置文件路径必须由入口传入（与 flag -config 一致）。勿再用 os.Args[1]，否则 ./cyberstrike-ai --https 会把 --https 当成路径。
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		configPath = "config.yaml"
	}

	skillsDir := skillpackage.SkillsRootFromConfig(cfg.SkillsDir, configPath)
	log.Logger.Debug("Skills 目录（Eino ADK skill 中间件 + Web 管理 API）", zap.String("skillsDir", skillsDir))
	configDir := filepath.Dir(configPath)
	// skill 供应链双闸：skills-lock.json 存在则 Verify（篡改/缺失/未锁定只 Warn 不阻断启动）
	lockPath := filepath.Join(configDir, "skills-lock.json")
	if tampered, missing, unlocked, err := skillpackage.VerifyLock(skillsDir, lockPath); err == nil {
		if n := len(tampered) + len(missing) + len(unlocked); n > 0 {
			log.Logger.Warn("skill 供应链锁校验发现违规（不阻断启动，请运行 make skills-lock 刷新）",
				zap.String("锁文件", lockPath),
				zap.String("违规", skillpackage.FormatViolations(tampered, missing, unlocked)))
		}
	} else if !os.IsNotExist(err) { // 锁文件读不了/格式坏才 Warn；不存在视为首装无锁
		log.Logger.Warn("skill 供应链锁校验失败", zap.String("锁文件", lockPath), zap.Error(err))
	}
	plantaskRel := strings.TrimSpace(cfg.MultiAgent.EinoMiddleware.PlantaskRelDir)
	if plantaskRel == "" {
		plantaskRel = ".eino/plantask"
	}
	plantaskBase := filepath.Join(skillsDir, plantaskRel)
	// Match eino_adk_run_loop: checkpoint_dir is used as configured (relative to process CWD when not absolute).
	checkpointBase := strings.TrimSpace(cfg.MultiAgent.EinoMiddleware.CheckpointDir)
	reductionRoot := strings.TrimSpace(cfg.MultiAgent.EinoMiddleware.ReductionRootDir)
	workspaceRoot := strings.TrimSpace(cfg.Agent.WorkspaceRootDir)
	db.SetEinoConversationDirs(plantaskBase, checkpointBase, reductionRoot, workspaceRoot)
	agent.SetPromptBaseDir(configDir)

	agentsDir := cfg.AgentsDir
	if agentsDir == "" {
		agentsDir = "agents"
	}
	if !filepath.IsAbs(agentsDir) {
		agentsDir = filepath.Join(configDir, agentsDir)
	}
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		log.Logger.Warn("创建 agents 目录失败", zap.String("path", agentsDir), zap.Error(err))
	}
	markdownAgentsHandler := handler.NewMarkdownAgentsHandler(agentsDir)
	markdownAgentsHandler.SetAudit(auditSvc)
	markdownAgentsHandler.SetLogger(log.Logger)
	log.Logger.Debug("多代理 Markdown 子 Agent 目录", zap.String("agentsDir", agentsDir))

	// 创建处理器
	agentHandler := handler.NewAgentHandler(agent, db, cfg, log.Logger)
	agentHandler.SetAudit(auditSvc)
	agentHandler.SetAgentsMarkdownDir(agentsDir)
	// J4：把会话→projectID 解析器注入 agent。工具执行 ctx 注入 projectID，
	// executor 据此读 projects.scope_json 做会话级授权范围硬拦（越界目标不执行）。
	agent.SetProjectScopeResolver(agentHandler.ConversationProjectID)
	// 如果知识库已启用，设置知识库管理器到AgentHandler以便记录检索日志
	if knowledgeManager != nil {
		agentHandler.SetKnowledgeManager(knowledgeManager)
	}
	monitorHandler := handler.NewMonitorHandler(mcpServer, executor, db, log.Logger)
	monitorHandler.SetAudit(auditSvc)
	monitorHandler.SetMonitorRetention(monitorRetention)
	monitorHandler.SetExternalMCPManager(externalMCPMgr) // 设置外部MCP管理器，以便获取外部MCP执行记录
	monitorHandler.SetTaskManager(agentHandler.TaskManager())
	monitorHandler.SetAgentHandler(agentHandler)
	notificationHandler := handler.NewNotificationHandler(db, agentHandler, log.Logger)
	authHandler := handler.NewAuthHandler(authManager, cfg, configPath, log.Logger)
	authHandler.SetAudit(auditSvc)
	attackChainHandler := handler.NewAttackChainHandler(db, &cfg.OpenAI, log.Logger)
	attackChainHandler.SetBlackboard(board)
	vulnerabilityHandler := handler.NewVulnerabilityHandler(db, log.Logger)
	assetHandler := handler.NewAssetHandler(db, log.Logger)
	projectHandler := handler.NewProjectHandler(db, log.Logger)
	rbacHandler := handler.NewRBACHandler(db, log.Logger)
	rbacHandler.SetAudit(auditSvc)
	rbacHandler.SetAuthManager(authManager)
	workflowHandler := handler.NewWorkflowHandler(db, log.Logger)
	workflowHandler.SetAudit(auditSvc)
	workflowHandler.SetRuntime(agent, cfg)
	vulnerabilityHandler.SetAudit(auditSvc)
	// 漏洞赏金与 ROI 处理器：复用 vulnerabilityHandler 的 db，保证筛选与 RBAC 语义一致。
	bugBountyHandler := handler.NewBugBountyHandler(vulnerabilityHandler, log.Logger)
	webshellHandler := handler.NewWebShellHandler(log.Logger, db, cfg.Security.WebshellAllowPrivateIP)
	webshellHandler.SetAudit(auditSvc)
	chatUploadsHandler := handler.NewChatUploadsHandler(log.Logger, db)
	chatUploadsHandler.SetAudit(auditSvc)
	registerWebshellTools(mcpServer, db, webshellHandler, log.Logger)
	registerWebshellManagementTools(mcpServer, db, webshellHandler, log.Logger)
	configHandler := handler.NewConfigHandler(configPath, cfg, mcpServer, executor, agent, attackChainHandler, externalMCPMgr, log.Logger)
	configHandler.SetDB(db)
	configHandler.SetAudit(auditSvc)
	agentHandler.SetHitlToolWhitelistSaver(configHandler)
	agentHandler.SetHitlAuditStrategySaver(configHandler)
	agentHandler.SetHitlDefaultReviewerSaver(configHandler)
	// HIGH_IMPACT 第二道标记闸：把 HITL 白名单判定 + 审计记录器注入 executor。
	// executor 命中 HighImpactTools 时调 IsToolWhitelisted 反查免审批白名单，
	// 非白名单则记一条 platform audit（不阻断执行）。
	executor.SetHITLWhitelist(securityHighImpactWhitelistAdapter{handler: agentHandler})
	executor.SetHighImpactAuditRecorder(securityHighImpactAuditAdapter{svc: auditSvc})
	// J4 project 级授权边界硬闸：注入 project scope 解析器。executor 执行工具前按
	// ctx 里的 projectID 读 projects.scope_json，越界目标返回 IsError 不执行。
	executor.SetProjectScopeResolver(securityProjectScopeResolver{db: db})
	externalMCPHandler := handler.NewExternalMCPHandler(externalMCPMgr, cfg, configPath, log.Logger)
	externalMCPHandler.SetAudit(auditSvc)
	roleHandler := handler.NewRoleHandler(cfg, configPath, log.Logger)
	roleHandler.SetAudit(auditSvc)
	skillsHandler := handler.NewSkillsHandler(cfg, configPath, log.Logger)
	skillsHandler.SetAudit(auditSvc)
	fofaHandler := handler.NewFofaHandler(cfg, log.Logger)
	terminalHandler := handler.NewTerminalHandler(log.Logger)
	if db != nil {
		skillsHandler.SetDB(db) // 设置数据库连接以便获取调用统计
	}

	// ============================================================================
	// 初始化 C2 模块（可按配置关闭，节省本机部署资源）
	// ============================================================================
	c2Manager, c2Watchdog, watchdogCancel := setupC2Runtime(cfg, db, agentHandler, log.Logger)
	if c2Manager != nil {
		registerC2Tools(mcpServer, c2Manager, log.Logger, cfg.Server.Port)
	}
	c2Handler := handler.NewC2Handler(c2Manager, log.Logger)
	c2Handler.SetAudit(auditSvc)

	// 创建OpenAPI处理器
	conversationHandler := handler.NewConversationHandler(db, log.Logger)
	conversationHandler.SetAudit(auditSvc)
	conversationHandler.SetTaskStopper(agentHandler)
	conversationHandler.SetTaskStateProvider(agentHandler)
	auditHandler := handler.NewAuditHandler(db, auditSvc, log.Logger)
	robotHandler := handler.NewRobotHandler(cfg, db, agentHandler, log.Logger)
	robotHandler.SetAudit(auditSvc)
	db.SetVulnerabilityCreatedHook(robotHandler.NotifyNewVulnerability)
	openAPIHandler := handler.NewOpenAPIHandler(db, log.Logger, conversationHandler, agentHandler)

	// 创建 App 实例（部分字段稍后填充）
	app := &App{
		config:             cfg,
		logger:             log,
		router:             router,
		mcpServer:          mcpServer,
		externalMCPMgr:     externalMCPMgr,
		agent:              agent,
		executor:           executor,
		db:                 db,
		knowledgeDB:        knowledgeDBConn,
		auth:               authManager,
		knowledgeManager:   knowledgeManager,
		knowledgeRetriever: knowledgeRetriever,
		knowledgeIndexer:   knowledgeIndexer,
		knowledgeHandler:   knowledgeHandler,
		knowledgeGraph:     knowledgeGraph,
		agentHandler:       agentHandler,
		robotHandler:       robotHandler,
		c2Manager:          c2Manager,
		c2Watchdog:         c2Watchdog,
		c2WatchdogCancel:   watchdogCancel,
		c2Handler:          c2Handler,
		auditSvc:           auditSvc,
		blackboard:         board,
	}
	// 飞书/钉钉长连接（无需公网），启用时在后台启动；后续前端应用配置时会通过 RestartRobotConnections 重启
	app.startRobotConnections()
	alertCtx, alertCancel := context.WithCancel(context.Background())
	app.alertCancel = alertCancel
	go robotHandler.RunVulnerabilityAlertWorker(alertCtx)

	// 设置漏洞工具注册器（内置工具，必须设置）
	vulnerabilityRegistrar := func() error {
		registerVulnerabilityTools(mcpServer, db, log.Logger)
		registerAssetTools(mcpServer, db, log.Logger)
		registerProjectFactTools(mcpServer, db, cfg, log.Logger)
		registerVisionTools(mcpServer, cfg, log.Logger)
		return nil
	}
	configHandler.SetVulnerabilityToolRegistrar(vulnerabilityRegistrar)

	// 设置 WebShell 工具注册器（ApplyConfig 时重新注册）
	webshellRegistrar := func() error {
		registerWebshellTools(mcpServer, db, webshellHandler, log.Logger)
		registerWebshellManagementTools(mcpServer, db, webshellHandler, log.Logger)
		return nil
	}
	configHandler.SetWebshellToolRegistrar(webshellRegistrar)

	// Skills 由 Eino ADK skill 中间件提供（多代理）；此处不注册 MCP 形态的技能工具
	configHandler.SetSkillsToolRegistrar(func() error { return nil })

	handler.RegisterBatchTaskMCPTools(mcpServer, agentHandler, log.Logger)
	batchTaskToolRegistrar := func() error {
		handler.RegisterBatchTaskMCPTools(mcpServer, agentHandler, log.Logger)
		return nil
	}
	configHandler.SetBatchTaskToolRegistrar(batchTaskToolRegistrar)

	// 设置知识库初始化器（用于动态初始化，需要在 App 创建后设置）
	configHandler.SetKnowledgeInitializer(func() (*handler.KnowledgeHandler, error) {
		knowledgeHandler, err := initializeKnowledge(cfg, db, knowledgeDBConn, mcpServer, agentHandler, app, log.Logger)
		if err != nil {
			return nil, err
		}

		// 动态初始化后，设置知识库工具注册器和检索器更新器
		// 这样后续 ApplyConfig 时就能重新注册工具了
		if app.knowledgeRetriever != nil && app.knowledgeManager != nil {
			// 创建闭包，捕获knowledgeRetriever和knowledgeManager的引用
			registrar := func() error {
				knowledge.RegisterKnowledgeTool(mcpServer, app.knowledgeRetriever, app.knowledgeManager, log.Logger)
				return nil
			}
			configHandler.SetKnowledgeToolRegistrar(registrar)
			// 设置检索器更新器，以便在ApplyConfig时更新检索器配置
			configHandler.SetRetrieverUpdater(app.knowledgeRetriever)
			log.Logger.Info("动态初始化后已设置知识库工具注册器和检索器更新器")
		}

		return knowledgeHandler, nil
	})

	// 如果知识库已启用，设置知识库工具注册器和检索器更新器
	if cfg.Knowledge.Enabled && knowledgeRetriever != nil && knowledgeManager != nil {
		// 创建闭包，捕获knowledgeRetriever和knowledgeManager的引用
		registrar := func() error {
			knowledge.RegisterKnowledgeTool(mcpServer, knowledgeRetriever, knowledgeManager, log.Logger)
			return nil
		}
		configHandler.SetKnowledgeToolRegistrar(registrar)
		// 设置检索器更新器，以便在ApplyConfig时更新检索器配置
		configHandler.SetRetrieverUpdater(knowledgeRetriever)
	}

	// 设置机器人连接重启器，前端应用配置后无需重启服务即可使钉钉/飞书/微信新配置生效
	configHandler.SetRobotRestarter(app)

	wechatRobotHandler := handler.NewWechatRobotHandler(cfg, configHandler, log.Logger)

	configHandler.SetC2Runtime(app)
	configHandler.SetC2ToolRegistrar(func() error {
		if app.config.C2.EnabledEffective() && app.c2Manager != nil {
			registerC2Tools(mcpServer, app.c2Manager, log.Logger, app.config.Server.Port)
		}
		return nil
	})

	// 设置路由（使用 App 实例以便动态获取 handler）
	setupRoutes(
		router,
		authHandler,
		agentHandler,
		monitorHandler,
		notificationHandler,
		conversationHandler,
		robotHandler,
		wechatRobotHandler,
		configHandler,
		externalMCPHandler,
		attackChainHandler,
		app, // 传递 App 实例以便动态获取 knowledgeHandler
		vulnerabilityHandler,
		assetHandler,
		projectHandler,
		workflowHandler,
		webshellHandler,
		chatUploadsHandler,
		roleHandler,
		skillsHandler,
		markdownAgentsHandler,
		fofaHandler,
		terminalHandler,
		app.c2Handler,
		auditHandler,
		auditSvc,
		rbacHandler,
		mcpServer,
		authManager,
		openAPIHandler,
		bugBountyHandler,
		configPath,
	)

	// K2：接线 reactions 引擎（移植自 agent-orchestrator reactions）。
	// 订阅 blackboard 事件流，匹配 Finding.Type 触发 notify/send-to-agent/log-only。
	// 安全事件源（HIGH_IMPACT/scope 拦截/capability 回滚）由 securityEventBoardAdapter
	// Publish 到 board（见 securityEventBoardAdapter 定义，已注入 executor/capability 审计链）。
	// 向后兼容：cfg.Reactions.EnabledEffective()=false 时引擎不 Start（仅构造）。
	//
	// K1：notifiers 从 pluginslot.Registry 取已注册 Notifier 实例（desktop/webhook 经
	// init() 自注册，H2 修复）。webhook 的 URL/Secret 从 cfg.Reactions.Notifiers 注入；
	// desktop 无配置。DetectAvailable 过滤不可用的（无 osascript/notify-send 的平台跳过）。
	notifierCfg := map[string]interface{}{}
	if u := strings.TrimSpace(cfg.Reactions.WebhookURL); u != "" {
		notifierCfg["url"] = u
	}
	if s := strings.TrimSpace(cfg.Reactions.WebhookSecret); s != "" {
		notifierCfg["secret"] = s
	}
	var notifiers []pluginslot.Notifier
	for _, name := range pluginslot.DetectAvailable(pluginslot.SlotNotifier) {
		if inst := pluginslot.Get(pluginslot.SlotNotifier, name, notifierCfg); inst != nil {
			if n, ok := inst.(pluginslot.Notifier); ok {
				notifiers = append(notifiers, n)
			}
		}
	}
	reactionsEngine := reactions.New(board, cfg.Reactions, notifiers, log.Logger)
	if reactionsEngine != nil && cfg.Reactions.EnabledEffective() {
		// H1：board 注册到 securityevents（HIGH_IMPACT/scope 拦截/capability 回滚
		// 三类安全事件 → blackboard → 本引擎）。必须在 Start 前注入，保证订阅
		// 先于任何工具执行产生的事件。
		securityevents.SetBoard(board)
		reactionsEngine.Start(context.Background())
		// app.reactionsEngine 字段存引用供 Stop（见 App struct + Shutdown）
		app.reactionsEngine = reactionsEngine
	}

	return app, nil

}

// securityHighImpactWhitelistAdapter 把 AgentHandler 的 HITL 免审批判定
// 适配为 executor 期望的 hitlWhitelistChecker 接口，避免 security 包反向
// 依赖 handler 包（循环导入）。NeedsToolApproval 与会话侧 HITL 判定一致：
// 仅当该会话开启人机协同且工具不在白名单时返回 true；executor 用它判定
// 是否对 HIGH_IMPACT 工具打标（白名单内 → 不打标）。
//
// 注意：IsToolWhitelisted 语义为"是否免审批"，与会话是否启用 HITL 解耦——
// 未启用 HITL 时 NeedsToolApproval 恒为 false，此时 executor 仍会对
// HIGH_IMPACT 工具打标（保守策略，符合"第二道标记闸"定位）。
func (a securityHighImpactWhitelistAdapter) IsToolWhitelisted(conversationID, toolName string) bool {
	if a.handler == nil {
		return false
	}
	// NeedsToolApproval 返回 true 表示"需要审批"=未在白名单；
	// 反查得到"是否在白名单"。
	return !a.handler.HITLNeedsToolApproval(conversationID, toolName)
}

// securityHighImpactAuditAdapter 把 audit.Service 适配为 executor 期望的
// highImpactAuditRecorder 接口，避免 security 包反向依赖 audit 包
// （audit 已依赖 security 的 RBAC，会构成循环）。走 RecordSystem 以
// 系统身份写一条 platform audit，ClientIP/SessionHint 留空。
//
// H1：HIGH_IMPACT 事件同时经 internal/securityevents 广播到 blackboard
// （SetBoard 注入，见下方 reactions 接线段），供 reactions 引擎触发反应式通知。
// securityevents 为包级注入点，board 未启用时 no-op，security 包零依赖反转。
func (a securityHighImpactAuditAdapter) RecordHighImpactTool(actor, conversationID, toolName, risk string) {
	if a.svc == nil {
		return
	}
	detail := map[string]interface{}{
		"toolName":       toolName,
		"risk":           risk,
		"conversationId": conversationID,
	}
	a.svc.RecordSystem(audit.Entry{
		Level:        "warn",
		Category:     "security",
		Action:       "high_impact_tool",
		Result:       "success",
		Actor:        actor,
		ResourceType: "tool",
		ResourceID:   toolName,
		Message:      fmt.Sprintf("HIGH_IMPACT 工具执行: %s（%s）", toolName, risk),
		Detail:       detail,
	})
	// H1：广播安全事件给 reactions 引擎（blackboard 订阅者）。
	securityevents.PublishHighImpactTool(toolName, risk, conversationID)
}

// securityHighImpactWhitelistAdapter / securityHighImpactAuditAdapter 类型定义
// 紧随 New 之后；放在文件末尾会破坏 package 顶层声明顺序，故与相关注入点
// 同段维护。下面是它们的 struct 声明（方法已见上方）。
type securityHighImpactWhitelistAdapter struct {
	handler *handler.AgentHandler
}

type securityHighImpactAuditAdapter struct {
	svc *audit.Service
}

// securityProjectScopeResolver 把 *database.DB 适配为 executor 期望的
// projectScopeResolver 接口（J4 会话级授权边界硬闸）。按 projectID 读
// projects.scope_json 并解析成 security.Scope。db 为空或查询失败返回零值
// （不限制），保证向后兼容。
type securityProjectScopeResolver struct {
	db *database.DB
}

// ResolveProjectScope 实现 security.projectScopeResolver。
func (a securityProjectScopeResolver) ResolveProjectScope(projectID string) security.Scope {
	if a.db == nil {
		return security.Scope{}
	}
	return security.ScopeFromProject(a.db, projectID)
}

// mcpHandlerWithAuth 在鉴权通过后转发到 MCP 处理；若配置了 auth_header 则校验请求头，否则直接放行
