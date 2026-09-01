package playbooks

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestLoadPlaybooksFromDir 表驱动测试：覆盖空目录、目录不存在、正常加载（2 个 yaml + 1 个 README.md）、
// yaml 顶层 name 字段决定 DisplayName、tools 节点支持字符串数组与对象数组两种写法。
func TestLoadPlaybooksFromDir(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(t *testing.T, dir string) // 在 dir 内创建文件
		dir       string                            // 传入 LoadPlaybooksFromDir 的 dir 参数（空 = 用 t.TempDir）
		wantErr   bool
		wantNames []string // 期望加载出的 Name 列表（已排序）
		check     func(t *testing.T, got []Playbook)
	}{
		{
			name:      "empty_dir_returns_empty_no_error",
			setup:     func(t *testing.T, dir string) {},
			wantNames: []string{},
		},
		{
			name:    "nonexistent_dir_returns_empty_no_error",
			dir:      filepath.Join(os.TempDir(), "cyberstrike-playbooks-does-not-exist-xyz"),
			wantNames: []string{},
		},
		{
			name: "empty_string_dir_returns_empty_no_error",
			dir:  "",
			setup: func(t *testing.T, dir string) {
				// dir 为空字符串，不应 panic
			},
			wantNames: []string{},
		},
		{
			name: "loads_two_yaml_skips_readme",
			setup: func(t *testing.T, dir string) {
				_ = os.WriteFile(filepath.Join(dir, "README.md"), []byte("# not a playbook"), 0644)
				_ = os.WriteFile(filepath.Join(dir, "owasp-top10.yaml"), []byte(`name: OWASP Top 10 Assessment
description: Comprehensive assessment targeting the OWASP Top 10
phases:
  - name: reconnaissance
    tools:
      - subfinder
      - name: httpx
        options: { follow_redirects: true }
    post_analysis: |
      Map the attack surface.
  - name: injection_testing
    tools:
      - nuclei
    post_analysis: |
      Focus on injection.
`), 0644)
				_ = os.WriteFile(filepath.Join(dir, "api-security.yaml"), []byte(`name: API Security Assessment
description: Focused assessment of REST/GraphQL APIs
phases:
  - name: api_discovery
    tools:
      - name: katana
      - name: gau
    post_analysis: |
      Discover endpoints.
`), 0644)
			},
			wantNames: []string{"api-security", "owasp-top10"},
			check: func(t *testing.T, got []Playbook) {
				if len(got) != 2 {
					t.Fatalf("want 2 playbooks, got %d", len(got))
				}
				// 排序后应为 api-security, owasp-top10
				got0 := got[0]
				if got0.Name != "api-security" {
					t.Fatalf("got[0].Name = %q, want api-security", got0.Name)
				}
				if got0.DisplayName != "API Security Assessment" {
					t.Fatalf("got[0].DisplayName = %q, want 'API Security Assessment'", got0.DisplayName)
				}
				if got0.Description != "Focused assessment of REST/GraphQL APIs" {
					t.Fatalf("got[0].Description = %q", got0.Description)
				}
				if len(got0.Phases) != 1 {
					t.Fatalf("got[0] phases = %d, want 1", len(got0.Phases))
				}
				phase := got0.Phases[0]
				if phase.Name != "api_discovery" {
					t.Fatalf("phase.Name = %q, want api_discovery", phase.Name)
				}
				// 对象数组写法：应解析为工具名列表 [katana, gau]
				wantTools := []string{"katana", "gau"}
				if !reflect.DeepEqual(phase.Tools, wantTools) {
					t.Fatalf("phase.Tools = %v, want %v", phase.Tools, wantTools)
				}
				if phase.PostAnalysis == "" {
					t.Fatal("phase.PostAnalysis should not be empty")
				}

				got1 := got[1]
				if got1.Name != "owasp-top10" {
					t.Fatalf("got[1].Name = %q, want owasp-top10", got1.Name)
				}
				if got1.DisplayName != "OWASP Top 10 Assessment" {
					t.Fatalf("got[1].DisplayName = %q", got1.DisplayName)
				}
				if len(got1.Phases) != 2 {
					t.Fatalf("got[1] phases = %d, want 2", len(got1.Phases))
				}
				// 混合写法：字符串 + 对象
				mixedTools := got1.Phases[0].Tools
				wantMixed := []string{"subfinder", "httpx"}
				if !reflect.DeepEqual(mixedTools, wantMixed) {
					t.Fatalf("phase0.Tools = %v, want %v", mixedTools, wantMixed)
				}
				// FilePath 应记录源文件路径
				if got1.FilePath == "" {
					t.Fatal("got[1].FilePath should not be empty")
				}
			},
		},
		{
			name: "display_name_falls_back_to_filename_when_yaml_name_missing",
			setup: func(t *testing.T, dir string) {
				_ = os.WriteFile(filepath.Join(dir, "ctf-solver.yaml"), []byte(`description: CTF solver without top-level name
phases:
  - name: enumeration
    tools: [nmap, httpx]
`), 0644)
			},
			wantNames: []string{"ctf-solver"},
			check: func(t *testing.T, got []Playbook) {
				if len(got) != 1 {
					t.Fatalf("want 1 playbook, got %d", len(got))
				}
				if got[0].Name != "ctf-solver" {
					t.Fatalf("Name = %q, want ctf-solver", got[0].Name)
				}
				if got[0].DisplayName != "ctf-solver" {
					t.Fatalf("DisplayName = %q, want ctf-solver (fallback)", got[0].DisplayName)
				}
				// 字符串数组写法 tools: [nmap, httpx]
				wantTools := []string{"nmap", "httpx"}
				if !reflect.DeepEqual(got[0].Phases[0].Tools, wantTools) {
					t.Fatalf("Tools = %v, want %v", got[0].Phases[0].Tools, wantTools)
				}
			},
		},
		{
			name: "skips_non_yaml_files",
			setup: func(t *testing.T, dir string) {
				_ = os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore me"), 0644)
				_ = os.WriteFile(filepath.Join(dir, "image.png"), []byte("not yaml"), 0644)
				_ = os.WriteFile(filepath.Join(dir, "pheromones.yaml"), []byte(`name: Pheromones
description: tuning
phases:
  - name: t
    tools: []
`), 0644)
			},
			wantNames: []string{"pheromones"},
		},
		{
			name: "malformed_yaml_skipped_others_still_load",
			setup: func(t *testing.T, dir string) {
				_ = os.WriteFile(filepath.Join(dir, "broken.yaml"), []byte("name: [unclosed\n  bad: : :"), 0644)
				_ = os.WriteFile(filepath.Join(dir, "good.yaml"), []byte(`name: Good
description: ok
phases:
  - name: p1
    tools: [t1]
`), 0644)
			},
			wantNames: []string{"good"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := tt.dir
			if dir == "" && tt.name != "empty_string_dir_returns_empty_no_error" {
				dir = t.TempDir()
			}
			// 对于空字符串测试用例，dir 保持 ""；对于其他用例若 tt.dir 指定则用之
			if tt.dir != "" {
				dir = tt.dir
			} else if tt.name != "empty_string_dir_returns_empty_no_error" {
				dir = t.TempDir()
			}

			if tt.setup != nil {
				tt.setup(t, dir)
			}

			got, err := LoadPlaybooksFromDir(dir)
			if tt.wantErr && err == nil {
				t.Fatal("want error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			gotNames := make([]string, 0, len(got))
			for _, p := range got {
				gotNames = append(gotNames, p.Name)
			}
			if len(tt.wantNames) == 0 {
				if len(got) != 0 {
					t.Fatalf("want 0 playbooks, got %d (%+v)", len(got), got)
				}
			} else {
				if !reflect.DeepEqual(gotNames, tt.wantNames) {
					t.Fatalf("Names = %v, want %v", gotNames, tt.wantNames)
				}
			}

			if tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}
