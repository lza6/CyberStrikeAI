package multiagent

import (
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

// TestStuckDetectorSameOutputRepeat 连续 3 次相同输出（归一化后）触发。
func TestStuckDetectorSameOutputRepeat(t *testing.T) {
	d := NewStuckDetector(DefaultStuckDetectorConfig())
	convID := "conv-1"
	content := "scan complete: port 80 open\nport 443 open"

	// 第 1 次：repeat=1，不触发。
	if ev := d.ObserveAssistantOutput(convID, content, nil); ev != nil {
		t.Fatalf("1st should not trigger, got %+v", ev)
	}
	// 第 2 次：repeat=2，不触发。
	if ev := d.ObserveAssistantOutput(convID, content, nil); ev != nil {
		t.Fatalf("2nd should not trigger, got %+v", ev)
	}
	// 第 3 次：repeat=3，触发 same-output-repeat。
	ev := d.ObserveAssistantOutput(convID, content, nil)
	if ev == nil || ev.Kind != "same-output-repeat" {
		t.Fatalf("3rd should trigger same-output-repeat, got %+v", ev)
	}
	if ev.Count != 3 || ev.Threshold != 3 {
		t.Fatalf("event count/threshold mismatch: %+v", ev)
	}
}

// TestStuckDetectorSameOutputRepeatNormalized 时间戳/进度不同但内容相同 → 哈希相同 → 触发。
func TestStuckDetectorSameOutputRepeatNormalized(t *testing.T) {
	d := NewStuckDetector(DefaultStuckDetectorConfig())
	convID := "conv-norm"
	out1 := "12:34:56 scan complete: port 80 open"
	out2 := "12:35:57 scan complete: port 80 open"
	out3 := "12:36:58 scan complete: port 80 open"

	if ev := d.ObserveAssistantOutput(convID, out1, nil); ev != nil {
		t.Fatalf("1st should not trigger")
	}
	if ev := d.ObserveAssistantOutput(convID, out2, nil); ev != nil {
		t.Fatalf("2nd should not trigger")
	}
	ev := d.ObserveAssistantOutput(convID, out3, nil)
	if ev == nil || ev.Kind != "same-output-repeat" {
		t.Fatalf("3rd should trigger after normalization, got %+v", ev)
	}
}

// TestStuckDetectorReconWhitelistExempt recon 工具白名单豁免 sameOutputRepeat。
func TestStuckDetectorReconWhitelistExempt(t *testing.T) {
	d := NewStuckDetector(DefaultStuckDetectorConfig())
	convID := "conv-recon"
	content := "80/tcp open http\n443/tcp open https"
	// nmap 在白名单：相同输出 3 次不触发 same-output-repeat。
	call := schema.ToolCall{Function: schema.FunctionCall{Name: "nmap"}}
	for i := 0; i < 5; i++ {
		if ev := d.ObserveAssistantOutput(convID, content, []schema.ToolCall{call}); ev != nil {
			t.Fatalf("recon whitelist should exempt same-output-repeat, got %+v at iter %d", ev, i)
		}
	}
}

// TestStuckDetectorReconWhitelistByName 命令前缀匹配（execute 内 nmap 命令）。
func TestStuckDetectorReconWhitelistByName(t *testing.T) {
	if !isReconTool("nmap") {
		t.Fatal("nmap should be recon")
	}
	if !isReconTool("nmap -sV target") {
		t.Fatal("nmap prefix should match")
	}
	if !isReconTool("MASSCAN") {
		t.Fatal("masscan case-insensitive")
	}
	if isReconTool("curl") {
		t.Fatal("curl should not be recon")
	}
	if isReconTool("") {
		t.Fatal("empty should not be recon")
	}
}

// TestStuckDetectorSameErrorRepeat 连续 2 次相同错误触发。
func TestStuckDetectorSameErrorRepeat(t *testing.T) {
	d := NewStuckDetector(DefaultStuckDetectorConfig())
	convID := "conv-err"
	errContent := "connection refused: 127.0.0.1:8080"

	// 第 1 次：repeat=1，不触发。
	if ev := d.ObserveToolError(convID, "curl", errContent); ev != nil {
		t.Fatalf("1st error should not trigger, got %+v", ev)
	}
	// 第 2 次：repeat=2，触发 same-error-repeat。
	ev := d.ObserveToolError(convID, "curl", errContent)
	if ev == nil || ev.Kind != "same-error-repeat" {
		t.Fatalf("2nd error should trigger same-error-repeat, got %+v", ev)
	}
	if ev.Count != 2 || ev.Threshold != 2 {
		t.Fatalf("event count/threshold mismatch: %+v", ev)
	}
}

// TestStuckDetectorSameErrorReconExempt recon 工具错误豁免。
func TestStuckDetectorSameErrorReconExempt(t *testing.T) {
	d := NewStuckDetector(DefaultStuckDetectorConfig())
	convID := "conv-err-recon"
	errContent := "host unreachable"
	// nmap 错误豁免。
	for i := 0; i < 5; i++ {
		if ev := d.ObserveToolError(convID, "nmap", errContent); ev != nil {
			t.Fatalf("recon error should be exempt, got %+v at iter %d", ev, i)
		}
	}
}

// TestStuckDetectorRevisionLoop 连续 4 次相同工具调用参数触发 revision-loop。
// 内容每次不同以隔离 revision-loop 路径（避免 same-output-repeat 在 count=3 先触发）。
func TestStuckDetectorRevisionLoop(t *testing.T) {
	d := NewStuckDetector(DefaultStuckDetectorConfig())
	convID := "conv-rev"
	call := schema.ToolCall{
		Function: schema.FunctionCall{
			Name:      "execute",
			Arguments: `{"command":"grep -r secret /etc"}`,
		},
	}
	// 前 3 次不触发（toolArgRepeat 1..3）；内容每次不同避免 same-output-repeat。
	for i := 0; i < 3; i++ {
		content := "no secret found iter " + string(rune('A'+i))
		if ev := d.ObserveAssistantOutput(convID, content, []schema.ToolCall{call}); ev != nil {
			t.Fatalf("iter %d should not trigger, got %+v", i, ev)
		}
	}
	// 第 4 次：toolArgRepeat=4，触发 revision-loop。
	ev := d.ObserveAssistantOutput(convID, "no secret found iter D", []schema.ToolCall{call})
	if ev == nil || ev.Kind != "revision-loop" {
		t.Fatalf("4th should trigger revision-loop, got %+v", ev)
	}
	if ev.Count != 4 || ev.Threshold != 4 {
		t.Fatalf("event count/threshold mismatch: %+v", ev)
	}
}

// TestStuckDetectorRevisionLoopReconExempt recon 工具 revision-loop 豁免。
func TestStuckDetectorRevisionLoopReconExempt(t *testing.T) {
	d := NewStuckDetector(DefaultStuckDetectorConfig())
	convID := "conv-rev-recon"
	call := schema.ToolCall{
		Function: schema.FunctionCall{
			Name:      "nmap",
			Arguments: `{"command":"nmap -sV target"}`,
		},
	}
	content := "80 open"
	for i := 0; i < 6; i++ {
		if ev := d.ObserveAssistantOutput(convID, content, []schema.ToolCall{call}); ev != nil {
			t.Fatalf("recon revision-loop should be exempt, got %+v at iter %d", ev, i)
		}
	}
}

// TestStuckDetectorMonologue 连续 6 轮无工具调用触发 monologue。
// 内容每次不同以隔离 monologue 路径（避免 same-output-repeat 在 count=3 先触发）。
func TestStuckDetectorMonologue(t *testing.T) {
	d := NewStuckDetector(DefaultStuckDetectorConfig())
	convID := "conv-mono"
	// 前 5 轮不触发（monologueCount 1..5）；内容每次不同避免 same-output-repeat。
	for i := 0; i < 5; i++ {
		content := "thinking iter " + string(rune('A'+i))
		if ev := d.ObserveAssistantOutput(convID, content, nil); ev != nil {
			t.Fatalf("iter %d should not trigger, got %+v", i, ev)
		}
	}
	// 第 6 轮：monologueCount=6，触发 monologue。
	ev := d.ObserveAssistantOutput(convID, "thinking iter F", nil)
	if ev == nil || ev.Kind != "monologue" {
		t.Fatalf("6th should trigger monologue, got %+v", ev)
	}
	if ev.Count != 6 || ev.Threshold != 6 {
		t.Fatalf("event count/threshold mismatch: %+v", ev)
	}
}

// TestStuckDetectorMonologueResetByToolCall 工具调用重置 monologue 计数。
func TestStuckDetectorMonologueResetByToolCall(t *testing.T) {
	d := NewStuckDetector(DefaultStuckDetectorConfig())
	convID := "conv-mono-reset"
	call := schema.ToolCall{Function: schema.FunctionCall{Name: "ls"}}
	// 5 轮 monologue（不触发）。
	for i := 0; i < 5; i++ {
		d.ObserveAssistantOutput(convID, "thinking", nil)
	}
	// 一轮工具调用重置计数。
	d.ObserveAssistantOutput(convID, "result", []schema.ToolCall{call})
	// 再 5 轮 monologue（不触发，因计数已重置）。
	for i := 0; i < 5; i++ {
		if ev := d.ObserveAssistantOutput(convID, "thinking", nil); ev != nil {
			t.Fatalf("after reset, iter %d should not trigger, got %+v", i, ev)
		}
	}
}

// TestStuckDetectorCooldown 同一 kind 冷却内不重复触发。
func TestStuckDetectorCooldown(t *testing.T) {
	cfg := DefaultStuckDetectorConfig()
	cfg.Cooldown = 50 * time.Millisecond
	d := NewStuckDetector(cfg)
	convID := "conv-cooldown"
	content := "same output"
	// 触发第 1 次。
	for i := 0; i < 3; i++ {
		d.ObserveAssistantOutput(convID, content, nil)
	}
	// 冷却内继续触发 → 应被抑制。
	suppressed := d.ObserveAssistantOutput(convID, content, nil)
	if suppressed != nil {
		t.Fatalf("cooldown should suppress, got %+v", suppressed)
	}
	// 等冷却过期。
	time.Sleep(60 * time.Millisecond)
	// 再次触发（repeat 已累积，第 4 次 >= 3 应触发）。
	ev := d.ObserveAssistantOutput(convID, content, nil)
	if ev == nil || ev.Kind != "same-output-repeat" {
		t.Fatalf("after cooldown should trigger again, got %+v", ev)
	}
}

// TestStuckDetectorReset 清零会话状态。
func TestStuckDetectorReset(t *testing.T) {
	d := NewStuckDetector(DefaultStuckDetectorConfig())
	convID := "conv-reset"
	content := "output"
	// 触发。
	for i := 0; i < 3; i++ {
		d.ObserveAssistantOutput(convID, content, nil)
	}
	// Reset 后重新开始。
	d.Reset(convID)
	if ev := d.ObserveAssistantOutput(convID, content, nil); ev != nil {
		t.Fatalf("after reset, 1st should not trigger, got %+v", ev)
	}
}

// TestStuckDetectorNormalization 归一化剥离时间戳/进度/行号。
func TestStuckDetectorNormalization(t *testing.T) {
	out1 := "12:34:56 [###] 50% 1: port 80 open"
	out2 := "12:35:57 [###] 60% 2: port 80 open"
	n1 := normalizeStuckOutput(out1)
	n2 := normalizeStuckOutput(out2)
	if n1 != n2 {
		t.Fatalf("normalized outputs should match:\n%q\n%q", n1, n2)
	}
	if n1 == "" {
		t.Fatal("normalized should not be empty")
	}
}

// TestStuckDetectorNilSafe nil detector 所有方法 no-op。
func TestStuckDetectorNilSafe(t *testing.T) {
	var d *StuckDetector
	if ev := d.ObserveAssistantOutput("conv", "x", nil); ev != nil {
		t.Fatal("nil detector should return nil")
	}
	if ev := d.ObserveToolError("conv", "tool", "err"); ev != nil {
		t.Fatal("nil detector should return nil")
	}
	d.Reset("conv")
}

// TestStuckDetectorAdapterMaterialized 适配器从 Assistant 消息提取 content+toolCalls。
func TestStuckDetectorAdapterMaterialized(t *testing.T) {
	d := NewStuckDetector(DefaultStuckDetectorConfig())
	a := newEinoStuckDetectorAdapter(d, "conv-adapt")
	if a == nil {
		t.Fatal("adapter should not be nil")
	}
	call := schema.ToolCall{Function: schema.FunctionCall{Name: "execute", Arguments: `{"command":"ls"}`}}
	// 4 次相同工具调用 → revision-loop；内容每次不同避免 same-output-repeat。
	for i := 0; i < 3; i++ {
		msg := schema.AssistantMessage("done iter "+string(rune('A'+i)), []schema.ToolCall{call})
		if ev := a.ObserveMaterialized(msg); ev != nil {
			t.Fatalf("iter %d should not trigger, got %+v", i, ev)
		}
	}
	msg := schema.AssistantMessage("done iter D", []schema.ToolCall{call})
	ev := a.ObserveMaterialized(msg)
	if ev == nil || ev.Kind != "revision-loop" {
		t.Fatalf("4th should trigger revision-loop, got %+v", ev)
	}
}

// TestStuckDetectorAdapterNonAssistant 非助手消息 no-op。
func TestStuckDetectorAdapterNonAssistant(t *testing.T) {
	d := NewStuckDetector(DefaultStuckDetectorConfig())
	a := newEinoStuckDetectorAdapter(d, "conv-adapt-2")
	msg := schema.ToolMessage("result", "call-1")
	if ev := a.ObserveMaterialized(msg); ev != nil {
		t.Fatalf("tool message should not trigger, got %+v", ev)
	}
}

// TestStuckDetectorAdapterNilDetector nil detector → nil adapter。
func TestStuckDetectorAdapterNilDetector(t *testing.T) {
	a := newEinoStuckDetectorAdapter(nil, "conv")
	if a != nil {
		t.Fatal("nil detector should return nil adapter")
	}
}

// TestPublishStuckEventNoop 不 panic（securityevents board 未注入时 no-op）。
func TestPublishStuckEventNoop(t *testing.T) {
	ev := &StuckEvent{
		ConversationID: "conv-publish",
		Kind:           "monologue",
		Count:          6,
		Threshold:      6,
		Reason:         "monologue:6",
	}
	PublishStuckEvent(ev)
	PublishStuckEvent(nil)
}
