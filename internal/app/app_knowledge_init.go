package app

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/handler"
	"cyberstrike-ai/internal/knowledge"
	"cyberstrike-ai/internal/mcp"

	"go.uber.org/zap"
)

func initializeKnowledge(
	cfg *config.Config,
	db *database.DB,
	knowledgeDBConn *database.DB,
	mcpServer *mcp.Server,
	agentHandler *handler.AgentHandler,
	app *App, // 传递 App 引用以便更新知识库组件
	logger *zap.Logger,
) (*handler.KnowledgeHandler, error) {
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
		knowledgeDBConn, err = database.NewKnowledgeDB(knowledgeDBPath, logger)
		if err != nil {
			return nil, fmt.Errorf("初始化知识库数据库失败: %w", err)
		}
		knowledgeDB = knowledgeDBConn.DB
		logger.Info("使用独立的知识库数据库", zap.String("path", knowledgeDBPath))
	} else {
		// 向后兼容：使用会话数据库
		knowledgeDB = db.DB
		logger.Info("使用会话数据库存储知识库数据（建议配置knowledge_db_path以分离数据）")
	}

	// 创建知识库管理器
	knowledgeManager := knowledge.NewManager(knowledgeDB, cfg.Knowledge.BasePath, logger)

	// 创建嵌入器
	// 使用OpenAI配置的API Key（如果知识库配置中没有指定）
	if cfg.Knowledge.Embedding.APIKey == "" {
		cfg.Knowledge.Embedding.APIKey = cfg.OpenAI.APIKey
	}
	if cfg.Knowledge.Embedding.BaseURL == "" {
		cfg.Knowledge.Embedding.BaseURL = cfg.OpenAI.BaseURL
	}

	embedder, err := knowledge.NewEmbedder(context.Background(), &cfg.Knowledge, &cfg.OpenAI, logger)
	if err != nil {
		return nil, fmt.Errorf("初始化知识库嵌入器失败: %w", err)
	}

	// 创建检索器（Eino MultiQuery + 重排流水线）
	retrievalConfig := knowledge.RetrievalConfigFromYAML(cfg.Knowledge.Retrieval)
	knowledgeRetriever := knowledge.NewRetriever(knowledgeDB, embedder, retrievalConfig, logger)
	if err := knowledge.WireRetrieverPipeline(context.Background(), knowledgeRetriever, &cfg.OpenAI); err != nil {
		return nil, fmt.Errorf("初始化知识库检索流水线失败: %w", err)
	}

	// 创建索引器（Eino Compose 链）
	knowledgeIndexer, err := knowledge.NewIndexer(context.Background(), knowledgeDB, embedder, logger, &cfg.Knowledge)
	if err != nil {
		return nil, fmt.Errorf("初始化知识库索引器失败: %w", err)
	}

	// 注册知识检索工具到MCP服务器
	knowledge.RegisterKnowledgeTool(mcpServer, knowledgeRetriever, knowledgeManager, logger)

	// 创建知识库API处理器
	knowledgeHandler := handler.NewKnowledgeHandler(knowledgeManager, knowledgeRetriever, knowledgeIndexer, db, logger)
	if app != nil && app.auditSvc != nil {
		knowledgeHandler.SetAudit(app.auditSvc)
	}
	logger.Info("知识库模块初始化完成", zap.Bool("handler_created", knowledgeHandler != nil))

	// 设置知识库管理器到AgentHandler以便记录检索日志
	agentHandler.SetKnowledgeManager(knowledgeManager)

	// 更新 App 中的知识库组件（如果 App 不为 nil，说明是动态初始化）
	if app != nil {
		app.knowledgeManager = knowledgeManager
		app.knowledgeRetriever = knowledgeRetriever
		app.knowledgeIndexer = knowledgeIndexer
		app.knowledgeHandler = knowledgeHandler
		// 如果使用独立数据库，更新 knowledgeDB
		if knowledgeDBPath != "" {
			app.knowledgeDB = knowledgeDBConn
		}
		logger.Info("App 中的知识库组件已更新")
	}

	// 扫描知识库并建立索引（异步）
	go func() {
		itemsToIndex, err := knowledgeManager.ScanKnowledgeBase()
		if err != nil {
			logger.Warn("扫描知识库失败", zap.Error(err))
			return
		}

		// 检查是否已有索引
		hasIndex, err := knowledgeIndexer.HasIndex()
		if err != nil {
			logger.Warn("检查索引状态失败", zap.Error(err))
			return
		}

		if hasIndex {
			// 如果已有索引，只索引新添加或更新的项
			if len(itemsToIndex) > 0 {
				logger.Info("检测到已有知识库索引，开始增量索引", zap.Int("count", len(itemsToIndex)))
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
							logger.Warn("索引知识项失败", zap.String("itemId", itemID), zap.Error(err))
						}

						// 如果连续失败2次，立即停止增量索引
						if consecutiveFailures >= 2 {
							logger.Error("连续索引失败次数过多，立即停止增量索引",
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
				}
				logger.Info("增量索引完成", zap.Int("totalItems", len(itemsToIndex)), zap.Int("failedCount", failedCount))
			} else {
				logger.Info("检测到已有知识库索引，没有需要索引的新项或更新项")
			}
			return
		}

		// 冷启动：仅为尚无向量的知识项构建索引（与 IndexMissing 语义一致）
		logger.Info("未检测到知识库索引，开始自动构建索引")
		ctx := context.Background()
		if err := knowledgeIndexer.IndexMissing(ctx); err != nil {
			logger.Warn("自动构建知识库索引失败", zap.Error(err))
		}
	}()

	return knowledgeHandler, nil
}

// corsMiddleware allows same-origin requests, valid Chromium extension
// origins, and exact origins explicitly configured by the operator. CORS is
// not an authentication boundary; API access still requires a valid session.
