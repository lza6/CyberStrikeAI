package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestE2EConfigLoadReactionsFromYAML K2/K4 E2E：从真实 YAML（含 reactions 段）
// 解析 → applyDefaultReactions 补齐 → 验证用户规则覆盖默认 + 默认补齐共存。
// 验证 config.Load 的 reactions 默认接入在真实 YAML 场景下可用。
func TestE2EConfigLoadReactionsFromYAML(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	// 用户只覆盖 high-impact-tool 的 message，其余用默认。
	yamlContent := `server:
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
reactions:
  enabled: true
  rules:
    high-impact-tool:
      auto: true
      action: notify
      priority: urgent
      message: "custom HIGH_IMPACT message"
`
	if err := os.WriteFile(cfgPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}
	// 不设 CYBERSTRIKEAI_HOME（避免 home 回退干扰本测试）。
	t.Setenv("CYBERSTRIKEAI_HOME", "")

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	// 用户配置的 high-impact-tool 应保留（user wins）。
	h := cfg.Reactions.Rules["high-impact-tool"]
	if h.Message != "custom HIGH_IMPACT message" {
		t.Fatalf("user high-impact-tool message should win, got %q", h.Message)
	}
	// 其余默认应补齐。
	if _, ok := cfg.Reactions.Rules["scope-violation"]; !ok {
		t.Fatal("scope-violation default should be merged in")
	}
	if _, ok := cfg.Reactions.Rules["hitl-pending"]; !ok {
		t.Fatal("hitl-pending default should be merged in")
	}
	if _, ok := cfg.Reactions.Rules["agent-stuck"]; !ok {
		t.Fatal("agent-stuck default should be merged in")
	}
	if !cfg.Reactions.EnabledEffective() {
		t.Fatal("enabled=true should be effective")
	}
}

// TestE2EConfigLoadReactionsDisabled K2 E2E：enabled=false 生效。
func TestE2EConfigLoadReactionsDisabled(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	yamlContent := `server:
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
reactions:
  enabled: false
`
	if err := os.WriteFile(cfgPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CYBERSTRIKEAI_HOME", "")
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Reactions.EnabledEffective() {
		t.Fatal("enabled=false should be effective (EnabledEffective()=false)")
	}
	// 即便 disabled，默认 reactions 仍补齐（供 enabled 切回 true 时可用）。
	if _, ok := cfg.Reactions.Rules["high-impact-tool"]; !ok {
		t.Fatal("defaults should still be merged even when disabled")
	}
}
