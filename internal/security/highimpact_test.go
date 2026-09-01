package security

import "testing"

func TestIsHighImpactTool_Hit(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"exec lowercase", "exec"},
		{"exec uppercase", "EXEC"},
		{"exec mixed case with spaces", "  Exec  "},
		{"execute long form", "execute"},
		{"sqlmap", "sqlmap"},
		{"metasploit", "metasploit"},
		{"hydra", "hydra"},
		{"bettercap", "bettercap"},
		{"aireplay-ng", "aireplay-ng"},
		{"delete-file", "delete-file"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			risk, ok := IsHighImpactTool(c.input)
			if !ok {
				t.Fatalf("IsHighImpactTool(%q) = (_, false), want (_, true)", c.input)
			}
			if risk == "" {
				t.Fatalf("IsHighImpactTool(%q) returned empty risk description", c.input)
			}
		})
	}
}

func TestIsHighImpactTool_Miss(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"read_file", "read_file"},
		{"glob", "glob"},
		{"grep", "grep"},
		{"empty", ""},
		{"unknown", "some-benign-tool"},
		{"nmap", "nmap"},
		{"whitespace only", "   "},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			risk, ok := IsHighImpactTool(c.input)
			if ok {
				t.Fatalf("IsHighImpactTool(%q) = (%q, true), want (_, false)", c.input, risk)
			}
			if risk != "" {
				t.Fatalf("IsHighImpactTool(%q) returned non-empty risk %q on miss", c.input, risk)
			}
		})
	}
}

func TestHighImpactTools_AllEntriesHaveRisk(t *testing.T) {
	for name, risk := range HighImpactTools {
		if strings := risk; strings == "" {
			t.Fatalf("HighImpactTools entry %q has empty risk description", name)
		}
	}
}
