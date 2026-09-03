package security

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// staticCacheMaxAge 是静态资源（/static/）的浏览器强缓存时长：1 年。
// 配合 index.html 里 ?v=YYYYMMDD-N 查询串做版本号：内容变 → 版本号变 → 浏览器视为新 URL 重拉。
const staticCacheMaxAge = 31536000 // 365 * 24 * 3600

// StaticCacheHeaders 为 /static/ 前缀的资源设置长缓存（public, max-age=1y, immutable），
// 非静态资源（HTML、/api/*）不设置，避免前端改版/配置变更被中间代理缓存。
//
// 设计：
//   - /static/js、/static/css、/static/vendor 等资源文件名/查询串已带版本号（?v=...），
//     内容变即版本号变，故可安全长缓存。
//   - immutable 让浏览器在版本号未变时连 304 校验请求都不发，显著减少首屏后回访的请求数。
//   - 根路径 index.html 与所有 /api 不受影响（本中间件不改它们的头）。
//
// 零额外依赖：不引 gziphandler，gzip 已由 gin/中间件链或反代处理；此处只管缓存语义。
func StaticCacheHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		if strings.HasPrefix(path, "/static/") {
			// 静态资源：长缓存 + immutable。?v= 变更即新 URL。
			c.Header("Cache-Control", "public, max-age="+itoa(staticCacheMaxAge)+", immutable")
		}
		// 非静态资源：不设 Cache-Control，让下游 SecureHeaders/各 handler 自行决定。
		c.Next()
	}
}

// itoa 避免 strconv 依赖（保持本文件零 import）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
