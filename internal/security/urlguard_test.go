package security

import "testing"

// TestIsPrivateOrReservedURL 覆盖私有/保留网段与公网地址判定。
// 所有 case 均不依赖外部 DNS：IP 字面量走本地段判断，域名只使用 localhost 特判。
func TestIsPrivateOrReservedURL(t *testing.T) {
	cases := []struct {
		name    string
		rawURL  string
		private bool
	}{
		{"IPv4 loopback", "http://127.0.0.1/shell.php", true},
		{"IPv4 RFC1918 10/8", "http://10.0.0.1/shell.php", true},
		{"IPv4 RFC1918 172.16/12", "http://172.16.1.1/shell.php", true},
		{"IPv4 RFC1918 192.168/16", "http://192.168.1.1/shell.php", true},
		{"IPv4 link-local", "http://169.254.1.1/shell.php", true},
		{"IPv4 unspecified", "http://0.0.0.0/shell.php", true},
		{"IPv4 multicast", "http://224.0.0.1/shell.php", true},
		{"IPv4 reserved broadcast", "http://255.255.255.255/shell.php", true},
		{"IPv4-mapped IPv6 RFC1918", "http://[::ffff:10.0.0.1]/shell.php", true},
		{"localhost domain", "http://localhost:8080/shell.php", true},
		{"IPv6 loopback", "http://[::1]/shell.php", true},
		{"IPv6 ULA", "http://[fc00::1]/shell.php", true},
		{"IPv6 link-local", "http://[fe80::1]/shell.php", true},
		{"public IPv4", "http://93.184.216.34/shell.php", false},
		{"public IPv6", "http://[2606:2800:220:1:248:1893:25c8:1946]/shell.php", false},
		{"malformed url", "http://[::1/shell.php", true},
		{"unsupported scheme", "ftp://93.184.216.34/shell.php", true},
		{"file scheme", "file:///etc/passwd", true},
		{"empty url", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, why := IsPrivateOrReservedURL(c.rawURL)
			if got != c.private {
				t.Errorf("IsPrivateOrReservedURL(%q) = %v (%s), want %v", c.rawURL, got, why, c.private)
			}
		})
	}
}
