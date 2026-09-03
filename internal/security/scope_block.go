// Package security 中 project 级授权范围（projects.scope_json）硬拦截核心。
//
// 与 internal/project/scope_block.go（拼给 LLM 的软提示）不同：本文件把
// project.scope_json 的 targets/exclude 解析为可强制校验的 Scope，供
// executor.ExecuteTool 在工具执行前做"会话级授权边界"硬拦截。
//
// 工具 yaml 的 scope（CIDRs/Domains/Ports/Excluded）是单工具范围裁剪；
// project scope_json 是会话级授权边界。二者在 executor 内叠加校验。
package security

import (
	"context"
	"encoding/json"
	"net"
	"strings"

	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/mcp"

	"go.uber.org/zap"
)

// projectScopePayload 与 internal/project/scope_block.go 约定字段一致，
// 独立定义避免 security→project 依赖。
type projectScopePayload struct {
	Targets []string `json:"targets"`
	Exclude []string `json:"exclude"`
	Notes   string   `json:"notes"`
}

// ScopeFromProjectJSONString 把 projects.scope_json 的 targets/exclude 转成可强制校验的 Scope。
// scope_json 为空/非法 JSON 时返回零值 Scope（不限制，向后兼容）。
// 每个条目归一化：含 "/" 视为 CIDR；host:port（port 为纯数字/范围）拆分为 host→Domains + port→Ports；
// 否则（域名/纯 IP）进 Domains。exclude 统一进 Excluded。
func ScopeFromProjectJSONString(raw string) Scope {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Scope{}
	}
	var p projectScopePayload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return Scope{}
	}
	s := Scope{}
	for _, t := range p.Targets {
		norm := normalizeScopeEntry(t)
		if norm == "" {
			continue
		}
		switch {
		case strings.Contains(norm, "/"):
			s.CIDRs = append(s.CIDRs, norm)
		case extractPortSpec(norm) != "":
			host, _, _ := splitHostPortLenient(norm)
			if host != "" {
				s.Domains = append(s.Domains, host)
			}
			s.Ports = append(s.Ports, extractPortSpec(norm))
		default:
			s.Domains = append(s.Domains, norm)
		}
	}
	for _, ex := range p.Exclude {
		norm := normalizeScopeEntry(ex)
		if norm != "" {
			s.Excluded = append(s.Excluded, norm)
		}
	}
	return s
}

// ScopeFromProject 按 projectID 从 db 读 scope_json 并解析为 Scope。
// db/projectID 为空或查询失败返回零值（不限制）。
func ScopeFromProject(db *database.DB, projectID string) Scope {
	if db == nil {
		return Scope{}
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return Scope{}
	}
	proj, err := db.GetProject(projectID)
	if err != nil || proj == nil {
		return Scope{}
	}
	return ScopeFromProjectJSONString(proj.ScopeJSON)
}

// normalizeScopeEntry 把 scope_json 目标条目归一化为 host（去 scheme/路径）。
// "http://10.0.0.5:80/admin" → "10.0.0.5:80"；"example.com" → "example.com"。
func normalizeScopeEntry(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if idx := strings.Index(raw, "://"); idx >= 0 {
		rest := raw[idx+3:]
		if p := strings.IndexAny(rest, "/?#"); p >= 0 {
			rest = rest[:p]
		}
		return strings.TrimSpace(rest)
	}
	return raw
}

// extractPortSpec 若 host:port 中 port 是纯数字/范围/列表，返回端口规格，否则 ""。
func extractPortSpec(norm string) string {
	idx := strings.LastIndexByte(norm, ':')
	if idx < 0 {
		return ""
	}
	p := strings.TrimSpace(norm[idx+1:])
	if p == "" {
		return ""
	}
	for _, c := range p {
		if (c < '0' || c > '9') && c != '-' && c != ',' {
			return ""
		}
	}
	return p
}

// splitHostPortLenient host:port 拆分（port 非空）。IPv6 多冒号视为无端口。
func splitHostPortLenient(raw string) (string, string, bool) {
	if strings.Count(raw, ":") != 1 {
		return "", "", false
	}
	parts := strings.SplitN(raw, ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	h := strings.TrimSpace(parts[0])
	p := strings.TrimSpace(parts[1])
	if h == "" || p == "" {
		return "", "", false
	}
	return h, p, true
}

// executeTargetCandidates 从 shell 命令中提取候选目标（url/主机名/IP），
// 用于 execute 类工具的 project scope 校验。命中任一越界即拦截。
func executeTargetCandidates(command string) []string {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil
	}
	var out []string
	fields := strings.Fields(command)
	for i, f := range fields {
		f = strings.Trim(f, `"'`)
		if f == "" {
			continue
		}
		// URL 形态（http/https）直接作为目标候选。
		if strings.Contains(f, "://") {
			out = append(out, f)
			continue
		}
		// 上一个是已知网络参数（-u/-url/-host/-t 等），则后续值作为目标候选。
		if i > 0 {
			prev := strings.Trim(fields[i-1], `"'`)
			switch strings.ToLower(prev) {
			case "-u", "--url", "-url", "--target", "-t", "-target", "--host", "-host", "-i", "--ip":
				out = append(out, f)
				continue
			}
		}
		// 独立 IP/域名：跳过纯文件路径。
		if isPlainTargetToken(f) {
			out = append(out, f)
		}
	}
	return out
}

// isPlainTargetToken 判断 token 是否像独立 IP/域名（不含文件/目录分隔符与危险字符）。
func isPlainTargetToken(s string) bool {
	if s == "" || strings.ContainsAny(s, `/\`) {
		return false
	}
	if net.ParseIP(s) != nil {
		return true
	}
	// 至少含一个点（域名），避免把普通单词当目标。
	return looksLikeHostname(s) && strings.Contains(s, ".")
}

// looksLikeHostname 粗判是否为域名（含点或只含 host 字符）。
func looksLikeHostname(s string) bool {
	for _, r := range s {
		if !(r == '.' || r == '-' || r == '_' ||
			(r >= '0' && r <= '9') ||
			(r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z')) {
			return false
		}
	}
	return true
}

// ParseTarget 解析 host:port / URL 形态的目标（与 scope.go ExtractTarget 同一解析器）。
func ParseTarget(raw string) (string, int) {
	host, port := parseHostPort(raw)
	return host, port
}

// ExecuteScopeGuard J4：execute 类工具执行前的授权范围闸。
// 由 app.go 注入 executor 时同步注入（供 Eino 内置 execute 走同一 project scope 语义）。
type ExecuteScopeGuard struct {
	Resolve func(projectID string) Scope
	Logger  *zap.Logger
	// OnViolation 拦截回调（H1）：越界被拦时以 (projectID, toolName, reason) 调用，
	// 供 app.go 把 scope-violation 事件广播到 blackboard（reactions 引擎）。
	// nil=不回调（向后兼容，纯日志拦截）。
	OnViolation func(projectID, toolName, reason string)
}

// CheckExecute 校验 shell 命令目标是否落在 project 授权范围。
// 返回 (拦截提示, blocked)。未绑定 project / 无 scope / 无目标 / 命中授权 → 不拦。
func (g *ExecuteScopeGuard) CheckExecute(ctx context.Context, toolName string, args map[string]interface{}, _ bool) (string, bool) {
	if g == nil || g.Resolve == nil {
		return "", false
	}
	projectID := strings.TrimSpace(mcp.MCPProjectIDFromContext(ctx))
	if projectID == "" {
		return "", false
	}
	command, _ := args["command"].(string)
	targets := executeTargetCandidates(command)
	if len(targets) == 0 {
		return "", false
	}
	ps := g.Resolve(projectID)
	if ps.Empty() {
		return "", false
	}
	for _, t := range targets {
		host, port := ParseTarget(t)
		if host == "" {
			continue
		}
		if allowed, reason := ps.Allows(host, port); !allowed {
			if g.Logger != nil {
				g.Logger.Warn("execute 命令目标越界被 project scope 拦截",
					zap.String("toolName", toolName),
					zap.String("host", host),
					zap.Int("port", port),
					zap.String("projectId", projectID),
					zap.String("reason", reason),
					zap.String("command", command),
				)
			}
			// H1：广播 scope-violation 事件（nil=未启用 reactions，跳过）。
			if g.OnViolation != nil {
				g.OnViolation(projectID, toolName, reason)
			}
			return "工具目标越界被项目授权范围拦截: " + reason, true
		}
	}
	return "", false
}

// Empty 判断 Scope 是否无任何限制。
func (s Scope) Empty() bool {
	return len(s.CIDRs) == 0 && len(s.Domains) == 0 && len(s.Ports) == 0 && len(s.Excluded) == 0
}
