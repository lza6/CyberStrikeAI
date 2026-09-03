package pluginslot

import "testing"

// TestQuoteAppleScriptEscapesBackslash Critic M3 复验：反斜杠先于双引号转义，
// `\"` 序列不得提前闭合 AppleScript 字符串。
func TestQuoteAppleScriptEscapesBackslash(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`plain`, `"plain"`},
		{`say "hi"`, `"say \"hi\""`},
		{`back\slash`, `"back\\slash"`},
		// 注入向量：`\"` 若只转义引号会产出 `\\"`（= 字面反斜杠 + 提前闭合）。
		// 正确输出 `\\\"` = 字面反斜杠 + 转义引号，字符串不闭合。
		{`x\" & (do shell script "id") & \"`, `"x\\\" & (do shell script \"id\") & \\\""`},
	}
	for _, c := range cases {
		if got := quoteAppleScript(c.in); got != c.want {
			t.Errorf("quoteAppleScript(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestWebhookSecretRefusesUnsignedSend Critic M4 复验：配置了 Secret 但 HMAC 未实现
// 时拒绝发送（显式失败优于假签名）。
func TestWebhookSecretRefusesUnsignedSend(t *testing.T) {
	w := &WebhookNotifier{URL: "http://127.0.0.1:1/hook", Secret: "topsecret"}
	err := w.Notify(NotifyEvent{Type: "test", Message: "m"})
	if err == nil {
		t.Fatal("Secret configured but HMAC unimplemented should refuse to send")
	}
	// 无 Secret 正常路径不拒绝（无 server 时返回网络错误，但不是"拒绝发送"错误）。
	w2 := &WebhookNotifier{URL: "http://127.0.0.1:1/hook"}
	if err := w2.Notify(NotifyEvent{Type: "test"}); err != nil {
		if we, ok := err.(*webhookError); ok && we.msg != "" {
			t.Fatalf("no-secret path should not be refused, got %v", err)
		}
		// 网络错误可接受（本地端口 1 不可达）。
	}
}
