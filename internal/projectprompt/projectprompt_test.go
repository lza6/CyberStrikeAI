package projectprompt

import (
	"strings"
	"testing"
)

// TestFactRecordingIncrementalRhythmMarkdown_Base 确认基础节奏段存在且含核心动作工具。
func TestFactRecordingIncrementalRhythmMarkdown_Base(t *testing.T) {
	got := FactRecordingIncrementalRhythmMarkdown(false, false)
	if got == "" {
		t.Fatal("空输出")
	}
	for _, want := range []string{"边渗透边记录", "upsert_project_fact", "record_vulnerability"} {
		if !strings.Contains(got, want) {
			t.Fatalf("缺少 %q\n输出: %s", want, got)
		}
	}
	// 未委派不应含委派后缀
	if strings.Contains(got, "协调者及时写入") {
		t.Fatal("coordinator=false 不应含委派后缀")
	}
}

// TestFactRecordingIncrementalRhythmMarkdown_Coordinator 确认 coordinator 后缀追加。
func TestFactRecordingIncrementalRhythmMarkdown_Coordinator(t *testing.T) {
	got := FactRecordingIncrementalRhythmMarkdown(true, false)
	if !strings.Contains(got, "协调者及时写入") {
		t.Fatal("coordinator=true 应含委派后缀")
	}
	if strings.Contains(got, "待落库") {
		t.Fatal("subAgent=false 不应含待落库后缀")
	}
}

// TestFactRecordingIncrementalRhythmMarkdown_SubAgent 确认 subAgent 后缀追加。
func TestFactRecordingIncrementalRhythmMarkdown_SubAgent(t *testing.T) {
	got := FactRecordingIncrementalRhythmMarkdown(false, true)
	if !strings.Contains(got, "待落库") {
		t.Fatal("subAgent=true 应含待落库后缀")
	}
	if strings.Contains(got, "协调者及时写入") {
		t.Fatal("coordinator=false 不应含委派后缀")
	}
}

// TestFactRecordingIncrementalRhythmMarkdown_Both 确认两者叠加且顺序正确。
func TestFactRecordingIncrementalRhythmMarkdown_Both(t *testing.T) {
	got := FactRecordingIncrementalRhythmMarkdown(true, true)
	iCoord := strings.Index(got, "协调者及时写入")
	iSub := strings.Index(got, "待落库")
	if iCoord < 0 || iSub < 0 || iCoord > iSub {
		t.Fatalf("后缀顺序异常: coord=%d sub=%d", iCoord, iSub)
	}
}

// TestFactRecordingBlackboardSection_Markdown 确认黑板段落完整（含事实/漏洞/证据三要素）。
func TestFactRecordingBlackboardSection_Markdown(t *testing.T) {
	for _, delegate := range []bool{true, false} {
		got := FactRecordingBlackboardSectionMarkdown(delegate)
		if got == "" {
			t.Fatalf("delegate=%v 空输出", delegate)
		}
		for _, want := range []string{"fact", "漏洞", "证据"} {
			if !strings.Contains(got, want) {
				t.Fatalf("delegate=%v 缺 %q", delegate, want)
			}
		}
	}
}

// TestFactRecordingSubAgentSection 确认子代理段包含结构化待落库条目提示。
func TestFactRecordingSubAgentSection(t *testing.T) {
	got := FactRecordingSubAgentSection()
	if got == "" {
		t.Fatal("空输出")
	}
	for _, want := range []string{"待落库", "fact_key"} {
		if !strings.Contains(got, want) {
			t.Fatalf("缺 %q", want)
		}
	}
}

// TestShellExecExecuteGuidanceSection 确认 shell 分工指导存在且关键约束齐全。
func TestShellExecExecuteGuidanceSection(t *testing.T) {
	got := ShellExecExecuteGuidanceSection()
	for _, want := range []string{"exec", "execute", "禁止", "工作目录"} {
		if !strings.Contains(got, want) {
			t.Fatalf("缺 %q\n输出: %s", want, got)
		}
	}
}

// TestShellExecExecuteGuidanceReconSuffix 确认侦察后缀提及子域枚举工具。
func TestShellExecExecuteGuidanceReconSuffix(t *testing.T) {
	got := ShellExecExecuteGuidanceReconSuffix()
	if !strings.Contains(got, "subfinder") {
		t.Fatalf("侦察后缀应提 subfinder: %s", got)
	}
}
