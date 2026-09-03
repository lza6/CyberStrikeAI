package security

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// shellBinaryName 返回本平台用于执行 shell 命令的二进制路径或名称。
//
// 背景：原实现硬编码 "/bin/sh"，在 Windows 上 exec.CommandContext 找不到
// /bin/sh（Windows 系统 PATH 无 sh），导致 Eino streaming shell 全部失败。
// 本函数提供跨平台回退链，避免把 Unix 路径硬编码进跨平台代码。
//
// 选择顺序（按可用性优先级）：
//  1. 显式覆盖：环境变量 CYBERSTRIKE_SHELL（供部署/测试固定 shell 路径）
//  2. 平台原生：unix 用 /bin/sh；windows 用 Git for Windows 的 sh.exe（PATH 探测 + 常见安装路径）
//  3. exec.LookPath("sh") 兜底（PATH 中任意 sh）
//
// 命中后做 exec.LookPath 验证可执行；都失败时返回 "sh" 让 exec 报错（行为对齐原实现）。
func shellBinaryName() string {
	// 1. 环境变量显式覆盖
	if v := strings.TrimSpace(os.Getenv("CYBERSTRIKE_SHELL")); v != "" {
		if p, err := exec.LookPath(v); err == nil {
			return p
		}
		// 非绝对路径也尝试直接用（exec 会再 LookPath）
		return v
	}

	if runtime.GOOS != "windows" {
		// Unix：/bin/sh 几乎总是存在
		if p, err := exec.LookPath("/bin/sh"); err == nil {
			return p
		}
		return "/bin/sh"
	}

	// Windows：优先 exec.LookPath("sh")，回退 Git for Windows 常见安装路径
	if p, err := exec.LookPath("sh"); err == nil {
		return p
	}
	// Git for Windows 常见安装路径（Program Files / 32位 / scoop）
	candidates := []string{
		`C:\Program Files\Git\usr\bin\sh.exe`,
		`C:\Program Files (x86)\Git\usr\bin\sh.exe`,
		`C:\Program Files\Git\bin\sh.exe`,
		`C:\Program Files (x86)\Git\bin\sh.exe`,
	}
	for _, c := range candidates {
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	return "sh"
}
