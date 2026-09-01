// Package shellsafe 提供 quote-aware 命令解析器，拒绝引号外 shell 元字符。
// 所有由 LLM 生成命令的执行出口都过这层，使同一安全策略处处生效——
// exploit executor、confirmation agent 的重放、cleanup registry 的默认 exec。
//
// 确实需要 shell 的调用方必须用 `sh -c "..."` 作为单个引号参数包裹——
// 这让意图在调用点显式表达，而非隐式藏在解析器里。
//
// 设计移植自 Pentest-Swarm-AI internal/shellsafe（Go 同语言，纯函数，零依赖）。
package security

import (
	"fmt"
	"strings"
	"unicode"
)

// ShellSafeParse 把命令串切分为 argv，尊重单/双引号；拒绝任何引号外的
// shell 元字符（| > < & ; ` $( ) 换行）。这是纵深防御——scope 与
// allowlist 校验仍独立运行。
func ShellSafeParse(cmd string) ([]string, error) {
	if err := shellRejectUnsafe(cmd); err != nil {
		return nil, err
	}

	var (
		args []string
		cur  strings.Builder
		in   rune // 0, '\'', or '"'
		esc  bool // 前一字符是反斜杠（仅双引号内）
	)
	flush := func() {
		if cur.Len() > 0 {
			args = append(args, cur.String())
			cur.Reset()
		}
	}

	for _, r := range cmd {
		switch {
		case esc:
			cur.WriteRune(r)
			esc = false
		case in == '"' && r == '\\':
			esc = true
		case in == 0 && (r == '\'' || r == '"'):
			in = r
		case in != 0 && r == in:
			in = 0
		case in == 0 && unicode.IsSpace(r):
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	if in != 0 {
		return nil, fmt.Errorf("未闭合的 %c 引号", in)
	}
	flush()
	if len(args) == 0 {
		return nil, fmt.Errorf("空命令")
	}
	return args, nil
}

// shellRejectUnsafe 扫描引号外的 shell 元字符。刻意严格——
// 误报是 feature 不是 bug，任何需要这些字符的调用方都应用 sh -c 显式包裹。
func shellRejectUnsafe(cmd string) error {
	var (
		in  rune
		esc bool
	)
	for i, r := range cmd {
		switch {
		case esc:
			esc = false
			continue
		case in == '"' && r == '\\':
			esc = true
			continue
		case in == 0 && (r == '\'' || r == '"'):
			in = r
			continue
		case in != 0 && r == in:
			in = 0
			continue
		}
		if in != 0 {
			continue
		}
		switch r {
		case '|', '>', '<', '&', ';', '`':
			return fmt.Errorf("位置 %d 出现禁用的 shell 元字符 %q", i, r)
		case '\n', '\r':
			return fmt.Errorf("位置 %d 出现换行", i)
		case '$':
			// 拒绝 $(...) 命令替换。$ 后跟字母或 { 是允许的（env-var 展开在
			// exec.Cmd 内发生，不经 shell，本身无害）。
			if i+1 < len(cmd) && cmd[i+1] == '(' {
				return fmt.Errorf("位置 %d 出现命令替换 $(", i)
			}
		}
	}
	return nil
}
