package knowledge

import (
	"os"
	"path/filepath"
	"testing"

	"cyberstrike-ai/internal/config"
)

// writeTempConfig 写临时 config.yaml 并用 config.Load 加载。
func writeTempConfig(t *testing.T, content string) *config.Config {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return cfg
}

// TestKnowledgeConfigGraphUnmarshal 验收：config.yaml 的 knowledge.graph 段能正确反序列化到
// config.KnowledgeConfig.Graph（LightRAG 迁移的配置入口）。
func TestKnowledgeConfigGraphUnmarshal(t *testing.T) {
	yamlBytes := []byte(`
knowledge:
  enabled: true
  base_path: knowledge_base
  embedding:
    provider: openai
    model: text-embedding-v4
  retrieval:
    top_k: 5
    similarity_threshold: 0.4
  graph:
    enabled: true
    backend: memory
    entity_types: ["CVE", "漏洞", "攻击技术"]
    default_search_mode: local
    top_k: 8
    similarity_threshold: 0.3
    use_llm_extractor: false
`)
	cfg := writeTempConfig(t, string(yamlBytes))
	g := cfg.Knowledge.Graph
	if !g.Enabled {
		t.Errorf("graph.enabled = false, want true")
	}
	if g.EffectiveBackend() != "memory" {
		t.Errorf("graph.backend = %q, want memory", g.EffectiveBackend())
	}
	if len(g.EntityTypes) != 3 {
		t.Errorf("graph.entity_types len = %d, want 3", len(g.EntityTypes))
	}
	if g.EffectiveDefaultSearchMode() != "local" {
		t.Errorf("graph.default_search_mode = %q, want local", g.EffectiveDefaultSearchMode())
	}
	if g.EffectiveTopK(5) != 8 {
		t.Errorf("graph.top_k effective = %d, want 8", g.EffectiveTopK(5))
	}
	if g.EffectiveSimilarityThreshold() != 0.3 {
		t.Errorf("graph.similarity_threshold = %v, want 0.3", g.EffectiveSimilarityThreshold())
	}
}

// TestKnowledgeConfigGraphDefaults 验收：graph 段缺省时 Effective* 回退正确（向后兼容）。
func TestKnowledgeConfigGraphDefaults(t *testing.T) {
	yamlBytes := []byte(`
knowledge:
  enabled: true
  base_path: knowledge_base
`)
	cfg := writeTempConfig(t, string(yamlBytes))
	g := cfg.Knowledge.Graph
	if g.Enabled {
		t.Errorf("graph.enabled default should be false")
	}
	if g.EffectiveBackend() != "sqlite" {
		t.Errorf("graph.backend default = %q, want sqlite", g.EffectiveBackend())
	}
	if g.EffectiveDefaultSearchMode() != "hybrid" {
		t.Errorf("graph.mode default = %q, want hybrid", g.EffectiveDefaultSearchMode())
	}
	if g.EffectiveTopK(5) != 5 {
		t.Errorf("graph.top_k should fall back to retrieval top_k 5, got %d", g.EffectiveTopK(5))
	}
	if g.EffectiveSimilarityThreshold() != 0.2 {
		t.Errorf("graph.threshold default = %v, want 0.2", g.EffectiveSimilarityThreshold())
	}
}
