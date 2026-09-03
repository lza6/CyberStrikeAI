// cmd/skill-evals 实跑 skill 触发路由质量评测：Tier 1 结构 + Tier 2 碰撞。
package main

import (
	"fmt"
	"os"

	"cyberstrike-ai/internal/skillpackage/evals"
)

func main() {
	skillsDir := "skills"
	violations, err := evals.ValidateStructure(skillsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Tier 1 扫描失败: %v\n", err)
		os.Exit(2)
	}
	collisions, err := evals.DetectTriggerCollisions(skillsDir, 0.7)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Tier 2 扫描失败: %v\n", err)
		os.Exit(2)
	}
	fmt.Print(evals.FormatReport(violations, collisions))
}
