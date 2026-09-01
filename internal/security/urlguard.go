package security

import (
	"net"
	"net/url"
	"strings"
)

// IsPrivateOrReservedURL 判断目标 URL 是否指向私有/保留网段（WebShell SSRF 防护首查）。
//
// 判定规则（fail-closed）：
//   - 解析失败、scheme 非 http/https、host 为空 → 视为私有
//   - IP 字面量：直接按网段判定，不发起 DNS 查询
//   - 域名：net.LookupHost 解析所有地址，任一命中即私有（防 DNS rebinding 的首次解析绕过）
//   - DNS 解析失败 → 视为私有
//
// 返回 (true, reason) 表示应拦截，reason 说明命中类别，可直接用于错误提示。
func IsPrivateOrReservedURL(rawURL string) (bool, string) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return true, "url parse failed"
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return true, "scheme must be http/https"
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if host == "" {
		return true, "host is empty"
	}

	// localhost 及其子域直接判私有，避免依赖系统 DNS/hosts 配置
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true, "loopback host"
	}

	// 纯 IP 字面量（含 IPv6 与 IPv4-mapped）直接按段判定
	if ip := net.ParseIP(host); ip != nil {
		return isPrivateOrReservedIP(ip)
	}

	// 域名：解析全部地址，任一命中即拦截
	ips, err := net.LookupHost(host)
	if err != nil {
		return true, "dns lookup failed"
	}
	if len(ips) == 0 {
		return true, "dns resolved to no address"
	}
	for _, s := range ips {
		if ip := net.ParseIP(s); ip != nil {
			if private, why := isPrivateOrReservedIP(ip); private {
				return true, why
			}
		} else {
			return true, "dns returned non-ip address"
		}
	}
	return false, ""
}

// isPrivateOrReservedIP 按 RFC 保留网段判定单个 IP 是否私有/保留。
// IPv4（含 IPv4-mapped IPv6）与 IPv6 分开处理。
func isPrivateOrReservedIP(ip net.IP) (bool, string) {
	if ip4 := ip.To4(); ip4 != nil {
		b := ip4
		switch {
		case b[0] == 127:
			return true, "IPv4 loopback (127.0.0.0/8)"
		case b[0] == 0:
			return true, "IPv4 unspecified (0.0.0.0/8)"
		case b[0] == 10:
			return true, "IPv4 private (10.0.0.0/8)"
		case b[0] == 172 && b[1] >= 16 && b[1] <= 31:
			return true, "IPv4 private (172.16.0.0/12)"
		case b[0] == 192 && b[1] == 168:
			return true, "IPv4 private (192.168.0.0/16)"
		case b[0] == 169 && b[1] == 254:
			return true, "IPv4 link-local (169.254.0.0/16)"
		case b[0] == 100 && b[1] >= 64 && b[1] <= 127:
			return true, "IPv4 shared address (100.64.0.0/10)"
		case b[0]&0xF0 == 0xE0:
			return true, "IPv4 multicast (224.0.0.0/4)"
		case b[0]&0xF0 == 0xF0: // 240/4 reserved，含 255.255.255.255 broadcast
			return true, "IPv4 reserved (240.0.0.0/4)"
		}
		return false, ""
	}

	// IPv6
	b := ip.To16()
	if b == nil {
		return true, "invalid ip"
	}
	allZeroFirst15 := true
	for _, v := range b[:15] {
		if v != 0 {
			allZeroFirst15 = false
			break
		}
	}
	switch {
	case allZeroFirst15:
		if b[15] == 1 {
			return true, "IPv6 loopback (::1)"
		}
		return true, "IPv6 reserved (::/96)"
	case b[0]&0xFE == 0xFC:
		return true, "IPv6 unique-local (fc00::/7)"
	case b[0] == 0xFE && b[1]&0xC0 == 0x80:
		return true, "IPv6 link-local (fe80::/10)"
	case b[0] == 0xFF:
		return true, "IPv6 multicast (ff00::/8)"
	}
	return false, ""
}
