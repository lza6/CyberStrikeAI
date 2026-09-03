package security

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestStaticCacheHeadersForStaticAssets 校验 /static 静态资源回长缓存 max-age + immutable，
// 而 index.html 与 /api/* 不缓存（避免前端改版/配置变更被浏览器或中间代理缓存）。
// 验收标准（P1）：首屏静态资源走强缓存，HTML 与 API 禁缓存。
func TestStaticCacheHeadersForStaticAssets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(StaticCacheHeaders())

	// /static/js/foo.js → 长缓存
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/static/js/foo.js", nil)
	r.ServeHTTP(w, req)
	cc := w.Header().Get("Cache-Control")
	if cc != "public, max-age=31536000, immutable" {
		t.Fatalf("/static/js 期望长缓存，实际 %q", cc)
	}
	// /static/css/main.css → 长缓存
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/static/css/main.css?v=1", nil))
	if w2.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("/static/css 期望长缓存，实际 %q", w2.Header().Get("Cache-Control"))
	}
}

// TestStaticCacheHeadersDoesNotAffectHTML HTML 根路径与 .html 不应被长缓存（便于发布新版）。
func TestStaticCacheHeadersDoesNotAffectHTML(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(StaticCacheHeaders())

	// 模拟 index.html：由 LoadHTMLGlob 渲染，路径为 "/" 或 "/index.html"
	// StaticCacheHeaders 仅对 /static/ 前缀生效，根路径不应设长缓存
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if cc := w.Header().Get("Cache-Control"); cc == "public, max-age=31536000, immutable" {
		t.Fatalf("根路径不应被长缓存，实际 %q", cc)
	}
}
