package robot

import (
	"strings"
	"testing"
)

// TestSplitTextChunks_NormalShort 验证短文本单段返回原文本（去首尾空白）。
func TestSplitTextChunks_NormalShort(t *testing.T) {
	got := splitTextChunks("  hello  ", 100)
	if len(got) != 1 || got[0] != "hello" {
		t.Fatalf("短文本应返回 [\"hello\"], got %#v", got)
	}
}

// TestSplitTextChunks_EmptyOrInvalid 验证空串/非正 maxRunes 返回 nil。
func TestSplitTextChunks_EmptyOrInvalid(t *testing.T) {
	if got := splitTextChunks("   ", 100); got != nil {
		t.Fatalf("纯空白应返回 nil, got %#v", got)
	}
	if got := splitTextChunks("text", 0); got != nil {
		t.Fatalf("maxRunes=0 应返回 nil, got %#v", got)
	}
	if got := splitTextChunks("text", -5); got != nil {
		t.Fatalf("maxRunes<0 应返回 nil, got %#v", got)
	}
}

// TestSplitTextChunks_ExactlyMaxRunes 验证长度恰等于上限时返回单段。
func TestSplitTextChunks_ExactlyMaxRunes(t *testing.T) {
	text := strings.Repeat("a", 10)
	got := splitTextChunks(text, 10)
	if len(got) != 1 || got[0] != text {
		t.Fatalf("恰等于上限应单段, got %#v", got)
	}
}

// TestSplitTextChunks_SplitsOnRuneBoundary 验证按 rune 切分而非字节，多字节字符不被拆断。
func TestSplitTextChunks_SplitsOnRuneBoundary(t *testing.T) {
	// 6 个汉字（每个 3 字节），maxRunes=4 → 拆成 4 + 2
	text := "一二三四五六"
	got := splitTextChunks(text, 4)
	if len(got) != 2 {
		t.Fatalf("应拆成 2 段, got %d: %#v", len(got), got)
	}
	if got[0] != "一二三四" {
		t.Fatalf("首段不符: %q", got[0])
	}
	if got[1] != "五六" {
		t.Fatalf("尾段不符: %q", got[1])
	}
	// 拼接后应等于原文
	if joined := strings.Join(got, ""); joined != text {
		t.Fatalf("拼接后丢失字符: %q", joined)
	}
}

// TestSplitTextChunks_NonUniformChunks 验证非整除时尾段为余数。
func TestSplitTextChunks_NonUniformChunks(t *testing.T) {
	// 7 个 a，maxRunes=3 → 3 + 3 + 1
	got := splitTextChunks("aaaaaaa", 3)
	if len(got) != 3 {
		t.Fatalf("应拆成 3 段, got %d", len(got))
	}
	if got[2] != "a" {
		t.Fatalf("尾段应为 1 字符, got %q", got[2])
	}
}

// TestTrimReply_TrimsWhitespace 验证 trimReply 去除首尾空白。
func TestTrimReply_TrimsWhitespace(t *testing.T) {
	if got := trimReply("  \thello\n\n"); got != "hello" {
		t.Fatalf("trimReply 未正确去空白: %q", got)
	}
	if got := trimReply(""); got != "" {
		t.Fatalf("空串应返回空, got %q", got)
	}
}

// fakeMessageHandler 是 MessageHandler 的测试替身，用于验证返回的回复链路。
type fakeMessageHandler struct {
	replies []struct {
		platform string
		userID   string
		text     string
	}
}

func (f *fakeMessageHandler) HandleMessage(platform, userID, text string) string {
	f.replies = append(f.replies, struct {
		platform string
		userID   string
		text     string
	}{platform, userID, text})
	return "echo:" + text
}

// TestMessageHandler_Contract 验证 MessageHandler 接口的测试替身按契约工作（确保接口可被实现且被调用）。
func TestMessageHandler_Contract(t *testing.T) {
	var h MessageHandler = &fakeMessageHandler{}
	reply := h.HandleMessage("dingtalk", "u1", "你好")
	if reply != "echo:你好" {
		t.Fatalf("reply 不符: %q", reply)
	}
	fh := h.(*fakeMessageHandler)
	if len(fh.replies) != 1 || fh.replies[0].platform != "dingtalk" || fh.replies[0].userID != "u1" || fh.replies[0].text != "你好" {
		t.Fatalf("HandleMessage 未记录调用: %+v", fh.replies)
	}
}
