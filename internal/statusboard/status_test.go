package statusboard_test

import (
	"testing"
	"time"

	"cyberstrike-ai/internal/statusboard"
)

// 移植自参考项目 pkg/contract/status_test.go，固定时间源保证确定性。
var statusNow = time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)

func session(activity statusboard.ActivityState) statusboard.SessionFacts {
	return statusboard.SessionFacts{
		Activity:       activity,
		LastActivityAt: statusNow,
		HasSignal:      true,
	}
}

// TestDeriveStatusPrecedence 验证派生优先级表：
// terminated > active > exited > waiting/blocked > PR 状态 > no_signal > idle。
func TestDeriveStatusPrecedence(t *testing.T) {
	tests := []struct {
		name    string
		session statusboard.SessionFacts
		prs     []statusboard.PRFacts
		want    statusboard.SessionStatus
	}{
		{"terminated", statusboard.SessionFacts{IsTerminated: true}, nil, statusboard.StatusTerminated},
		{"terminated merged", statusboard.SessionFacts{IsTerminated: true}, []statusboard.PRFacts{{Merged: true}}, statusboard.StatusMerged},
		{"active before PR", session(statusboard.ActivityActive), []statusboard.PRFacts{{CI: statusboard.CIFailing}}, statusboard.StatusWorking},
		{"exited before PR", session(statusboard.ActivityExited), []statusboard.PRFacts{{Mergeability: statusboard.MergeMergeable}}, statusboard.StatusExited},
		{"waiting before PR", session(statusboard.ActivityWaitingInput), []statusboard.PRFacts{{CI: statusboard.CIFailing}}, statusboard.StatusNeedsInput},
		{"blocked before PR", session(statusboard.ActivityBlocked), []statusboard.PRFacts{{CI: statusboard.CIFailing}}, statusboard.StatusNeedsInput},
		{"PR before idle", session(statusboard.ActivityIdle), []statusboard.PRFacts{{CI: statusboard.CIFailing}}, statusboard.StatusCIFailed},
		{"idle", session(statusboard.ActivityIdle), nil, statusboard.StatusIdle},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := statusboard.DeriveStatus(tt.session, tt.prs, statusNow, 90*time.Second)
			if got != tt.want {
				t.Fatalf("DeriveStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestDeriveStatusNoSignalRules 验证 no_signal 派生规则（SignalExpected + !HasSignal + 静默超 grace）。
func TestDeriveStatusNoSignalRules(t *testing.T) {
	const grace = 90 * time.Second
	silent := statusboard.SessionFacts{
		Activity:       statusboard.ActivityIdle,
		LastActivityAt: statusNow.Add(-2 * grace),
	}

	tests := []struct {
		name           string
		session        statusboard.SessionFacts
		signalExpected bool
		now            time.Time
		want           statusboard.SessionStatus
	}{
		{"past grace", silent, true, statusNow, statusboard.StatusNoSignal},
		{"signal not expected", silent, false, statusNow, statusboard.StatusIdle},
		{"signal received", withSignal(silent), true, statusNow, statusboard.StatusIdle},
		{"at boundary", silent, true, silent.LastActivityAt.Add(grace), statusboard.StatusIdle},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			facts := tt.session
			facts.SignalExpected = tt.signalExpected
			got := statusboard.DeriveStatus(facts, nil, tt.now, grace)
			if got != tt.want {
				t.Fatalf("DeriveStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestDeriveSCMStatusPipelineAndWorstWins 验证 PR 管线状态 + worst-severity wins。
func TestDeriveSCMStatusPipelineAndWorstWins(t *testing.T) {
	tests := []struct {
		name string
		prs  []statusboard.PRFacts
		want statusboard.SessionStatus
	}{
		{"closed ignored", []statusboard.PRFacts{{Closed: true}}, ""},
		{"merged", []statusboard.PRFacts{{Merged: true}}, statusboard.StatusMerged},
		{"open", []statusboard.PRFacts{{}}, statusboard.StatusPROpen},
		{"review pending", []statusboard.PRFacts{{Review: statusboard.ReviewRequired}}, statusboard.StatusReviewPending},
		{"review pending with provider merge blocker", []statusboard.PRFacts{{Review: statusboard.ReviewRequired, Mergeability: statusboard.MergeBlocked}}, statusboard.StatusReviewPending},
		{"approved", []statusboard.PRFacts{{Review: statusboard.ReviewApproved}}, statusboard.StatusApproved},
		{"mergeable", []statusboard.PRFacts{{Mergeability: statusboard.MergeMergeable}}, statusboard.StatusMergeable},
		{"merge blocked", []statusboard.PRFacts{{Mergeability: statusboard.MergeBlocked}}, statusboard.StatusPROpen},
		{"merge blocked with approved review", []statusboard.PRFacts{{Mergeability: statusboard.MergeBlocked, Review: statusboard.ReviewApproved}}, statusboard.StatusPROpen},
		{"changes requested", []statusboard.PRFacts{{Review: statusboard.ReviewChangesRequest}}, statusboard.StatusChangesRequested},
		{"review comments", []statusboard.PRFacts{{ReviewComments: true}}, statusboard.StatusChangesRequested},
		{"draft", []statusboard.PRFacts{{Draft: true}}, statusboard.StatusDraft},
		{"CI failed", []statusboard.PRFacts{{CI: statusboard.CIFailing}}, statusboard.StatusCIFailed},
		{
			"worst wins",
			[]statusboard.PRFacts{
				{URL: "a", SourceBranch: "a", TargetBranch: "main", Mergeability: statusboard.MergeMergeable},
				{URL: "b", SourceBranch: "b", TargetBranch: "main", CI: statusboard.CIFailing},
			},
			statusboard.StatusCIFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := statusboard.DeriveSCMStatus(tt.prs); got != tt.want {
				t.Fatalf("DeriveSCMStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestStackRules 验证 stack 派生：父 PR 不阻塞，子 PR 阻塞；父合并后子解除阻塞。
func TestStackRules(t *testing.T) {
	parent := statusboard.PRFacts{
		URL:          "parent",
		SourceBranch: "feature",
		TargetBranch: "main",
		Mergeability: statusboard.MergeMergeable,
	}
	child := statusboard.PRFacts{
		URL:          "child",
		SourceBranch: "feature/child",
		TargetBranch: "feature",
	}

	positions := statusboard.BuildStacks([]statusboard.PRFacts{parent, child})
	if positions["parent"].Blocked || !positions["parent"].BottomOfStack {
		t.Fatalf("parent position = %+v", positions["parent"])
	}
	if !positions["child"].Blocked || positions["child"].BottomOfStack {
		t.Fatalf("child position = %+v", positions["child"])
	}

	// 被阻塞的子 PR 的 readiness 信号被抑制（父未合并时子的 mergeable 不冒泡）。
	if got := statusboard.DeriveSCMStatus([]statusboard.PRFacts{parent, child}); got != statusboard.StatusMergeable {
		t.Fatalf("blocked child readiness was not suppressed: got %q", got)
	}
	// 被阻塞的子 PR 的 CI 失败是 actionable，仍冒泡。
	child.CI = statusboard.CIFailing
	if got := statusboard.DeriveSCMStatus([]statusboard.PRFacts{parent, child}); got != statusboard.StatusCIFailed {
		t.Fatalf("blocked child problem was suppressed: got %q", got)
	}

	// 父合并后子解除阻塞。
	parent.Merged = true
	positions = statusboard.BuildStacks([]statusboard.PRFacts{parent, child})
	if positions["child"].Blocked {
		t.Fatal("merged parent still blocks child")
	}
}

// TestColumnFor 验证 SessionStatus → Kanban 列映射。
func TestColumnFor(t *testing.T) {
	tests := []struct {
		status statusboard.SessionStatus
		want   statusboard.KanbanColumn
	}{
		{statusboard.StatusWorking, statusboard.ColumnWorking},
		{statusboard.StatusIdle, statusboard.ColumnWorking},
		{statusboard.StatusNeedsInput, statusboard.ColumnNeedsYou},
		{statusboard.StatusCIFailed, statusboard.ColumnNeedsYou},
		{statusboard.StatusChangesRequested, statusboard.ColumnNeedsYou},
		{statusboard.StatusNoSignal, statusboard.ColumnNeedsYou},
		{statusboard.StatusExited, statusboard.ColumnNeedsYou},
		{statusboard.StatusPROpen, statusboard.ColumnInReview},
		{statusboard.StatusDraft, statusboard.ColumnInReview},
		{statusboard.StatusReviewPending, statusboard.ColumnInReview},
		{statusboard.StatusApproved, statusboard.ColumnReadyToMerge},
		{statusboard.StatusMergeable, statusboard.ColumnReadyToMerge},
		{statusboard.StatusMerged, statusboard.ColumnArchived},
		{statusboard.StatusTerminated, statusboard.ColumnArchived},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			if got := statusboard.ColumnFor(tt.status); got != tt.want {
				t.Fatalf("ColumnFor(%q) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

func withSignal(facts statusboard.SessionFacts) statusboard.SessionFacts {
	facts.HasSignal = true
	return facts
}
