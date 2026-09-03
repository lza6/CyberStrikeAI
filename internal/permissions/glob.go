// Package permissions — glob 匹配工具，跨平台兼容。
//
// OpenHarness 用 Python fnmatch.fnmatch（Unix shell glob：* ? [seq] [!seq]）。
// Go 标准库 path.Match 语义接近但不支持 [!seq]（用 [^seq]）。本文件提供
// 一个最小 fnmatch 等价实现，确保 deny 规则跨平台行为一致。
package permissions

import "strings"

// matchGlob 是 fnmatch.fnmatch 的 Go 等价物（跨平台）。
//
// 支持：*（任意序列含 /）、?（单字符）、[abc]（字符集）、[!abc]（否定集）、
// [a-z]（范围）。不支持 \ 转义（与 fnmatch 一致，除非转义字面量方括号）。
//
// 与 path.Match 的差异：path.Match 的 * 不匹配 /，fnmatch 的 * 匹配任意字符含 /。
// 这里按 fnmatch 语义：* 贪婪匹配任意字符（含路径分隔符），更贴合 deny 规则直觉。
func matchGlob(pattern, name string) bool {
	return globMatch(pattern, name)
}

// globMatch 递归实现 fnmatch 语义。
//
// 移植自 Python fnmatch.translate 的核心逻辑（非字节级直译，语义等价）。
func globMatch(pattern, name string) bool {
	// 简化：先用标准库 path.Match 试一次（覆盖简单模式），失败再用自定义递归。
	// 但 path.Match 的 * 不跨 /，会漏匹配多段路径。故直接走自定义实现。
	p, n := 0, 0
	starP, starN := -1, -1
	for n < len(name) {
		if p < len(pattern) {
			c := pattern[p]
			switch c {
			case '*':
				starP = p
				starN = n
				p++
				continue
			case '?':
				p++
				n++
				continue
			case '[':
				ok, advance, matched := matchCharClass(pattern[p:], name[n])
				if ok {
					p += advance
					if !matched {
						// 不匹配，回溯到上一个 *
						if starP >= 0 {
							p = starP + 1
							starN++
							n = starN
							continue
						}
						return false
					}
					n++
					continue
				}
			default:
				if c == name[n] {
					p++
					n++
					continue
				}
			}
		}
		// 回溯到上一个 *
		if starP >= 0 {
			p = starP + 1
			starN++
			n = starN
			continue
		}
		return false
	}
	// 消耗 pattern 尾部的 *
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

// matchCharClass 匹配 [abc]/[!abc]/[a-z]/[]abc] 字符类。
//
// 返回：(是否是合法字符类, pattern 消耗长度, 是否匹配当前字符)。
func matchCharClass(pat string, ch byte) (bool, int, bool) {
	if len(pat) < 3 || pat[0] != '[' {
		return false, 0, false
	}
	// L1 修复：] 在字符类首位（[]...]）时按 fnmatch 语义视为字面量，闭合 ] 在其后。
	// 否定前缀 !/^ 若在 ] 之前（如 [!]]），先剥离前缀再找闭合 ]。
	bodyStart := 1
	negPrefix := ""
	if len(pat) > 2 && (pat[1] == '!' || pat[1] == '^') && pat[2] == ']' && len(pat) > 3 {
		// [!]]...：否定 + 字面量 ]
		negPrefix = string(pat[1])
		bodyStart = 3
	} else if pat[1] == ']' && len(pat) > 2 {
		bodyStart = 2
	}
	if bodyStart > 1 || pat[1] == ']' {
		end := strings.IndexByte(pat[bodyStart:], ']')
		if end < 0 {
			return false, 0, false
		}
		body := negPrefix + "]"
		if end > 0 {
			body = negPrefix + "]" + pat[bodyStart:bodyStart+end]
		}
		matched := charClassContains(body, ch)
		return true, bodyStart + end + 1, matched
	}
	end := strings.IndexByte(pat[bodyStart:], ']')
	if end < 0 {
		return false, 0, false
	}
	body := pat[bodyStart : bodyStart+end]
	matched := charClassContains(body, ch)
	return true, end + 2, matched
}

// charClassContains 判断字符 ch 是否命中字符类 body（[...] 内部，可含 !/^ 否定前缀）。
func charClassContains(body string, ch byte) bool {
	negate := false
	if len(body) > 0 && (body[0] == '!' || body[0] == '^') {
		negate = true
		body = body[1:]
	}
	matched := false
	for i := 0; i < len(body); i++ {
		if i+2 < len(body) && body[i+1] == '-' {
			lo, hi := body[i], body[i+2]
			if ch >= lo && ch <= hi {
				matched = true
			}
			i += 2
		} else {
			if body[i] == ch {
				matched = true
			}
		}
	}
	if negate {
		matched = !matched
	}
	return matched
}
