package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"cyberstrike-ai/internal/database"
	"go.uber.org/zap"
)

// TestWebshellSSRFRedirectBlocked SSRF 重定向防护回归：
// 初始 URL 是公网（过 guardWebshellTarget 初始校验），服务端 302 跳到回环/内网，
// 应被 http.Client.CheckRedirect 拦截，绝不能把内网响应回传给调用方。
func TestWebshellSSRFRedirectBlocked(t *testing.T) {
	handler := NewWebShellHandler(zap.NewNop(), nil, false)

	// 内网靶（重定向最终目标）
	internalTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("internal-ok"))
	}))
	defer internalTarget.Close()

	// "公网"入口：收到请求后 302 → internalTarget
	// 注：单测环境两个 server 都在 127.0.0.1；初始 URL 走 allowPrivateTarget=false 会被
	// guardWebshellTarget 直接拦（回环也是私有段）。为了只测 CheckRedirect 逻辑，
	// 这里用 allowPrivateTarget=false + 直接调 h.client（绕过 guard）验证重定向拦截本身。
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, internalTarget.URL, http.StatusFound)
	}))
	defer redirector.Close()

	// 直接用 handler 的 client（带 CheckRedirect）跟随重定向，期望被拦
	req, err := http.NewRequest(http.MethodGet, redirector.URL+"/probe", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := handler.client.Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatalf("期望重定向到私有地址被拦截，实际请求成功 status=%d", resp.StatusCode)
	}
	if resp != nil && resp.StatusCode == http.StatusOK {
		t.Fatalf("期望被拦，实际拿到内网响应 status=200")
	}
	_ = database.RBACScopeAll // 保持 import（与其他测试文件风格一致）
}
