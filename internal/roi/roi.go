// Package roi 计算 campaign（战役）的投资回报（ROI）页脚，置于每份战役
// 报告的末尾：预期赏金价值 vs LLM 花费，并给出红/黄/绿判定灯。
//
// 其意义在于让研究员诚实面对"这次扫描是否值得运行"。一场烧掉 $40
// token 却只产出 $50 投机性发现的战役，充其量是黄色——研究员应该在
// 提交报告前就知道这一点，而不是等到分诊把大多数发现归为仅供参考后
// 才意识到。
//
// 移植自 Pentest-Swarm-AI 的 internal/agent/report/roi。
package roi

import (
	"fmt"

	"cyberstrike-ai/internal/bounty"
)

// Verdict 是报告页脚渲染的红黄绿判定灯。
type Verdict string

const (
	VerdictGreen  Verdict = "green"  // 倍率 > 10×
	VerdictYellow Verdict = "yellow" // 倍率 2× – 10×
	VerdictRed    Verdict = "red"    // 倍率 < 2×
)

// Result 是一场战役的 ROI 计算结果。
type Result struct {
	SpendUSD      float64
	BountyLowUSD  int
	BountyHighUSD int
	RatioLow      float64 // BountyLow / Spend
	RatioHigh     float64 // BountyHigh / Spend
	Verdict       Verdict
}

// Calculate 对每个发现运行赏金估值器，汇总区间，并对照花费评级。
//
// Verdict 基于赏金区间的*低端*判定——宁可把边际战役标黄，也不在乐观
// 情绪下渲染成绿。
func Calculate(spendUSD float64, findings []bounty.Finding, stats *bounty.ProgramStats) Result {
	low, high := bounty.Total(findings, stats)
	r := Result{
		SpendUSD:      spendUSD,
		BountyLowUSD:  low,
		BountyHighUSD: high,
	}
	if spendUSD > 0 {
		r.RatioLow = float64(low) / spendUSD
		r.RatioHigh = float64(high) / spendUSD
	}
	switch {
	case r.RatioLow > 10:
		r.Verdict = VerdictGreen
	case r.RatioLow >= 2:
		r.Verdict = VerdictYellow
	default:
		r.Verdict = VerdictRed
	}
	return r
}

// Footer 是报告的 markdown 块——置于每份战役报告的末尾，让研究员无需
// 滚过 50 条发现就能看到判定结果。
func (r Result) Footer() string {
	icon := map[Verdict]string{
		VerdictGreen:  "🟢",
		VerdictYellow: "🟡",
		VerdictRed:    "🔴",
	}[r.Verdict]
	return fmt.Sprintf(
		"**Campaign ROI:** %s estimated bounty $%d–$%d  ·  LLM spend $%.2f  ·  ratio %.1f×–%.1f×",
		icon, r.BountyLowUSD, r.BountyHighUSD, r.SpendUSD, r.RatioLow, r.RatioHigh,
	)
}
