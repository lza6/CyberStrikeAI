package handler

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"time"

	"cyberstrike-ai/internal/blackboard"
	"cyberstrike-ai/internal/database"

	"github.com/gin-gonic/gin"
)

// HealthHandler 健康探针处理器：/healthz（liveness）与 /readyz（readiness）。
//
// 定位：基础设施探活端点，注册在公开路由区（不走鉴权/限流/审计），
// 供 Docker HEALTHCHECK / k8s 滚动更新判断进程存活与依赖就绪。
// 探针被限流误杀会导致编排器误判容器不健康并重启，故刻意绕过所有中间件。
type HealthHandler struct {
	startedAt time.Time
	version   string

	// 主数据库（会话库）；nil 时 readyz 的 db 检查标记 degraded（不阻断 /healthz）。
	db *database.DB
	// 知识库独立数据库（可能为 nil：未启用知识库或与会话库共库）。
	knowledgeDB *database.DB
	// 进程内黑板（MemoryBoard 或 SQLiteBoard）；nil 时跳过该检查。
	blackboard blackboard.Board
}

// NewHealthHandler 构造 HealthHandler。startedAt 为进程启动时间（用于 uptime）；
// version 为展示版本号，空则回退 "unknown"。
func NewHealthHandler(startedAt time.Time, version string, db *database.DB, knowledgeDB *database.DB, board blackboard.Board) *HealthHandler {
	if version == "" {
		version = "unknown"
	}
	return &HealthHandler{
		startedAt:   startedAt,
		version:     version,
		db:          db,
		knowledgeDB: knowledgeDB,
		blackboard:  board,
	}
}

// checkTimeout 单项依赖检查的超时；探针必须快速返回，
// 避免被慢依赖拖死导致编排器超时误判。
const checkTimeout = 5 * time.Second

// pingDB 对 *sql.DB 做带超时的 PingContext，返回 "ok" 或 "fail: <原因>"。
func pingDB(ctx context.Context, db *sql.DB) string {
	if db == nil {
		return "skipped"
	}
	pingCtx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		return fmt.Sprintf("fail: %v", err)
	}
	return "ok"
}

// Healthz GET /healthz — liveness：进程存活即 200，不触碰任何外部依赖。
// 响应: 200 {"status":"ok","uptime":"...","version":"..."}
func (h *HealthHandler) Healthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"uptime":  time.Since(h.startedAt).Round(time.Second).String(),
		"version": h.version,
	})
}

// Readyz GET /readyz — readiness：聚合依赖检查。
// DB Ping（5s 超时）+ 知识库（若为独立库，DB Ping 覆盖其就绪性）+
// 黑板就绪（MemoryBoard 进程内恒可用；SQLiteBoard 由 DB Ping 覆盖——
// blackboard.Board 接口无独立健康检查方法，不引入额外查询）。
// 全过 200 {"status":"ready","checks":{...}}；任一失败 503 {"status":"degraded",...}。
func (h *HealthHandler) Readyz(c *gin.Context) {
	checks := gin.H{}

	if h.db == nil {
		checks["db"] = "fail: database not configured"
	} else {
		checks["db"] = pingDB(c.Request.Context(), h.db.DB)
	}

	// 知识库：nil = 未启用或共库（共库场景由 db 检查覆盖），标 skipped 而非 degraded。
	if h.knowledgeDB == nil {
		checks["knowledge"] = "skipped"
	} else {
		checks["knowledge"] = pingDB(c.Request.Context(), h.knowledgeDB.DB)
	}

	// 黑板：MemoryBoard 无外部依赖，进程存活即就绪；
	// SQLiteBoard 的存储健康由 db Ping 覆盖（同一 SQLite 引擎语义），
	// Board 接口无健康检查方法，不做额外探测以免探针被慢查询拖死。
	if h.blackboard == nil {
		checks["blackboard"] = "skipped"
	} else {
		checks["blackboard"] = "ok"
	}

	ready := true
	for _, v := range checks {
		if v != "ok" && v != "skipped" {
			ready = false
			break
		}
	}

	if ready {
		c.JSON(http.StatusOK, gin.H{"status": "ready", "checks": checks})
		return
	}
	c.JSON(http.StatusServiceUnavailable, gin.H{"status": "degraded", "checks": checks})
}
