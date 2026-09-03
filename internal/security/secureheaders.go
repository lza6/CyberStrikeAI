package security

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

// cspNonceKey 是存入 gin.Context 的 nonce 键。
// 每个请求一个独立 nonce，注入到 CSP 头与 index.html 模板，供 inline script/onclick 收紧使用。
const cspNonceKey = "cyberstrike_csp_nonce"

// SecureHeaders 返回安全响应头中间件。
//
// CSP 策略演进：
//   - v1.7：script-src 'self' 'unsafe-inline'（保守妥协，前端大量 inline onclick 不打断）
//   - v1.8：script-src 'self' 'nonce-<per-request>'（F4 nonce 化收紧）
//
// nonce 化前提：index.html 的 2 处 inline <script>（主题初始化 + 路由 pending）已迁为
// nonce 注入；490 处 inline onclick 已全部迁为 data-action 事件委托（nav-delegate.js
// 统一分发）。'unsafe-inline' 移除后，未带 nonce 的 inline 脚本/onclick 被浏览器拒绝执行。
//
// 风险控制：nonce 每请求随机（16 字节），不可预测；CSP 只放行 'self' + nonce，
// 外部脚本注入（XSS）即使写入 <script> 也因无 nonce 而失效。
func SecureHeaders(isTLS bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 每请求生成 CSP nonce（16 字节 hex = 32 字符）
		nonce := generateCSPNonce()
		c.Set(cspNonceKey, nonce)

		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		// CSP：script-src 移除 'unsafe-inline'，改为 nonce 收紧。
		// style-src 保留 'unsafe-inline'（inline style 广泛用于动态样式，迁移成本高且风险低）。
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self' 'nonce-"+nonce+"'; style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data: blob:; connect-src 'self'; font-src 'self' data:; "+
				"object-src 'none'; base-uri 'self'; frame-ancestors 'none'")
		if isTLS {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		// 静态资源（/static/）由 StaticCacheHeaders 中间件设长缓存；
		// 此处为根 HTML 与 API 设禁缓存，避免发布新版/配置变更被浏览器或中间代理缓存。
		if !strings.HasPrefix(c.Request.URL.Path, "/static/") {
			h.Set("Cache-Control", "no-store, no-cache, must-revalidate")
		}
		c.Next()
	}
}

// CSPNonceFromContext 取当前请求的 CSP nonce（供模板渲染注入 inline script）。
// 若中间件未注入（测试/非 HTTP 上下文），返回空串。
func CSPNonceFromContext(c *gin.Context) string {
	if v, ok := c.Get(cspNonceKey); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// nonceBuf pool 避免每请求分配 16 字节切片（高频路径）。
var nonceBufPool = sync.Pool{
	New: func() interface{} {
		b := make([]byte, 16)
		return &b
	},
}

// generateCSPNonce 生成 16 字节随机 nonce，hex 编码为 32 字符。
// crypto/rand 保证不可预测；失败时回退到时间相关值（不应在正常环境发生）。
func generateCSPNonce() string {
	bp := nonceBufPool.Get().(*[]byte)
	defer nonceBufPool.Put(bp)
	b := *bp
	if _, err := rand.Read(b); err != nil {
		// 回退：不应发生，但保证不返回空 nonce 让 CSP 失效
		for i := range b {
			b[i] = byte(i * 7)
		}
	}
	return hex.EncodeToString(b)
}
