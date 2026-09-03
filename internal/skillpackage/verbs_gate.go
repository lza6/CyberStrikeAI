// Package skillpackage 提供工具引用漂移门：扫描 skills/*/SKILL.md 正文里引用的工具名，
// 与"真实工具全集"比对，报告引用了不存在的工具（幽灵工具）。设计移植自 caveman
// verbs-gate.mjs（从源派生真实 tool surface，扫描 skill 引用，fail-closed）。
//
// 启发式提取规则：优先抓反引号代码片段（`tool_name`）与 SKILL.md frontmatter
// 的 tools 字段。宁可少报不要漏报——false positive 可由人工复核排除。
//
// K0a：扫描改为 filepath.WalkDir 递归子目录（支持 skills/security/ skills/office/
// 子目录分层）。realTools 基线保持全部（vertical 过滤留后续批次，本期只奠基+递归）。
package skillpackage

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// VerbsGateViolation 单条违规：skill 引用了不存在的工具。
type VerbsGateViolation struct {
	Skill      string // skill 包名
	File       string // SKILL.md 相对路径
	Line       int    // 行号（1-based）
	Referenced string // 被引用的幽灵工具名
	Hint       string // 提示
}

// ScanToolReferences 扫描 skillsDir 下每个 SKILL.md，提取引用的工具名，
// 与 realTools 比对，返回引用了不存在工具的违规清单。
//
// 提取规则（保守，覆盖主要写法）：
//  1. frontmatter 的 tools: [a, b, c] 或 tools:\n - a\n - b
//  2. 正文中反引号包裹的工具名 `tool_name`（snake_case / kebab-case / 含连字符）
//  3. "使用 exec 工具" / "调用 nmap 工具" 这类中文行内引用只作为补充启发，不强制
//
// realTools 为空时直接返回空（无基线无法判定）。
func ScanToolReferences(skillsDir string, realTools map[string]bool) ([]VerbsGateViolation, error) {
	if len(realTools) == 0 {
		return nil, nil
	}
	if skillsDir == "" {
		return nil, nil
	}
	info, err := os.Stat(skillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取 skills 目录失败: %w", err)
	}
	if !info.IsDir() {
		return nil, nil
	}
	// K0a：改用 filepath.WalkDir 递归扫描子目录，支持 skills/security/、skills/office/
	// 等子目录分层（vertical 域目录）。WalkDir 比 Walk 更高效（不调用 Lstat）。
	// 同一 SKILL.md 只扫一次（WalkDir 天然去重路径）。
	// 工具名候选正则：反引号里的 snake_case/kebab-case/含连字符的标识符（3+ 字符）
	backtickRe := regexp.MustCompile("`([a-z][a-z0-9_-]{2,})`")
	// frontmatter tools: 行两种形态
	toolsInlineRe := regexp.MustCompile(`(?m)^\s*tools:\s*\[([^\]]*)\]`)
	toolsListRe := regexp.MustCompile(`(?m)^\s*-\s+([a-z][a-z0-9_-]{2,})`)

	// P1-6：真实工具前缀/子串匹配 hint（正文反引号里的缩写引用不算幽灵）。
	hint := newRealToolsHint(realTools)

	// P1-6：skill 目录名白名单。递归扫描时收集所有非隐藏目录名（含嵌套
	// vertical 子目录），正文反引号里的 skill 互引（`ctf-web` 指另一 skill 包）
	// 不算幽灵工具。仅作用于正文——frontmatter tools 字段是真契约仍严格校验。
	// （cmd/verbs-gate 只把顶层目录名加进 realTools，嵌套层在这里补齐。）
	skillDirNames := make(map[string]bool)
	_ = filepath.WalkDir(skillsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") && path != skillsDir {
			return filepath.SkipDir
		}
		if path != skillsDir {
			skillDirNames[d.Name()] = true
		}
		return nil
	})

	// P1-6：Markdown 子代理名白名单（skillsDir 的兄弟目录 agents/*.md 的
	// basename，与 internal/agents.collectMarkdownBasenames 的加载规则一致：
	// 顶层非隐藏 .md、跳过 README 与 `_` 前缀共享片段）。正文里「交给
	// vulnerability-triage 子代理」是对真实子代理的引用，不是幽灵工具。
	// frontmatter tools 字段不受此白名单影响。
	agentNames := markdownAgentNames(filepath.Join(filepath.Dir(skillsDir), "agents"))

	var violations []VerbsGateViolation
	walkErr := filepath.WalkDir(skillsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// 读取子目录失败只跳过该子树，不中断整体扫描（fail-soft for listing）
			return nil
		}
		if d.IsDir() {
			// 跳过隐藏目录（.eino/plantask 等运行时产物）
			if strings.HasPrefix(d.Name(), ".") && path != skillsDir {
				return filepath.SkipDir
			}
			return nil
		}
		// 只处理 SKILL.md 文件
		if d.Name() != "SKILL.md" {
			return nil
		}
		// 计算相对路径用于违规报告（保留 skills/<pkg>/SKILL.md 形态）
		rel, relErr := filepath.Rel(skillsDir, path)
		if relErr != nil {
			rel = filepath.Base(path)
		}
		skillPkgName := skillPkgNameFromPath(skillsDir, path)

		data, rerr := os.ReadFile(path)
		if rerr != nil {
			// 读不了单个 SKILL.md 跳过，不中断
			return nil
		}
		lines := strings.Split(string(data), "\n")

		// 分离 frontmatter 与正文
		fmEnd := -1
		if strings.HasPrefix(string(data), "---") {
			for i := 1; i < len(lines); i++ {
				if strings.TrimSpace(lines[i]) == "---" {
					fmEnd = i
					break
				}
			}
		}

		// 1. frontmatter tools 字段
		if fmEnd > 0 {
			inTools := false
			for i := 0; i < fmEnd; i++ {
				line := lines[i]
				if toolsInlineRe.MatchString(line) {
					// inline 形态 tools: [a, b, c]
					m := toolsInlineRe.FindStringSubmatch(line)
					if len(m) > 1 {
						for _, name := range strings.Split(m[1], ",") {
							name = strings.TrimSpace(strings.Trim(name, `"' `))
							if name != "" && isLikelyToolName(name) && !realTools[name] {
								violations = append(violations, VerbsGateViolation{
									Skill: skillPkgName, File: filepath.ToSlash(rel),
									Line: i + 1, Referenced: name, Hint: "frontmatter tools 字段",
								})
							}
						}
					}
					inTools = false
					continue
				}
				if strings.HasPrefix(strings.TrimSpace(line), "tools:") {
					inTools = true
					continue
				}
				if inTools {
					m := toolsListRe.FindStringSubmatch(line)
					if len(m) > 1 {
						name := m[1]
						if isLikelyToolName(name) && !realTools[name] {
							violations = append(violations, VerbsGateViolation{
								Skill: skillPkgName, File: filepath.ToSlash(rel),
								Line: i + 1, Referenced: name, Hint: "frontmatter tools 列表项",
							})
						}
						continue
					}
					// 列表项结束（非 - 开头且非空）
					if strings.TrimSpace(line) != "" && !strings.HasPrefix(strings.TrimSpace(line), "-") {
						inTools = false
					}
				}
			}
		}

		// 2. 正文中反引号工具名（P1-6：真实工具前缀/子串引用降级不报，减少假阳性；
		// frontmatter 是真契约仍严格校验，不受 hint 影响）
		for i := fmEnd + 1; i < len(lines); i++ {
			for _, m := range backtickRe.FindAllStringSubmatch(lines[i], -1) {
				name := m[1]
				if isLikelyToolName(name) && !realTools[name] && !hint.isRealToolRef(name) &&
					!skillDirNames[name] && !agentNames[name] {
					violations = append(violations, VerbsGateViolation{
						Skill: skillPkgName, File: filepath.ToSlash(rel),
						Line: i + 1, Referenced: name, Hint: "正文反引号引用",
					})
				}
			}
		}
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("遍历 skills 目录失败: %w", walkErr)
	}

	// 去重（同一 skill 同一工具多次出现只报一次）
	seen := map[string]bool{}
	var dedup []VerbsGateViolation
	for _, v := range violations {
		key := v.Skill + "|" + v.Referenced
		if !seen[key] {
			seen[key] = true
			dedup = append(dedup, v)
		}
	}
	sort.Slice(dedup, func(i, j int) bool {
		if dedup[i].Skill != dedup[j].Skill {
			return dedup[i].Skill < dedup[j].Skill
		}
		return dedup[i].Referenced < dedup[j].Referenced
	})
	return dedup, nil
}

// markdownAgentNames 收集 agents 目录下的 Markdown 子代理名（basename 去扩展名）。
// 加载规则与 internal/agents.collectMarkdownBasenames 对齐：顶层非隐藏 .md、
// 跳过 README.md 与 `_` 前缀共享片段。目录不存在（测试环境/独立部署）返回空。
func markdownAgentNames(agentsDir string) map[string]bool {
	out := map[string]bool{}
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasPrefix(n, ".") || strings.HasPrefix(n, "_") {
			continue
		}
		if !strings.EqualFold(filepath.Ext(n), ".md") || strings.EqualFold(n, "README.md") {
			continue
		}
		out[strings.TrimSuffix(n, filepath.Ext(n))] = true
	}
	return out
}

// skillPkgNameFromPath 从 SKILL.md 绝对路径提取 skill 包名。
// 包名 = SKILL.md 所在目录相对 skillsRoot 的路径（ToSlash），与 lock.go 对齐：
//   - 顶层 skills/alpha/SKILL.md → "alpha"（向后兼容旧 e.Name()）
//   - 嵌套 skills/security/alpha/SKILL.md → "security/alpha"（避免不同 vertical 下同名冲突）
//
// K0a：递归扫描后路径深度不一，统一用相对目录路径作包名。
func skillPkgNameFromPath(skillsRoot, skillMdPath string) string {
	rel, err := filepath.Rel(skillsRoot, skillMdPath)
	if err != nil {
		return filepath.Base(filepath.Dir(skillMdPath))
	}
	// rel 形如 "alpha/SKILL.md" 或 "security/alpha/SKILL.md"；取目录部分
	return filepath.ToSlash(filepath.Dir(rel))
}

// isLikelyToolName 过滤明显非工具名的反引号内容（句子/代码块/路径）。
// 工具名约定：snake_case 或 kebab-case，不含空格/点/斜杠，长度 3-40。
func isLikelyToolName(s string) bool {
	if len(s) < 3 || len(s) > 40 {
		return false
	}
	if strings.ContainsAny(s, " ./\\='\":") {
		return false
	}
	if isCommonNonToolWord(strings.ToLower(s)) {
		return false
	}
	return true
}

// commonNonToolWords P1-6：高频假阳性停词表。正文反引号里大量出现的是字段名/
// 通用技术词/协议术语（name、status、body、jwt header claims、HTTP 方法、
// 编解码名、SQLite/JSON 字段等），不是工具引用。扩充自 verbs-gate 实跑
// 233 条假阳性的词频统计（前 150 词覆盖绝大多数重复项）。
var commonNonToolWords = map[string]bool{
	// 原有停词
	"the": true, "and": true, "for": true, "with": true, "this": true, "that": true,
	"use": true, "tool": true, "tools": true, "skill": true, "skills": true,
	// 通用字段/属性名（snake_case 正文高频）
	"name": true, "type": true, "from": true, "body": true, "data": true,
	"status": true, "count": true, "file": true, "chain": true, "token": true,
	"id": true, "args": true, "result": true, "error": true, "time": true,
	"path": true, "paths": true, "key": true, "value": true, "list": true,
	"map": true, "get": true, "set": true, "true": true, "false": true,
	"none": true, "null": true, "description": true, "version": true,
	"target": true, "level": true, "page": true, "num": true, "notes": true,
	"note": true, "reproduction": true, "poc": true, "finding": true,
	"exp": true, "links": true, "routing": true, "ref": true, "role": true,
	"roles": true, "sub": true, "service": true, "secret": true, "system": true,
	"template": true, "shell": true, "requires": true, "require": true,
	"supports": true, "supported": true, "confirmed": true, "verified": true,
	"verifications": true, "tentative": true, "vulnerable": true,
	"warning": true, "business": true, "balance": true, "badges": true,
	"documents": true, "certificates": true, "licenses": true, "invites": true,
	"entitlements": true, "subscriptions": true, "exploits": true,
	"exploit": true, "penetration": true, "progressive": true,
	"authenticated": true, "deprecated": true, "contains": true,
	"confidence": true, "part_of": true, "leads_to": true, "depends_on": true,
	"discovered_on": true, "created_at": true, "related_vulnerability_id": true,
	"resource_path": true, "http_status": true, "name_value": true,
	"file_count": true, "script_count": true, "read_payloads": true,
	"redirect_uri": true, "fact_key": true, "project_facts": true,
	"project_fact_edges": true, "cve_candidate_list": true, "is_file": true,
	"is_admin": true, "include": true, "include_once": true, "eval": true,
	// HTTP / 网络方法名
	"http": true, "https": true, "fetch": true, "curl": true, "dig": true,
	"cat": true, "sed": true, "cmd": true, "aws": true, "infra": true,
	// DNS / 网络 / 系统字段名（DNS 记录类型、账号角色字段等正文高频词）
	"cname": true, "cpe": true, "uid": true, "auth": true, "admins": true,
	"service_role": true, "fingerprint": true, "flag": true, "hack": true,
	"web_fetch": true,
	// JWT / 协议 claim 名（jwt header/payload 字段高频出现在反引号里）
	"alg": true, "iss": true, "aud": true, "sub_jwk": true, "jwk": true,
	"jku": true, "kid": true, "nbf": true, "exp_claim": true, "cty": true,
	"x5c": true, "x5u": true, "kyc": true, "anon": true, "apikey": true,
	// 编解码/哈希算法名（对称词尾 _encode/_decode/_hash 大量误报的根因之一，
	// 逐个列入；工具名如 jwt_encode 若真实存在则走 realTools 精确匹配，不受影响）
	"base64_encode": true, "base64_decode": true, "base64url": true,
	"base32_encode": true, "base32_decode": true, "base58_encode": true,
	"base58_decode": true, "hex_encode": true, "hex_decode": true,
	"url_encode": true, "url_decode": true, "html_encode": true,
	"html_decode": true, "unicode_encode": true, "unicode_decode": true,
	"auto_decode": true, "rot13_encode": true, "rot13_decode": true,
	"morse_encode": true, "morse_decode": true, "caesar_encode": true,
	"caesar_decode": true, "jwt_encode": true, "jwt_decode": true,
	"crypto_decode": true, "sha1_hash": true, "sha256_hash": true,
	"sha512_hash": true, "md5_hash": true, "aes_encrypt": true,
	"aes_decrypt": true, "des_encrypt": true, "des_decrypt": true,
	"rsa_encrypt": true, "rsa_decrypt": true,
	// Python/JS 内置与常见代码标识符
	"str_replace": true, "python_execute": true, "preg_replace": true,
	"concat_ws": true, "onerror": true, "onload": true, "onfocus": true,
	"onmouseover": true, "onclick": true,
}

// isCommonNonToolWord 判断词是否命中停词表。
func isCommonNonToolWord(lower string) bool {
	return commonNonToolWords[lower]
}

// realToolsHint P1-6：正文反引号里的真实工具缩写/变体引用（如 "nmap" 前缀于
// 真实工具 "nmap-sub"、"curl" 是真实工具 "curl-wrap" 的前缀）。这类词不是幽灵，
// 是对真实工具的不完整引用。前缀或包含匹配命中即跳过报幽灵。
// frontmatter tools 字段不使用本 hint（真契约仍严格校验）。
type realToolsHint struct {
	prefixes map[string]bool // 真实工具名的所有前缀（长度 >= 3）
	whole    map[string]bool // 真实工具名全集
}

func newRealToolsHint(realTools map[string]bool) *realToolsHint {
	h := &realToolsHint{
		prefixes: make(map[string]bool, len(realTools)*8),
		whole:    make(map[string]bool, len(realTools)),
	}
	for name := range realTools {
		h.whole[name] = true
		// 收集所有长度 >= 3 的前缀。"nmap-sub" → nma/nmap/nmap-/nmap-s/...
		for i := 3; i <= len(name); i++ {
			h.prefixes[name[:i]] = true
		}
	}
	return h
}

// isRealToolRef 判断 name 是否为某真实工具的前缀或真实工具名的子串
//（真实工具的缩写/变体引用，不算幽灵）。
func (h *realToolsHint) isRealToolRef(name string) bool {
	if h == nil || name == "" {
		return false
	}
	if h.whole[name] {
		return true
	}
	if h.prefixes[name] {
		return true
	}
	// name 是某真实工具名的子串（如 "map" in "nmap-sub"）。O(N*len) 朴素匹配，
	// realTools 规模（百级）与调用频率可接受。
	for tool := range h.whole {
		if strings.Contains(tool, name) {
			return true
		}
	}
	return false
}
