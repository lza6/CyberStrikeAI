package termout

import (
	"fmt"
	"os/exec"
	"runtime"
)

// OpenBrowser 在默认浏览器中打开指定 URL。
// Windows: 用 cmd /c start（最稳，不依赖具体浏览器路径）
// darwin:  open
// Linux:  xdg-open
func OpenBrowser(url string) error {
	if url == "" {
		return fmt.Errorf("url 为空")
	}
	switch runtime.GOOS {
	case "windows":
		// start "" 让标题为空，避免把 URL 当成 start 的窗口标题解析。
		return exec.Command("cmd", "/c", "start", "", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		// Linux/FreeBSD 等
		return exec.Command("xdg-open", url).Start()
	}
}
