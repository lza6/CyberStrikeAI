// Package statusboard 提供 CDC（变更数据捕获）+ status-derivation 活看板能力。
//
// 迁移自参考项目 agent-orchestrator/backend 的 internal/cdc + pkg/contract 体系。
// 核心设计（与参考项目一致）：
//   - status 永远是读侧投影：从 session/pr 事实用纯函数 DeriveStatus 派生，
//     没有 mutable state 机。CDC 事件只是"失效信号"让客户端重拉读模型。
//   - CDC 靠 SQLite AFTER INSERT/UPDATE 触发器写 change_log（应用层零 emit 代码，
//     原子一致）；poller + broadcaster 只是 live push on top。
//   - 看板列映射由消费侧把 SessionStatus 归类，DeriveStatus 只产单一 status 字符串。
//
// 与参考项目的差异（Go 重写，适配 CyberStrikeAI）：
//   - 参考项目用 sqlc 生成 query 代码；本实现用 database/sql 手写（与主项目范式一致）。
//   - 参考项目用 modernc.org/sqlite；本实现复用主项目 mattn/modernc 双驱动。
//   - 参考项目 CDC 独立 internal/cdc 包；本实现复用 internal/eventstream 的
//     Event/Store/EventStream 体系（已落地未接线，是 CDC 天然载体），新增 SQLiteStore。
//
// 设计为 leaf 包：只依赖标准库 + internal/eventstream（类型，Store 接口），
// 不反向导入 agent/handler/project/multiagent，避免循环依赖。
package statusboard

import "time"

// ActivityState 是 agent 的最新观察状态。移植自 contract/status.go:5-15。
type ActivityState string

// ActivityState 值，由 agent runtime 上报。
const (
	ActivityActive       ActivityState = "active"
	ActivityIdle         ActivityState = "idle"
	ActivityWaitingInput ActivityState = "waiting_input"
	ActivityBlocked      ActivityState = "blocked"
	ActivityExited       ActivityState = "exited"
)

// SessionStatus 是 session 的派生展示状态。移植自 contract/status.go:17-36。
type SessionStatus string

// SessionStatus 值，AO 客户端展示。
const (
	StatusWorking          SessionStatus = "working"
	StatusPROpen           SessionStatus = "pr_open"
	StatusDraft            SessionStatus = "draft"
	StatusCIFailed         SessionStatus = "ci_failed"
	StatusReviewPending    SessionStatus = "review_pending"
	StatusChangesRequested SessionStatus = "changes_requested"
	StatusApproved         SessionStatus = "approved"
	StatusMergeable        SessionStatus = "mergeable"
	StatusMerged           SessionStatus = "merged"
	StatusNeedsInput       SessionStatus = "needs_input"
	StatusExited           SessionStatus = "exited"
	StatusIdle             SessionStatus = "idle"
	StatusTerminated       SessionStatus = "terminated"
	StatusNoSignal         SessionStatus = "no_signal"
)

// SessionFacts 是用于派生 session status 的持久化无关事实。移植自 contract/status.go:38-45。
type SessionFacts struct {
	Activity       ActivityState
	LastActivityAt time.Time
	HasSignal      bool // 是否曾收到 hook 信号
	SignalExpected bool // 该 harness 是否应发信号
	IsTerminated   bool
}

// CIState 是 pull request 的聚合 CI 状态。移植自 contract/status.go:47-56。
type CIState string

// CIState 值，聚合一个 PR 的所有 checks。
const (
	CIUnknown CIState = "unknown"
	CIPending CIState = "pending"
	CIPassing CIState = "passing"
	CIFailing CIState = "failing"
)

// ReviewDecision 是 pull request 上的人工审核聚合裁决。移植自 contract/status.go:58-67。
type ReviewDecision string

// ReviewDecision 值，聚合人工审核状态。
const (
	ReviewNone           ReviewDecision = "none"
	ReviewApproved       ReviewDecision = "approved"
	ReviewChangesRequest ReviewDecision = "changes_requested"
	ReviewRequired       ReviewDecision = "review_required"
)

// Mergeability 描述 pull request 当前能否合并。移植自 contract/status.go:69-79。
type Mergeability string

// Mergeability 值，描述当前 PR 合并状态。
const (
	MergeUnknown     Mergeability = "unknown"
	MergeMergeable   Mergeability = "mergeable"
	MergeConflicting Mergeability = "conflicting"
	MergeBlocked     Mergeability = "blocked"
	MergeUnstable    Mergeability = "unstable"
)

// PRFacts 是用于派生 session 和 stack status 的 pull-request 事实。移植自 contract/status.go:81-93。
type PRFacts struct {
	URL            string
	Draft          bool
	Merged         bool
	Closed         bool
	CI             CIState
	Review         ReviewDecision
	Mergeability   Mergeability
	ReviewComments bool // 是否有未解决 review 评论
	SourceBranch   string
	TargetBranch   string
}

// StackPosition 是 pull request 在其 stack 中的派生位置。移植自 contract/status.go:95-99。
type StackPosition struct {
	Blocked       bool // 是否被上游 PR 阻塞
	BottomOfStack bool // 是否在 stack 底部（无上游阻塞）
}

// KanbanColumn 是看板列。AO Kanban 的四列映射，由消费侧把 SessionStatus 归类。
type KanbanColumn string

// KanbanColumn 值，对应 AO 看板四列 + 归档。
const (
	ColumnWorking      KanbanColumn = "working"        // 正在实现或就绪
	ColumnNeedsYou     KanbanColumn = "needs_you"      // 阻塞/缺输入/CI 失败/待审核
	ColumnInReview     KanbanColumn = "in_review"      // 开放/draft PR 等审核
	ColumnReadyToMerge KanbanColumn = "ready_to_merge" // 已批准/可合并
	ColumnArchived     KanbanColumn = "archived"       // 已合并/已终止
)

// DeriveStatus 从 session 和 pull-request 事实派生展示状态。移植自 contract/status.go:101-132。
//
// 纯函数，无副作用，无外部依赖，可直接表驱动测试。
// now 由调用方传入（非 time.Now()），保证可测与确定性。
// 派生优先级：terminated > active > exited > waiting/blocked > PR 状态 > no_signal > idle。
func DeriveStatus(
	session SessionFacts,
	prs []PRFacts,
	now time.Time,
	noSignalGrace time.Duration,
) SessionStatus {
	if session.IsTerminated {
		if anyMerged(prs) {
			return StatusMerged
		}
		return StatusTerminated
	}

	switch session.Activity {
	case ActivityActive:
		return StatusWorking
	case ActivityExited:
		return StatusExited
	case ActivityWaitingInput, ActivityBlocked:
		return StatusNeedsInput
	}

	if scmStatus := DeriveSCMStatus(prs); scmStatus != "" {
		return scmStatus
	}

	if silentPastGrace(session, now, noSignalGrace) {
		return StatusNoSignal
	}
	return StatusIdle
}

// silentPastGrace 报告一个应上报 hook 活动的 session 是否从未上报且静默超过 grace。
// 移植自 contract/status.go:136-139。
func silentPastGrace(session SessionFacts, now time.Time, noSignalGrace time.Duration) bool {
	return session.SignalExpected && !session.HasSignal &&
		now.Sub(session.LastActivityAt) > noSignalGrace
}

// DeriveSCMStatus 独立于 activity 派生 stack-aware 的 pull-request 状态。移植自 contract/status.go:141-151。
func DeriveSCMStatus(prs []PRFacts) SessionStatus {
	open := openPRs(prs)
	if len(open) > 0 {
		return aggregatePRStatus(open)
	}
	if anyMerged(prs) {
		return StatusMerged
	}
	return ""
}

// BuildStacks 从 open 的 source/target 分支派生 stack 位置。移植自 contract/status.go:153-171。
//
// 某 PR 的 target branch 是另一 open PR 的 source → 该 PR Blocked=true。
func BuildStacks(prs []PRFacts) map[string]StackPosition {
	openSources := make(map[string]bool, len(prs))
	for _, pr := range prs {
		if !pr.Merged && !pr.Closed && pr.SourceBranch != "" {
			openSources[pr.SourceBranch] = true
		}
	}

	positions := make(map[string]StackPosition, len(prs))
	for _, pr := range prs {
		blocked := pr.TargetBranch != "" && openSources[pr.TargetBranch]
		positions[pr.URL] = StackPosition{
			Blocked:       blocked,
			BottomOfStack: !blocked,
		}
	}
	return positions
}

// ColumnFor 把 SessionStatus 归类到看板列。这是 AO Kanban 四列映射的纯函数。
// 移植自参考项目 renderer 的归类逻辑（Kanban 列映射由消费侧做，非 DeriveStatus 职责）。
func ColumnFor(status SessionStatus) KanbanColumn {
	switch status {
	case StatusWorking:
		return ColumnWorking
	case StatusNeedsInput, StatusCIFailed, StatusChangesRequested, StatusNoSignal, StatusExited:
		// StatusExited 归 NeedsYou：worker 退出但产物未被收割/确认前，仍需人工介入
		// （AO 语义：exited session 的 PR 可能待 review，或有未提交工作待处理）。
		// 若产品后续定义"正常退出自动归档"，此处应改 ColumnArchived（审计发现 7 留意）。
		return ColumnNeedsYou
	case StatusPROpen, StatusDraft, StatusReviewPending:
		return ColumnInReview
	case StatusApproved, StatusMergeable:
		return ColumnReadyToMerge
	case StatusMerged, StatusTerminated:
		return ColumnArchived
	case StatusIdle:
		return ColumnWorking // idle 视为就绪，归 Working 列
	default:
		return ColumnWorking
	}
}

func openPRs(prs []PRFacts) []PRFacts {
	open := make([]PRFacts, 0, len(prs))
	for _, pr := range prs {
		if !pr.Merged && !pr.Closed {
			open = append(open, pr)
		}
	}
	return open
}

func anyMerged(prs []PRFacts) bool {
	for _, pr := range prs {
		if pr.Merged {
			return true
		}
	}
	return false
}

// aggregatePRStatus 聚合所有 open PR 的状态，worst-severity wins。移植自 contract/status.go:192-215。
func aggregatePRStatus(open []PRFacts) SessionStatus {
	stacks := BuildStacks(open)
	candidates := make([]SessionStatus, 0, len(open))
	for _, pr := range open {
		status := prPipelineStatus(pr)
		// 被阻塞的子 PR 的非 actionable 信号被跳过（避免 stack 中间 PR 的噪声冒泡）。
		if stacks[pr.URL].Blocked && !isActionableChildSignal(status) {
			continue
		}
		candidates = append(candidates, status)
	}
	if len(candidates) == 0 {
		// 所有 open PR 都被阻塞且无 actionable 信号，退化为取所有 PR 状态。
		for _, pr := range open {
			candidates = append(candidates, prPipelineStatus(pr))
		}
	}

	worst := candidates[0]
	for _, status := range candidates[1:] {
		if statusSeverity(status) < statusSeverity(worst) {
			worst = status
		}
	}
	return worst
}

// isActionableChildSignal 报告被阻塞子 PR 的某状态是否仍需冒泡。移植自 contract/status.go:217-224。
func isActionableChildSignal(status SessionStatus) bool {
	switch status {
	case StatusCIFailed, StatusDraft, StatusChangesRequested:
		return true
	default:
		return false
	}
}

// statusSeverity 状态严重度（越小越严重），worst-severity wins。移植自 contract/status.go:226-245。
func statusSeverity(status SessionStatus) int {
	switch status {
	case StatusCIFailed:
		return 0
	case StatusChangesRequested:
		return 1
	case StatusDraft:
		return 2
	case StatusReviewPending:
		return 3
	case StatusPROpen:
		return 4
	case StatusApproved:
		return 5
	case StatusMergeable:
		return 6
	default:
		return 7
	}
}

// prPipelineStatus 单个 PR 的管线状态。移植自 contract/status.go:247-266。
func prPipelineStatus(pr PRFacts) SessionStatus {
	switch {
	case pr.CI == CIFailing:
		return StatusCIFailed
	case pr.Draft:
		return StatusDraft
	case pr.Review == ReviewChangesRequest || pr.ReviewComments:
		return StatusChangesRequested
	case pr.Mergeability == MergeMergeable:
		return StatusMergeable
	case pr.Review == ReviewRequired:
		return StatusReviewPending
	case pr.Mergeability == MergeBlocked:
		return StatusPROpen
	case pr.Review == ReviewApproved:
		return StatusApproved
	default:
		return StatusPROpen
	}
}
