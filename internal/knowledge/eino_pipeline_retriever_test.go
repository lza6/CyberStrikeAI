package knowledge

import (
	"context"
	"fmt"
	"testing"

	"cyberstrike-ai/internal/config"

	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// 本文件覆盖 knowledgePipelineRetriever.Retrieve 的关键行为：
//   - 重排器被调用（spy reranker 计数）
//   - 最终返回数不超过 TopK
//   - post-process 去重生效
// 不接触真实 LLM / 真实 rerank API，全部用 stub。

// spyReranker 记录 Rerank 调用次数，并按预设顺序返回文档（便于断言被调用）。
type spyReranker struct {
	calls int
	// order 若非 nil，按指定 ID 顺序重排后返回；否则原序返回。
	order []string
}

func (s *spyReranker) Rerank(_ context.Context, _ string, docs []*schema.Document) ([]*schema.Document, error) {
	s.calls++
	if len(s.order) == 0 {
		return docs, nil
	}
	byID := make(map[string]*schema.Document, len(docs))
	for _, d := range docs {
		if d == nil {
			continue
		}
		byID[d.ID] = d
	}
	out := make([]*schema.Document, 0, len(s.order))
	for _, id := range s.order {
		if d, ok := byID[id]; ok {
			out = append(out, d)
		}
	}
	if len(out) == 0 {
		return docs, nil
	}
	return out, nil
}

// stubRetriever 固定返回预设文档，模拟 inner retriever（multi-query/vector）的输出。
type stubRetriever struct {
	docs []*schema.Document
	err  error
}

func (s *stubRetriever) GetType() string { return "stub" }

func (s *stubRetriever) Retrieve(_ context.Context, _ string, _ ...retriever.Option) ([]*schema.Document, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.docs, nil
}

func newDoc(id, content string, score float64) *schema.Document {
	d := &schema.Document{
		ID:      id,
		Content: content,
		MetaData: map[string]any{
			metaKBItemID:     "item-" + id,
			metaKBCategory:   "cat",
			metaKBTitle:      "title-" + id,
			metaKBChunkIndex: 0,
		},
	}
	d.WithScore(score)
	return d
}

// newPipelineTestRetriever 构造一个 Retriever + 注入 spy reranker + 用 stubRetriever 作为 inner，
// 走 knowledgePipelineRetriever.Retrieve 全路径（rerank + post-process）。
func newPipelineTestRetriever(t *testing.T, cfg *RetrievalConfig, inner retriever.Retriever, spy *spyReranker) *Retriever {
	t.Helper()
	if cfg == nil {
		cfg = &RetrievalConfig{TopK: 5, SimilarityThreshold: 0.7}
	}
	r := NewRetriever(nil, nil, cfg, zap.NewNop())
	r.SetDocumentReranker(spy)
	r.pipeline = newKnowledgePipelineRetriever(inner, r)
	return r
}

// TestPipelineRetrieve_RerankCalled 断言 pipeline.Retrieve 调用了注入的 reranker。
func TestPipelineRetrieve_RerankCalled(t *testing.T) {
	spy := &spyReranker{}
	inner := &stubRetriever{docs: []*schema.Document{
		newDoc("a", "alpha", 0.9),
		newDoc("b", "beta", 0.8),
		newDoc("c", "gamma", 0.7),
	}}
	r := newPipelineTestRetriever(t, &RetrievalConfig{TopK: 5, SimilarityThreshold: 0.7}, inner, spy)

	out, err := r.activeEinoRetriever().Retrieve(context.Background(), "q")
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if spy.calls != 1 {
		t.Fatalf("expect reranker called once, got %d", spy.calls)
	}
	if len(out) != 3 {
		t.Fatalf("expect 3 docs (no truncation under TopK=5), got %d", len(out))
	}
}

// TestPipelineRetrieve_TopKCorrect 最终返回数不超过 TopK（即便 inner 返回更多）。
func TestPipelineRetrieve_TopKCorrect(t *testing.T) {
	spy := &spyReranker{}
	inner := &stubRetriever{docs: []*schema.Document{
		newDoc("a", "alpha", 0.9),
		newDoc("b", "beta", 0.8),
		newDoc("c", "gamma", 0.7),
		newDoc("d", "delta", 0.6),
		newDoc("e", "epsilon", 0.5),
	}}
	// TopK=2，inner 返回 5 条，最终应被截断到 2 条
	r := newPipelineTestRetriever(t, &RetrievalConfig{TopK: 2, SimilarityThreshold: 0.7}, inner, spy)

	out, err := r.activeEinoRetriever().Retrieve(context.Background(), "q", retriever.WithTopK(2))
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(out) > 2 {
		t.Fatalf("expect <= TopK=2 results, got %d", len(out))
	}
}

// TestPipelineRetrieve_TopKCorrect_RerankRespected 验证 rerank 顺序被保留到最终输出
// （spy 按 c,a 顺序返回，最终输出前两条应为 c,a，证明 rerank 结果未被 post-process 打乱）。
func TestPipelineRetrieve_TopKCorrect_RerankRespected(t *testing.T) {
	spy := &spyReranker{order: []string{"c", "a", "b"}}
	inner := &stubRetriever{docs: []*schema.Document{
		newDoc("a", "alpha", 0.9),
		newDoc("b", "beta", 0.8),
		newDoc("c", "gamma", 0.7),
	}}
	r := newPipelineTestRetriever(t, &RetrievalConfig{TopK: 5, SimilarityThreshold: 0.7}, inner, spy)

	out, err := r.activeEinoRetriever().Retrieve(context.Background(), "q", retriever.WithTopK(2))
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expect TopK=2 results, got %d", len(out))
	}
	if out[0].ID != "c" || out[1].ID != "a" {
		t.Fatalf("expect rerank order [c,a], got [%s,%s]", out[0].ID, out[1].ID)
	}
}

// TestPipelineRetrieve_PostProcessDedupe 重复内容文档被去重（dedupeByNormalizedContent）。
func TestPipelineRetrieve_PostProcessDedupe(t *testing.T) {
	spy := &spyReranker{}
	// a 与 b 内容相同（仅空白差异），应被去重为 1 条；c 不同
	inner := &stubRetriever{docs: []*schema.Document{
		newDoc("a", "hello   world", 0.9),
		newDoc("b", "hello world", 0.8),
		newDoc("c", "other content", 0.7),
	}}
	r := newPipelineTestRetriever(t, &RetrievalConfig{TopK: 5, SimilarityThreshold: 0.7}, inner, spy)

	out, err := r.activeEinoRetriever().Retrieve(context.Background(), "q")
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expect 2 after dedupe (a/b 合并), got %d (%+#v)", len(out), out)
	}
	// 保留首次出现的 a，丢弃 b
	if out[0].ID != "a" {
		t.Fatalf("expect first kept id=a, got %s", out[0].ID)
	}
}

// TestPipelineRetrieve_EmptyQuery 空查询直接返回错误，不调用 inner。
func TestPipelineRetrieve_EmptyQuery(t *testing.T) {
	spy := &spyReranker{}
	inner := &stubRetriever{docs: []*schema.Document{newDoc("a", "x", 0.9)}}
	r := newPipelineTestRetriever(t, nil, inner, spy)

	_, err := r.activeEinoRetriever().Retrieve(context.Background(), "   ")
	if err == nil {
		t.Fatal("expect error for empty query")
	}
	if spy.calls != 0 {
		t.Fatalf("expect reranker not called for empty query, got %d", spy.calls)
	}
}

// TestPipelineRetrieve_InnerError inner 报错时 rerank 不被调用，错误向上传播。
func TestPipelineRetrieve_InnerError(t *testing.T) {
	spy := &spyReranker{}
	innerErr := fmt.Errorf("boom")
	inner := &stubRetriever{err: innerErr}
	r := newPipelineTestRetriever(t, nil, inner, spy)

	_, err := r.activeEinoRetriever().Retrieve(context.Background(), "q")
	if err == nil {
		t.Fatal("expect inner error to propagate")
	}
	if spy.calls != 0 {
		t.Fatalf("expect reranker not called when inner errors, got %d", spy.calls)
	}
}

// TestPipelineRetrieve_NilReranker 未注入 reranker 时 pipeline 仍可正常工作（跳过精排，只做 post-process）。
func TestPipelineRetrieve_NilReranker(t *testing.T) {
	inner := &stubRetriever{docs: []*schema.Document{
		newDoc("a", "alpha", 0.9),
		newDoc("b", "beta", 0.8),
	}}
	r := NewRetriever(nil, nil, &RetrievalConfig{TopK: 5, SimilarityThreshold: 0.7}, zap.NewNop())
	// 不调用 SetDocumentReranker，documentReranker() 返回 nil
	r.pipeline = newKnowledgePipelineRetriever(inner, r)

	out, err := r.activeEinoRetriever().Retrieve(context.Background(), "q")
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expect 2 docs without rerank, got %d", len(out))
	}
}

// 编译期断言：确保 stubRetriever / spyReranker 实现接口。
var (
	_ retriever.Retriever = (*stubRetriever)(nil)
	_ DocumentReranker    = (*spyReranker)(nil)
)

// 引用 config 包避免 unused（PostRetrieveConfig 在 cfg 中使用，此处显式引用以防止未来删除时编译错误）。
var _ = config.PostRetrieveConfig{}
