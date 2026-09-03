package pluginslot

import (
	"os/exec"
	"runtime"
)

func init() {
	// H2 修复：desktop notifier 注册到 Registry（DetectAvailable 按平台探测命令）。
	RegisterWithManifest(Manifest{
		Name:        "desktop",
		Slot:        SlotNotifier,
		Description: "系统桌面通知（macOS osascript / Linux notify-send / Windows msg）",
		Version:     "1.0.0",
		DisplayName: "Desktop Notification",
	}, func(cfg map[string]interface{}) interface{} {
		return NewDesktopNotifier(cfg)
	}, desktopNotifyAvailable)
}

// desktopNotifyAvailable 探测当前平台是否有可用的系统通知命令。
func desktopNotifyAvailable() bool {
	switch runtime.GOOS {
	case "darwin":
		_, err := exec.LookPath("osascript")
		return err == nil
	case "linux":
		_, err := exec.LookPath("notify-send")
		return err == nil
	default:
		// Windows：msg 不一定存在，但通知失败会静默（Notify 返回 nil）。
		// 注册恒可用，让 app.go 能拿到实例；Notify 内部再降级。
		return true
	}
}

// DesktopNotifier 桌面通知 Notifier 实现（SlotNotifier: "desktop"）。
// 跨平台：macOS 用 osascript，Linux 用 notify-send，Windows 用 msg/powershell。
// 失败静默（返回 error 供上层记日志，不阻断 reactions）。
type DesktopNotifier struct {
	// cfg 配置（当前无配置项，预留扩展）。
	cfg map[string]interface{}
}

// NewDesktopNotifier 构造。注册时用工厂函数，此处也暴露直接构造供测试。
func NewDesktopNotifier(cfg map[string]interface{}) *DesktopNotifier {
	return &DesktopNotifier{cfg: cfg}
}

// Notify 实现 Notifier。跨平台调用系统通知命令。
func (d *DesktopNotifier) Notify(event NotifyEvent) error {
	if d == nil {
		return nil
	}
	title := "CyberStrikeAI"
	if event.Priority == "urgent" {
		title = "⚠️ " + title
	}
	body := event.Message
	if body == "" {
		body = event.Type
	}
	switch runtime.GOOS {
	case "darwin":
		// osascript -e 'display notification "body" with title "title"'
		return exec.Command("osascript", "-e",
			`display notification `+quoteAppleScript(body)+` with title `+quoteAppleScript(title),
		).Run()
	case "linux":
		// notify-send --urgency=normal "title" "body"
		urgency := "normal"
		if event.Priority == "urgent" {
			urgency = "critical"
		}
		return exec.Command("notify-send", "--urgency="+urgency, title, body).Run()
	default:
		// windows: powershell BurntToast 或 msg。用 msg（内置）兜底。
		// msg * /TIME:10 "body" —— 需要终端服务，单机可能不可用，失败静默。
		if _, err := exec.LookPath("msg"); err == nil {
			return exec.Command("msg", "*", "/TIME:10", body).Run()
		}
		return nil
	}
}

// quoteAppleScript 把字符串转义为 AppleScript 字面量。
// Critic M3 修复：反斜杠必须先转义（\\），否则 `\"` 序列会被 AppleScript
// 解析为"转义反斜杠+提前闭合字符串"，后续文本成为代码执行（osascript 注入）。
// 顺序：先转义反斜杠，再转义双引号。
func quoteAppleScript(s string) string {
	var b []byte
	b = append(b, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' || c == '"' {
			b = append(b, '\\')
		}
		b = append(b, c)
	}
	b = append(b, '"')
	return string(b)
}
