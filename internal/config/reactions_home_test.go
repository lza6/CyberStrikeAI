package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadDefaultsHomeDirFromEnv K4：YAML 未配 storage.home_dir 时，回退到
// $CYBERSTRIKEAI_HOME（Convention over configuration，移植自 agent-orchestrator）。
func TestLoadDefaultsHomeDirFromEnv(t *testing.T) {
	// 准备一个最小 config.yaml（无 storage 段）。
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	minimal := `server:
  host: 127.0.0.1
  port: 0
log:
  level: info
mcp:
  enabled: true
agent:
  model: test
security:
  tools_dir: ""
database:
  path: data/conversations.db
auth:
  session_duration_hours: 12
`
	if err := os.WriteFile(cfgPath, []byte(minimal), 0644); err != nil {
		t.Fatal(err)
	}

	// 设环境变量指向临时目录。
	homeDir := t.TempDir()
	t.Setenv("CYBERSTRIKEAI_HOME", homeDir)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Storage.HomeDir != homeDir {
		t.Fatalf("Storage.HomeDir = %q, want %q (from env)", cfg.Storage.HomeDir, homeDir)
	}
	// 不应有副作用（Load 不触发迁移）。
	if cfg.Reactions.Rules == nil {
		t.Fatal("Reactions.Rules should be initialized by applyDefaultReactions")
	}
}

// TestLoadExplicitHomeDirOverridesEnv K4：YAML 显式 home_dir 覆盖环境变量。
func TestLoadExplicitHomeDirOverridesEnv(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	withHome := `server:
  host: 127.0.0.1
  port: 0
log:
  level: info
mcp:
  enabled: true
agent:
  model: test
security:
  tools_dir: ""
database:
  path: data/conversations.db
auth:
  session_duration_hours: 12
storage:
  home_dir: /explicit/home
`
	if err := os.WriteFile(cfgPath, []byte(withHome), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CYBERSTRIKEAI_HOME", "/env/should/be/ignored")

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Storage.HomeDir != "/explicit/home" {
		t.Fatalf("Storage.HomeDir = %q, want /explicit/home", cfg.Storage.HomeDir)
	}
}

// TestApplyDefaultReactionsMergesDefaults K2：用户未配的 key 用默认补齐。
func TestApplyDefaultReactionsMergesDefaults(t *testing.T) {
	cfg := &Config{}
	applyDefaultReactions(cfg)
	if cfg.Reactions.Rules == nil {
		t.Fatal("Rules should be initialized")
	}
	// 默认应含 high-impact-tool 等。
	if _, ok := cfg.Reactions.Rules["high-impact-tool"]; !ok {
		t.Fatal("default should include high-impact-tool")
	}
	if _, ok := cfg.Reactions.Rules["scope-violation"]; !ok {
		t.Fatal("default should include scope-violation")
	}
	if _, ok := cfg.Reactions.Rules["run-complete"]; !ok {
		t.Fatal("default should include run-complete")
	}
}

// TestApplyDefaultReactionsSessionStatusDefault P2-3：lifecycle 派生的
// session-status finding 必须有默认规则（log-only），否则触发后 no-op。
func TestApplyDefaultReactionsSessionStatusDefault(t *testing.T) {
	cfg := &Config{}
	applyDefaultReactions(cfg)
	r, ok := cfg.Reactions.Rules["session-status"]
	if !ok {
		t.Fatal("default should include session-status")
	}
	if !r.Auto {
		t.Fatal("session-status default should be auto")
	}
	if r.Action != "log-only" {
		t.Fatalf("session-status default action = %q, want log-only", r.Action)
	}
	if r.Priority != "low" {
		t.Fatalf("session-status default priority = %q, want low", r.Priority)
	}
}

// TestApplyDefaultReactionsUserWins K2：用户配的 key 不被默认覆盖（user wins）。
func TestApplyDefaultReactionsUserWins(t *testing.T) {
	custom := "custom message"
	cfg := &Config{
		Reactions: ReactionsConfig{
			Rules: map[string]Reaction{
				"high-impact-tool": {Auto: false, Action: "log-only", Message: custom},
			},
		},
	}
	applyDefaultReactions(cfg)
	got := cfg.Reactions.Rules["high-impact-tool"]
	if got.Auto != false || got.Action != "log-only" || got.Message != custom {
		t.Fatalf("user config should win, got %+v", got)
	}
	// 其余 key 仍补默认。
	if _, ok := cfg.Reactions.Rules["scope-violation"]; !ok {
		t.Fatal("scope-violation default should be merged in")
	}
}

// TestReactionsEnabledEffectiveDefaultsTrue K2：省略 enabled → true。
func TestReactionsEnabledEffectiveDefaultsTrue(t *testing.T) {
	r := ReactionsConfig{}
	if !r.EnabledEffective() {
		t.Fatal("default should be enabled")
	}
	f := false
	r2 := ReactionsConfig{Enabled: &f}
	if r2.EnabledEffective() {
		t.Fatal("explicit false should be false")
	}
}

// TestApplyDefaultReactionsUserPartialWins K2：用户只配部分字段也整 key 覆盖（参考项目语义）。
// 这与参考项目 applyDefaultReactions 一致：user 整 key 覆盖 default，非字段级合并。
func TestApplyDefaultReactionsUserPartialWins(t *testing.T) {
	cfg := &Config{
		Reactions: ReactionsConfig{
			Rules: map[string]Reaction{
				"high-impact-tool": {Message: "only message"}, // 只填 1 字段
			},
		},
	}
	applyDefaultReactions(cfg)
	got := cfg.Reactions.Rules["high-impact-tool"]
	// 整 key 覆盖：用户只填 Message，其余字段为默认零值（Auto=false），不被默认补字段。
	if got.Message != "only message" {
		t.Fatalf("user Message should win, got %q", got.Message)
	}
	if got.Auto != false {
		t.Fatalf("user partial should not merge default fields, Auto should be zero false, got %v", got.Auto)
	}
	// 未配置的 key 仍补默认。
	if _, ok := cfg.Reactions.Rules["agent-idle"]; !ok {
		t.Fatal("agent-idle default should be merged in")
	}
}

// 防止 import 未使用告警（若上述测试不引用 strings）。
var _ = strings.TrimSpace
