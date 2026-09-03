// Package bounty 根据漏洞严重程度（severity）与可选的目标项目赏金统计，
// 估算单条发现（finding）的美元赏金区间。
//
// 设计来源：移植自 Pentest-Swarm-AI 的 internal/agent/report/bounty 包，
// 去掉了对 pipeline 包的依赖（CyberStrikeAI 没有 pipeline 包），改为在
// 本包内自定义 Severity 常量与最小化的 Finding 结构，保证零外部依赖。
//
// 用途：
//   - 最终报告页脚（ROI 计算）：把预期赏金与 LLM 花费做对比；
//   - 单条发现的提交视图：让研究员在提交前判断草稿是否值得花时间。
//
// 估值刻意保守——少承诺可以接受，多承诺会侵蚀信任。公开市场（publicMarket）
// 回退值取自 HackerOne 年度 hacker-powered-security 报告的中位数区间，
// 并向下取整以避免研究员锚定高端。
package bounty

// Severity 严重程度字符串常量，与主项目（internal/database/vulnerability.go
// 的 Vulnerability.Severity 字段值、internal/blackboard/board.go 的
// Finding.Severity 字段值）保持一致：critical / high / medium / low / info。
type Severity string

const (
	// SeverityCritical 关键。
	SeverityCritical Severity = "critical"
	// SeverityHigh 高。
	SeverityHigh Severity = "high"
	// SeverityMedium 中。
	SeverityMedium Severity = "medium"
	// SeverityLow 低。
	SeverityLow Severity = "low"
	// SeverityInformational 信息级。
	SeverityInformational Severity = "info"
)

// Finding 是赏金估算所需的最小化发现结构。只保留 Severity 字段——
// 估值逻辑不依赖其他信息（标题、证据、时间等），因此本包不引用
// database 包或 blackboard 包，保持零外部依赖，便于单测与复用。
type Finding struct {
	// Severity 严重程度，取值为上述 Severity 常量字符串。
	Severity string
}

// ProgramStats 是调用方从赏金平台公开主页能抓取到的 per-program 数据。
// 所有字段可选——零值会回退到保守的公开市场（publicMarket）表。
type ProgramStats struct {
	// Slug 标识项目，用于日志与报告页脚。
	Slug string

	// AveragePerSeverity 非空时覆盖默认表。键为 Severity 字符串值，
	// 值为美元（该项目该严重程度 bucket 的历史均值）。
	AveragePerSeverity map[string]int

	// TopPerSeverity 是该项目该严重程度的已发布最高赏金。
	// 把它作为区间的“高端”，让研究员看到上限而不至于被均值虚高误导。
	TopPerSeverity map[string]int
}

// Range 是单条发现的估算赏金区间。
type Range struct {
	LowUSD  int
	HighUSD int
	// Source 描述数值来源——便于 tooltip 显示
	// （"from program stats" vs "industry average"）。
	Source string
}

// publicMarket 是保守的公开市场回退值。数值取自 HackerOne 已发布的中位数
// 赏金表，并向下取整（rounded DOWN），避免对研究员过度承诺。
var publicMarket = map[string]Range{
	string(SeverityCritical):      {LowUSD: 1500, HighUSD: 10000, Source: "industry-average"},
	string(SeverityHigh):          {LowUSD: 500, HighUSD: 3000, Source: "industry-average"},
	string(SeverityMedium):        {LowUSD: 150, HighUSD: 800, Source: "industry-average"},
	string(SeverityLow):           {LowUSD: 50, HighUSD: 200, Source: "industry-average"},
	string(SeverityInformational): {LowUSD: 0, HighUSD: 50, Source: "industry-average"},
}

// Estimate 返回单条发现的估算美元区间。若 programStats 中有该发现
// 严重程度的数据，则以项目数据为准；否则回退到公开市场表。
//
// 推导规则（与源项目语义一致）：
//   - 同时有 avg 与 top：low=avg, high=top；
//   - 只有 top：low=top/2（仍展示有意义的区间跨度）；
//   - 只有 avg：high=avg*5/2（H1 公开项目统计中常见的 top/avg ≈ 2.5× 比例）；
//   - avg 与 top 都没有：回退到 publicMarket；publicMarket 也没有该
//     severity 时返回零值 Range（仅 Source="industry-average"）。
func Estimate(f Finding, stats *ProgramStats) Range {
	if stats != nil {
		avg, hasAvg := stats.AveragePerSeverity[f.Severity]
		top, hasTop := stats.TopPerSeverity[f.Severity]
		if hasAvg || hasTop {
			low := avg
			high := top
			if !hasAvg && hasTop {
				// 只发布了最高赏金——取一半作为低端，保证区间仍有意义。
				low = top / 2
			}
			if !hasTop && hasAvg {
				// 只有均值——按 2.5× 推导高端（H1 公开统计常见比例）。
				high = avg * 5 / 2
			}
			return Range{LowUSD: low, HighUSD: high, Source: "program:" + stats.Slug}
		}
	}
	if r, ok := publicMarket[f.Severity]; ok {
		return r
	}
	// 未知 severity：返回零值 Range，Source 标记为 industry-average
	// 以便调用方在 tooltip 中区分“未知 severity 回退”与“已知 severity 查表”。
	return Range{Source: "industry-average"}
}

// Total 对一组发现累加 Estimate——用于报告页脚的 ROI 计算。
// 返回 (low, high) 美元总额。空 findings 返回 (0, 0)。
func Total(findings []Finding, stats *ProgramStats) (lowUSD, highUSD int) {
	for _, f := range findings {
		r := Estimate(f, stats)
		lowUSD += r.LowUSD
		highUSD += r.HighUSD
	}
	return
}
