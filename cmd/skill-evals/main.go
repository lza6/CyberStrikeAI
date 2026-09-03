// cmd/skill-evals 实跑 skill 触发路由质量评测：
// Tier 1 结构 + Tier 2 碰撞 + Tier 3 离线路由行为（真实 LLM 评测需付费红线内另行执行）。
package main

import (
	"flag"
	"fmt"
	"os"

	"cyberstrike-ai/internal/skillpackage/evals"
)

// 默认 Tier 3 用例集：对齐本仓库 skills/ 实际目录的代表性查询 → skill。
// 这些用例是「确定性回归锚」——skill description 措辞改动导致触发场景脱节时在此报警。
var defaultRouteCases = []evals.RouteCase{
	{Query: "对目标做被动侦察和攻击面测绘 whoamass fofa shodan", ExpectedSkill: "attack-surface-recon"},
	{Query: "web 应用漏洞攻击 sql注入 xss ssrf 命令注入", ExpectedSkill: "web-attack-methods"},
	{Query: "后渗透提权与凭据破解 反弹shell 横向移动", ExpectedSkill: "post-exploitation"},
	{Query: "零日漏洞自主发现 变体分析 fuzzing 补丁间隙", ExpectedSkill: "zero-day-discovery"},
	{Query: "redteam 隐蔽作战 opsec 流量混淆 反取证", ExpectedSkill: "redteam-opsec"},
}

func main() {
	skillsDir := flag.String("skills", "skills", "skills 目录")
	tier3 := flag.Bool("tier3", true, "运行 Tier 3 离线路由评测")
	flag.Parse()

	violations, err := evals.ValidateStructure(*skillsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Tier 1 扫描失败: %v\n", err)
		os.Exit(2)
	}
	collisions, err := evals.DetectTriggerCollisions(*skillsDir, 0.7)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Tier 2 扫描失败: %v\n", err)
		os.Exit(2)
	}
	fmt.Print(evals.FormatReport(violations, collisions))

	if *tier3 {
		res, err := evals.EvaluateRouteBehavior(*skillsDir, defaultRouteCases)
		if err != nil {
			// skills 目录不完整时 Tier 3 降级为警告（不阻塞 Tier1/2 结论）
			fmt.Fprintf(os.Stderr, "Tier 3 离线路由评测跳过: %v\n", err)
		} else {
			fmt.Print(evals.FormatRouteReport(res))
		}
	}
}
