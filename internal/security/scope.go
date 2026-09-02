// Package security 中的 scope validator：CIDR/Domain/Port/Excluded 四元。
// 网络工具调用前的统一闸门——工具 yaml 可声明 scope，越界目标被拦截。
// 设计移植自 Pentest-Swarm-AI internal/scope/validator.go（Go 同语言，纯函数）。
package security

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Scope 工具执行目标范围（yaml 字段 cidrs/domains/ports/excluded）。
// 全部为空 = 不限制（允许任意目标）；excluded 优先级最高。
type Scope struct {
	CIDRs    []string `yaml:"cidrs,omitempty" json:"cidrs,omitempty"`       // 允许的 IP 段（如 192.168.1.0/24）
	Domains  []string `yaml:"domains,omitempty" json:"domains,omitempty"`   // 允许的域名（支持 *.example.com 通配）
	Ports    []string `yaml:"ports,omitempty" json:"ports,omitempty"`       // 允许的端口（80,443,1000-2000）
	Excluded []string `yaml:"excluded,omitempty" json:"excluded,omitempty"` // 明确排除（CIDR 或域名）
}

// Allows 判断 host:port 是否在 scope 内。返回 (allowed, reason)。
// host 支持 IP 字面量（CIDR 匹配）或域名（后缀/通配匹配）。
// 空 scope 允许全部（向后兼容：未声明 scope 的工具不受限）。
func (s *Scope) Allows(host string, port int) (bool, string) {
	if s == nil {
		return true, ""
	}
	empty := len(s.CIDRs) == 0 && len(s.Domains) == 0 && len(s.Ports) == 0 && len(s.Excluded) == 0
	if empty {
		return true, ""
	}

	// excluded 优先：命中即拒绝
	for _, ex := range s.Excluded {
		if matchScopeEntry(ex, host, port, s) {
			return false, fmt.Sprintf("目标 %s:%d 命中排除项 %q", host, port, ex)
		}
	}

	// 端口校验（若声明了 ports）
	if len(s.Ports) > 0 && !portAllowed(s.Ports, port) {
		return false, fmt.Sprintf("端口 %d 不在允许范围 %v", port, s.Ports)
	}

	// 目标校验（若声明了 cidrs/domains）
	hasTargetRules := len(s.CIDRs) > 0 || len(s.Domains) > 0
	if hasTargetRules {
		ip := net.ParseIP(host)
		if ip != nil {
			// IP 字面量：CIDR 匹配
			for _, cidr := range s.CIDRs {
				_, network, err := net.ParseCIDR(cidr)
				if err != nil {
					continue
				}
				if network.Contains(ip) {
					return true, ""
				}
			}
			return false, fmt.Sprintf("IP %s 不在允许 CIDR %v", host, s.CIDRs)
		}
		// 域名：精确/通配后缀匹配
		hostLower := strings.ToLower(strings.TrimSuffix(host, "."))
		for _, d := range s.Domains {
			if domainMatches(d, hostLower) {
				return true, ""
			}
		}
		return false, fmt.Sprintf("域名 %s 不在允许列表 %v", host, s.Domains)
	}

	// 只有端口规则且端口已通过
	return true, ""
}

// matchScopeEntry 判断 excluded 条目是否命中 host:port。
func matchScopeEntry(entry, host string, port int, s *Scope) bool {
	// CIDR 排除
	if _, network, err := net.ParseCIDR(entry); err == nil {
		if ip := net.ParseIP(host); ip != nil {
			return network.Contains(ip)
		}
		return false
	}
	// 域名排除
	hostLower := strings.ToLower(strings.TrimSuffix(host, "."))
	return domainMatches(entry, hostLower)
}

// portAllowed 检查端口是否在允许列表（支持 "80", "1000-2000"）。
func portAllowed(ports []string, port int) bool {
	for _, p := range ports {
		p = strings.TrimSpace(p)
		if strings.Contains(p, "-") {
			parts := strings.SplitN(p, "-", 2)
			lo, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
			hi, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
			if err1 == nil && err2 == nil && port >= lo && port <= hi {
				return true
			}
			continue
		}
		if v, err := strconv.Atoi(p); err == nil && v == port {
			return true
		}
	}
	return false
}

// domainMatches 域名匹配：精确相等或 *.example.com 通配（覆盖任意层级子域）。
func domainMatches(pattern, host string) bool {
	pattern = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(pattern), "."))
	if pattern == host {
		return true
	}
	if strings.HasPrefix(pattern, "*.") {
		suffix := pattern[1:] // ".example.com"
		return strings.HasSuffix(host, suffix)
	}
	return false
}

// TargetScope 目标范围校验的通用接口结构（executor 侧把 config.ToolScope 的字段
// 拷贝进此结构，避免 security→config 循环依赖——config.ToolScope 与本结构同构）。
type TargetScope struct {
	CIDRs    []string
	Domains  []string
	Ports    []string
	Excluded []string
}

// Allows 与 Scope.Allows 同逻辑。
func (t *TargetScope) Allows(host string, port int) (bool, string) {
	s := Scope{CIDRs: t.CIDRs, Domains: t.Domains, Ports: t.Ports, Excluded: t.Excluded}
	return s.Allows(host, port)
}

// ExtractTarget 从工具参数中提取目标 host 和端口（常见参数名兼容）。
// 返回 host, port, found。未找到时 found=false。
func ExtractTarget(args map[string]interface{}) (string, int, bool) {
	hostKeys := []string{"target", "host", "ip", "domain", "url", "hostname"}
	for _, k := range hostKeys {
		if v, ok := args[k]; ok {
			if val, isStr := v.(string); isStr {
				host, port := parseHostPort(val)
				if host != "" {
					return host, port, true
				}
			}
		}
	}
	return "", 0, false
}

// parseHostPort 解析 "1.2.3.4:80" / "http://host:port/path" / "example.com" 形态。
func parseHostPort(raw string) (string, int) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", 0
	}
	// URL 形态：去掉 scheme 与 path
	if idx := strings.Index(raw, "://"); idx >= 0 {
		rest := raw[idx+3:]
		if p := strings.IndexAny(rest, "/?#"); p >= 0 {
			rest = rest[:p]
		}
		raw = rest
	}
	// host:port（IPv6 多冒号视为无端口）
	if strings.Count(raw, ":") == 1 {
		parts := strings.SplitN(raw, ":", 2)
		if port, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil {
			return parts[0], port
		}
	}
	return raw, 0
}
