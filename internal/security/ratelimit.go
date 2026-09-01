package security

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// rateLimitEntry 记录某个 IP 的请求窗口信息
type rateLimitEntry struct {
	count    int
	windowAt time.Time
}

// RateLimiter 基于 IP 的滑动窗口速率限制器
type RateLimiter struct {
	mu      sync.Mutex
	entries map[string]*rateLimitEntry
	limit   int           // 窗口内允许的最大请求数
	window  time.Duration // 窗口时长
}

// NewRateLimiter 创建速率限制器
func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	rl := &RateLimiter{
		entries: make(map[string]*rateLimitEntry),
		limit:   limit,
		window:  window,
	}
	// 后台定期清理过期条目，防止内存泄漏
	go rl.cleanup()
	return rl
}

// cleanup 每分钟清理一次过期条目
func (rl *RateLimiter) cleanup() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		rl.mu.Lock()
		now := time.Now()
		for ip, entry := range rl.entries {
			if now.Sub(entry.windowAt) > rl.window {
				delete(rl.entries, ip)
			}
		}
		rl.mu.Unlock()
	}
}

// allow 检查指定 IP 是否允许通过
func (rl *RateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	entry, ok := rl.entries[ip]
	if !ok || now.Sub(entry.windowAt) > rl.window {
		rl.entries[ip] = &rateLimitEntry{count: 1, windowAt: now}
		return true
	}

	entry.count++
	return entry.count <= rl.limit
}

// RateLimitMiddleware 返回 Gin 中间件，对超限请求返回 429
func RateLimitMiddleware(rl *RateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := c.ClientIP()
		if !rl.allow(ip) {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "rate limit exceeded, please try again later",
			})
			return
		}
		c.Next()
	}
}

// globalRateLimitExemptSuffixes 流式/长连接端点按请求数限流没有意义（一次连接持续数分钟），
// 这些路径后缀的请求豁免全局限流。
var globalRateLimitExemptSuffixes = []string{"/stream", "/sse", "/ws", "/mcp"}

// isStreamingRequest 判断请求是否为流式/长连接端点（路径或 Accept 头）。
func isStreamingRequest(c *gin.Context) bool {
	path := c.Request.URL.Path
	for _, suffix := range globalRateLimitExemptSuffixes {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}
	return strings.Contains(strings.ToLower(c.GetHeader("Accept")), "text/event-stream")
}

// GlobalRateLimitMiddleware 全局 API 限流中间件（区别于登录等专用限流）：
//   - 流式/长连接端点（/stream、/sse、/ws、/mcp、Accept: text/event-stream）豁免
//   - 超限时返回 429 并附中文重试提示
func GlobalRateLimitMiddleware(rl *RateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		if isStreamingRequest(c) {
			c.Next()
			return
		}
		ip := c.ClientIP()
		if !rl.allow(ip) {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "请求过于频繁，请稍后再试",
			})
			return
		}
		c.Next()
	}
}
