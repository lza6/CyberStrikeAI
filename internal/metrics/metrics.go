// Package metrics 定义平台核心 Prometheus 指标，并提供 HTTP 中间件
// 与辅助函数，供 app.go 接入 /metrics 端点。
//
// 指标设计：
//   - HTTPRequestsTotal      ：HTTP 请求计数（method/path/status）
//   - HTTPRequestDuration    ：HTTP 请求耗时直方图（method/path）
//   - ActiveSessions         ：当前活跃会话数（gauge）
//   - ToolExecutionsTotal    ：工具执行计数（tool_name/status）
//   - AgentTurnsTotal        ：Agent 轮次计数（orchestration/status）
//   - LLMTokenUsage          ：LLM token 用量（channel/type=prompt/completion）
//
// /metrics 端点默认公开（不走 RBAC）。生产环境建议在前置反向代理加
// IP 白名单或 basic auth，限制内网访问。
package metrics

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"net/http"
)

// 命名空间：所有平台指标统一前缀，避免与默认采集器冲突。
const namespace = "cyberstrike"

// 全局指标定义。用 const label key 保证一致性。
var (
	HTTPRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "http_requests_total",
			Help:      "Total number of HTTP requests by method, path and status code.",
		},
		[]string{"method", "path", "status"},
	)

	HTTPRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "http_request_duration_seconds",
			Help:      "HTTP request latency in seconds, by method and path.",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"method", "path"},
	)

	ActiveSessions = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "active_sessions",
			Help:      "Number of currently active sessions.",
		},
	)

	ToolExecutionsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "tool_executions_total",
			Help:      "Total tool executions by tool name and status (success/failure).",
		},
		[]string{"tool_name", "status"},
	)

	AgentTurnsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "agent_turns_total",
			Help:      "Total agent turns by orchestration type and status.",
		},
		[]string{"orchestration", "status"},
	)

	LLMTokenUsage = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "llm_token_usage_total",
			Help:      "LLM token usage by channel and type (prompt/completion).",
		},
		[]string{"channel", "type"},
	)
)

// Register 把所有平台指标注册到默认 Registry。
// 在 app 初始化时调用一次。重复调用会 panic，故不在 init 里做。
func Register() {
	prometheus.MustRegister(
		HTTPRequestsTotal,
		HTTPRequestDuration,
		ActiveSessions,
		ToolExecutionsTotal,
		AgentTurnsTotal,
		LLMTokenUsage,
	)
}

// Handler 返回 /metrics 端点的 http.Handler。
// 直接用 promhttp.Handler()，它会采集默认 Registry（含 Go 运行时 + 平台指标）。
func Handler() http.Handler {
	return promhttp.Handler()
}

// ---- HTTP 中间件 ----

// Middleware 记录 HTTP 请求计数 + 耗时。在 gin 引擎里用 gin.WrapF 包裹
// 会有麻烦，所以这里直接提供 gin 风格的 handler 签名供 app.go 调用。
//
// 注意：为避免高基数，path label 用 gin 的FullPath()（路由模板）而非
// 实际 URL，否则 /conversations/:id 会把每个 id 都展开成一个 label。
type httpRecorder struct {
	method string
	path   string
	start  time.Time
}

// BeginHTTP 在请求开始时调用，返回一个可在请求结束时传入 EndHTTP 的 token。
func BeginHTTP(method, path string) interface{} {
	return &httpRecorder{
		method: method,
		path:   path,
		start:  time.Now(),
	}
}

// EndHTTP 在请求结束时调用，记录计数与耗时。status 是 HTTP 状态码。
func EndHTTP(token interface{}, status int) {
	rec, ok := token.(*httpRecorder)
	if !ok || rec == nil {
		return
	}
	elapsed := time.Since(rec.start).Seconds()
	HTTPRequestsTotal.WithLabelValues(rec.method, rec.path, strconv.Itoa(status)).Inc()
	HTTPRequestDuration.WithLabelValues(rec.method, rec.path).Observe(elapsed)
}

// ---- 业务辅助函数 ----

// SetActiveSessions 设置当前活跃会话数（gauge）。
func SetActiveSessions(n int) {
	ActiveSessions.Set(float64(n))
}

// RecordToolExecution 记录一次工具执行。status 用 "success"/"failure"。
func RecordToolExecution(toolName, status string) {
	ToolExecutionsTotal.WithLabelValues(toolName, status).Inc()
}

// RecordAgentTurn 记录一次 Agent 轮次。orchestration 如 "single"/"multi"。
func RecordAgentTurn(orchestration, status string) {
	AgentTurnsTotal.WithLabelValues(orchestration, status).Inc()
}

// RecordLLMToken 记录 LLM token 用量。typ 用 "prompt"/"completion"。
func RecordLLMToken(channel, typ string, count int) {
	if count <= 0 {
		return
	}
	LLMTokenUsage.WithLabelValues(channel, typ).Add(float64(count))
}
