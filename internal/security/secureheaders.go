package security

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// SecureHeaders 返回安全响应头中间件。
//
// CSP 说明：当前前端存在大量内联 script/onclick 与 SSE fetch，'unsafe-inline' 是
// 有意的保守妥协——先落地 CSP 骨架防外部脚本注入与点击劫持，避免打断现有 UI；
// 后续前端改造（nonce 化）后应收紧为 'nonce-xxx'。
func SecureHeaders(isTLS bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; "+
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
