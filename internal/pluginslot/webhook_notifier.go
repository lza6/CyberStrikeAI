package pluginslot

import (
	"bytes"
	"encoding/json"
	"net/http"
	"time"
)

// WebhookNotifier Webhook 通知 Notifier 实现（SlotNotifier: "webhook"）。
// POST JSON body 到用户配置的 URL。非阻塞：带 10s 超时，失败返回 error 供上层记日志。
// 移植语义：参考项目 Notifier.notify → registry.get<Notifier>("notifier", name) → notify(event)。
type WebhookNotifier struct {
	// URL 目标 webhook 地址（cfg["url"]）。
	URL string
	// Secret 可选：用于签名校验（预留，当前不实现 HMAC）。
	Secret string
	// Client HTTP 客户端（测试可注入；nil 用默认 10s 超时 client）。
	Client *http.Client
}

// NewWebhookNotifier 工厂：从 cfg 读 url/secret。
func NewWebhookNotifier(cfg map[string]interface{}) *WebhookNotifier {
	w := &WebhookNotifier{}
	if cfg != nil {
		if u, ok := cfg["url"].(string); ok {
			w.URL = u
		}
		if s, ok := cfg["secret"].(string); ok {
			w.Secret = s
		}
	}
	return w
}

func init() {
	// H2 修复：webhook notifier 注册到 Registry。detect 恒 true（HTTP 客户端零外部依赖）；
	// URL 由 app.go 传入 cfg（reactions.notifiers.webhook.url 配置段）。
	RegisterWithManifest(Manifest{
		Name:        "webhook",
		Slot:        SlotNotifier,
		Description: "Webhook 通知（POST JSON，10s 超时）",
		Version:     "1.0.0",
		DisplayName: "Webhook Notification",
	}, func(cfg map[string]interface{}) interface{} {
		return NewWebhookNotifier(cfg)
	}, nil)
}

// Notify 实现 Notifier。POST JSON 到 URL。
func (w *WebhookNotifier) Notify(event NotifyEvent) error {
	if w == nil || w.URL == "" {
		return nil
	}
	client := w.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CyberStrikeAI-Event", event.Type)
	// Critic M4 修复：HMAC 签名未实现前不发签名头——空签名头会让接收端把
	// 未签名请求当"已签名"（假安全）。Secret 已配置但签名未实现 → 拒绝发送
	//（显式失败优于静默假签名）。
	if w.Secret != "" {
		return &webhookError{status: 0, event: event.Type, msg: "webhook secret 配置了但 HMAC 签名未实现，拒绝发送（防假签名）"}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 2xx 视为成功；非 2xx 返回 error 供记日志（不阻断其他 notifier）。
	if resp.StatusCode >= 300 {
		return &webhookError{status: resp.StatusCode, event: event.Type}
	}
	return nil
}

type webhookError struct {
	status int
	event  string
	msg    string // 非空时优先用（status=0 的显式拒绝场景）
}

func (e *webhookError) Error() string {
	if e.msg != "" {
		return e.msg
	}
	return "webhook " + e.event + " returned " + http.StatusText(e.status)
}
