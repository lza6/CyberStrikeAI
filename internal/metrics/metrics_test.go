package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// 本测试文件各用例用独立 label 组合隔离，不跨用例共享 label，
// 避免累计值相互污染（counter 不能重置）。

func TestRecordHTTP(t *testing.T) {
	tok := BeginHTTP("GET", "/test-unique-path-1")
	EndHTTP(tok, 200)

	count := testutil.ToFloat64(HTTPRequestsTotal.WithLabelValues("GET", "/test-unique-path-1", "200"))
	if count != 1 {
		t.Errorf("after 1 request, counter = %v, want 1", count)
	}

	tok2 := BeginHTTP("GET", "/test-unique-path-1")
	EndHTTP(tok2, 200)
	tok3 := BeginHTTP("GET", "/test-unique-path-1")
	EndHTTP(tok3, 200)
	count2 := testutil.ToFloat64(HTTPRequestsTotal.WithLabelValues("GET", "/test-unique-path-1", "200"))
	if count2 != 3 {
		t.Errorf("after 3 requests, counter = %v, want 3", count2)
	}
}

func TestRecordHTTPDifferentStatus(t *testing.T) {
	tok := BeginHTTP("POST", "/test-unique-path-2")
	EndHTTP(tok, 500)
	count := testutil.ToFloat64(HTTPRequestsTotal.WithLabelValues("POST", "/test-unique-path-2", "500"))
	if count != 1 {
		t.Errorf("500 status counter = %v, want 1", count)
	}
}

func TestRecordHTTPNilTokenSafe(t *testing.T) {
	// nil/非法 token 不应 panic
	EndHTTP(nil, 200)
	EndHTTP("not-a-recorder", 200)
}

func TestSetActiveSessions(t *testing.T) {
	SetActiveSessions(5)
	v := testutil.ToFloat64(ActiveSessions)
	if v != 5 {
		t.Errorf("ActiveSessions = %v, want 5", v)
	}
	SetActiveSessions(3)
	v2 := testutil.ToFloat64(ActiveSessions)
	if v2 != 3 {
		t.Errorf("ActiveSessions after update = %v, want 3", v2)
	}
}

func TestRecordToolExecution(t *testing.T) {
	RecordToolExecution("sqlmap-test", "success")
	RecordToolExecution("sqlmap-test", "success")
	RecordToolExecution("sqlmap-test", "failure")

	success := testutil.ToFloat64(ToolExecutionsTotal.WithLabelValues("sqlmap-test", "success"))
	if success != 2 {
		t.Errorf("tool success counter = %v, want 2", success)
	}
	failure := testutil.ToFloat64(ToolExecutionsTotal.WithLabelValues("sqlmap-test", "failure"))
	if failure != 1 {
		t.Errorf("tool failure counter = %v, want 1", failure)
	}
}

func TestRecordAgentTurn(t *testing.T) {
	RecordAgentTurn("single-test", "success")
	RecordAgentTurn("single-test", "success")
	RecordAgentTurn("multi-test", "success")

	single := testutil.ToFloat64(AgentTurnsTotal.WithLabelValues("single-test", "success"))
	if single != 2 {
		t.Errorf("single agent turn counter = %v, want 2", single)
	}
	multi := testutil.ToFloat64(AgentTurnsTotal.WithLabelValues("multi-test", "success"))
	if multi != 1 {
		t.Errorf("multi agent turn counter = %v, want 1", multi)
	}
}

func TestRecordLLMToken(t *testing.T) {
	RecordLLMToken("openai-test", "prompt", 100)
	RecordLLMToken("openai-test", "prompt", 50)
	RecordLLMToken("openai-test", "completion", 80)

	prompt := testutil.ToFloat64(LLMTokenUsage.WithLabelValues("openai-test", "prompt"))
	if prompt != 150 {
		t.Errorf("prompt token counter = %v, want 150", prompt)
	}
	completion := testutil.ToFloat64(LLMTokenUsage.WithLabelValues("openai-test", "completion"))
	if completion != 80 {
		t.Errorf("completion token counter = %v, want 80", completion)
	}
}

func TestRecordLLMTokenZeroAndNegativeIgnored(t *testing.T) {
	// count <= 0 应忽略，不增加 counter
	RecordLLMToken("openai-neg-test", "prompt", 0)
	RecordLLMToken("openai-neg-test", "prompt", -5)
	v := testutil.ToFloat64(LLMTokenUsage.WithLabelValues("openai-neg-test", "prompt"))
	if v != 0 {
		t.Errorf("count<=0 不应递增 counter, got %v", v)
	}
}

func TestHandlerReturnsNonNil(t *testing.T) {
	h := Handler()
	if h == nil {
		t.Fatal("Handler 返回 nil")
	}
}
