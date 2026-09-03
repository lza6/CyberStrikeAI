package knowledge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/mcp"
	"cyberstrike-ai/internal/openai"

	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// ---- tool.go ----

func newToolTestEnv(t *testing.T) (*mcp.Server, *Retriever, *Manager) {
	t.Helper()
	db, e := newVectorTestSetup(t, []float32{1, 0, 0})
	r := NewRetriever(db, e, &RetrievalConfig{TopK: 5, SimilarityThreshold: 0.1}, zap.NewNop())
	m := NewManager(db, "", nil)
	srv := mcp.NewServer(zap.NewNop())
	RegisterKnowledgeTool(srv, r, m, zap.NewNop())
	return srv, r, m
}

func TestRegisterKnowledgeTool_ListRiskTypes(t *testing.T) {
	srv, _, _ := newToolTestEnv(t)

	res, _, err := srv.CallTool(t.Context(), "list_knowledge_risk_types", map[string]any{})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res == nil || res.IsError {
		t.Fatalf("result = %+v", res)
	}
	if len(res.Content) == 0 || res.Content[0].Text == "" {
		t.Fatalf("empty content: %+v", res)
	}
}

func TestRegisterKnowledgeTool_SearchBasic(t *testing.T) {
	srv, _, _ := newToolTestEnv(t)

	res, _, err := srv.CallTool(t.Context(), "search_knowledge_base", map[string]any{
		"query": "xss",
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.IsError {
		t.Fatalf("search error: %+v", res)
	}
	text := res.Content[0].Text
	if !strings.Contains(text, "知识片段") {
		t.Fatalf("unexpected text: %s", text)
	}
	if !strings.Contains(text, "METADATA") {
		t.Fatalf("metadata block missing: %s", text)
	}
}

func TestRegisterKnowledgeTool_SearchWithRiskType(t *testing.T) {
	srv, _, _ := newToolTestEnv(t)

	res, _, err := srv.CallTool(t.Context(), "search_knowledge_base", map[string]any{
		"query":     "sql",
		"risk_type": "SQLi",
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.IsError {
		t.Fatalf("search error: %+v", res)
	}
	if strings.Contains(res.Content[0].Text, "XSS") && !strings.Contains(res.Content[0].Text, "SQLi") {
		t.Fatalf("risk filter not applied: %s", res.Content[0].Text)
	}
}

func TestRegisterKnowledgeTool_SearchEmptyQuery(t *testing.T) {
	srv, _, _ := newToolTestEnv(t)

	res, _, err := srv.CallTool(t.Context(), "search_knowledge_base", map[string]any{})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !res.IsError {
		t.Fatalf("empty query should be an error result: %+v", res)
	}
}

func TestRegisterKnowledgeTool_SearchNoResults(t *testing.T) {
	srv, _, _ := newToolTestEnv(t)

	res, _, err := srv.CallTool(t.Context(), "search_knowledge_base", map[string]any{
		"query":     "zzzz-no-match",
		"risk_type": "Nope",
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.IsError {
		t.Fatalf("no-result should not be error: %+v", res)
	}
	if !strings.Contains(res.Content[0].Text, "未找到") {
		t.Fatalf("expected no-result text: %s", res.Content[0].Text)
	}
}

// ---- eino_retrieve_chain.go: CompileRetrieveChain ----

func TestCompileRetrieveChain_Method(t *testing.T) {
	db, e := newVectorTestSetup(t, []float32{1, 0, 0})
	r := NewRetriever(db, e, &RetrievalConfig{TopK: 3, SimilarityThreshold: 0.1}, zap.NewNop())
	run, err := r.CompileRetrieveChain(t.Context())
	if err != nil {
		t.Fatalf("CompileRetrieveChain: %v", err)
	}
	docs, err := run.Invoke(t.Context(), "注入")
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if len(docs) == 0 {
		t.Fatalf("expected docs")
	}
}

// ---- retrieval_postprocess.go: encoding/countDocTokens via ApplyPostRetrieve ----

func TestApplyPostRetrieve_TokenBudget(t *testing.T) {
	docs := []*schema.Document{
		doc("1", "hello world this is a test", 0.9),
		doc("2", "another doc with words", 0.8),
	}
	// generous budget keeps both
	out, err := ApplyPostRetrieve(docs, &config.PostRetrieveConfig{MaxContextTokens: 100}, "gpt-4", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("len = %d", len(out))
	}
	// tight token budget keeps nothing (each doc exceeds 2 tokens)
	out2, err := ApplyPostRetrieve(docs, &config.PostRetrieveConfig{MaxContextTokens: 2}, "gpt-4", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(out2) != 0 {
		t.Fatalf("tight budget len = %d", len(out2))
	}
	// empty docs
	out3, err := ApplyPostRetrieve(nil, nil, "gpt-4", 5)
	if err != nil || len(out3) != 0 {
		t.Fatalf("empty docs: %d %v", len(out3), err)
	}
	// 无预算时不去除空白文档（dedupe 保留空正文）——显式验证 budget-only 过滤路径。
	// 用 budget 过滤空白文档：MaxContextChars=1 时空白 doc 的 runes=2 > 1 被跳过。
	out4, err := ApplyPostRetrieve([]*schema.Document{doc("1", "  ", 0.9), doc("2", "real", 0.8)}, &config.PostRetrieveConfig{MaxContextChars: 4}, "gpt-4", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(out4) != 1 || out4[0].ID != "2" {
		t.Fatalf("budget filter: %#v", out4)
	}
	// dedupe keeps first occurrence
	out5, err := ApplyPostRetrieve([]*schema.Document{doc("1", "same", 0.9), doc("2", " same ", 0.8)}, nil, "gpt-4", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(out5) != 1 || out5[0].ID != "1" {
		t.Fatalf("dedupe: %#v", out5)
	}
	// topK default when < 1
	out6, err := ApplyPostRetrieve(docs, nil, "gpt-4", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(out6) != 2 {
		t.Fatalf("default topK keeps all: %d", len(out6))
	}
}

// ---- wire_retriever.go ----

func TestWireRetrieverPipeline_Errors(t *testing.T) {
	if err := WireRetrieverPipeline(t.Context(), nil, nil); err == nil {
		t.Fatalf("nil retriever should error")
	}
	db, e := newVectorTestSetup(t, []float32{1, 0, 0})
	r := NewRetriever(db, e, &RetrievalConfig{TopK: 3}, zap.NewNop())
	if err := WireRetrieverPipeline(t.Context(), r, nil); err == nil {
		t.Fatalf("nil openai should error")
	}
	if err := WireRetrieverPipeline(t.Context(), r, &config.OpenAIConfig{}); err == nil {
		// r.config 非空，走到 reranker 构造：openai.APIKey 为空 -> reranker 报错
		t.Fatalf("missing api key should error through reranker")
	}
}

func TestWireRetrieverPipeline_WithMockOpenAI(t *testing.T) {
	db, e := newVectorTestSetup(t, []float32{1, 0, 0})
	// 把 knowledge_embeddings 的向量改成与 mock embedder 输出一致，保证向量召回有命中。
	embJSON, _ := json.Marshal([]float32{1.0, 0.0, 0.0})
	if _, err := db.Exec(`UPDATE knowledge_embeddings SET embedding=?, embedding_model='text-embedding-3-small', embedding_dim=3`, string(embJSON)); err != nil {
		t.Fatal(err)
	}
	// mock OpenAI-compatible chat completions endpoint for MultiQuery rewrite.
	var callCount int
	chatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		_ = jsonEncode(w, map[string]any{
			"id":     "chatcmpl-test",
			"object": "chat.completion",
			"choices": []map[string]any{
				{
					"index":         0,
					"message":       map[string]any{"role": "assistant", "content": "xss\nxss defense"},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]int{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer chatSrv.Close()

	r := NewRetriever(db, e, &RetrievalConfig{
		TopK:                3,
		SimilarityThreshold: 0.1,
		MultiQuery:          config.MultiQueryConfig{MaxQueries: 2},
		Rerank:              config.RerankConfig{Provider: "cohere"},
		PostRetrieve:        config.PostRetrieveConfig{},
	}, zap.NewNop())

	oa := &config.OpenAIConfig{
		Provider: "openai",
		APIKey:   "test-key",
		BaseURL:  chatSrv.URL,
		Model:    "gpt-4o-mini",
	}
	if err := WireRetrieverPipeline(t.Context(), r, oa); err != nil {
		t.Fatalf("WireRetrieverPipeline: %v", err)
	}
	if r.pipeline == nil {
		t.Fatalf("pipeline not set")
	}

	// Search now routes through MultiQuery pipeline (LLM rewrite + vector + rerank + post).
	res, err := r.Search(t.Context(), &SearchRequest{Query: "xss", TopK: 3, Threshold: 0.1})
	if err != nil {
		t.Fatalf("Search through pipeline: %v", err)
	}
	if len(res) == 0 {
		t.Fatalf("expected results through pipeline")
	}
	if callCount == 0 {
		t.Fatalf("rewrite LLM was not called")
	}

	// UpdateConfig with wireOpenAI set re-wires the pipeline.
	r.UpdateConfig(&RetrievalConfig{
		TopK:                2,
		SimilarityThreshold: 0.1,
		MultiQuery:          config.MultiQueryConfig{MaxQueries: 2},
	})
	if r.pipeline == nil {
		t.Fatalf("pipeline should be re-wired after UpdateConfig")
	}
	_ = retriever.WithTopK
	_ = openai.NewEinoHTTPClient
}
