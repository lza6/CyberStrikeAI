package evals

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, name), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name, "SKILL.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

const validSkill = `---
name: 测试技能
description: 这是一个测试用的技能描述
---
# 正文

测试内容。
`

func TestValidateStructure(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "good", validSkill)
	writeSkill(t, dir, "bad-nofm", "没有 frontmatter 的内容")
	writeSkill(t, dir, "bad-nodesc", "---\nname: x\n---\n正文")

	violations, err := ValidateStructure(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 2 {
		t.Fatalf("期望 2 违规，got %d: %+v", len(violations), violations)
	}
	// bad-nofm: 缺 frontmatter；bad-nodesc: 缺 description
	found := map[string]string{}
	for _, v := range violations {
		found[v.Skill] = v.Issue
	}
	if !strings.Contains(found["bad-nofm"], "frontmatter") {
		t.Fatalf("bad-nofm 违规不符: %+v", violations)
	}
	if !strings.Contains(found["bad-nodesc"], "description") {
		t.Fatalf("bad-nodesc 违规不符: %+v", violations)
	}
	// good 无违规
	if _, ok := found["good"]; ok {
		t.Fatal("good 不应有违规")
	}
}

func TestDetectTriggerCollisions(t *testing.T) {
	dir := t.TempDir()
	// 两个高度相似 description
	writeSkill(t, dir, "web-scan-a", "---\nname: a\ndescription: web 漏洞扫描器，扫描 web 应用漏洞\n---\n正文")
	writeSkill(t, dir, "web-scan-b", "---\nname: b\ndescription: web 应用漏洞扫描器，扫描 web 漏洞\n---\n正文")
	// 一个完全不相关的
	writeSkill(t, dir, "wifi-crack", "---\nname: c\ndescription: 无线网络 WPA 握手破解工具\n---\n正文")

	collisions, err := DetectTriggerCollisions(dir, 0.6)
	if err != nil {
		t.Fatal(err)
	}
	if len(collisions) != 1 {
		t.Fatalf("期望 1 碰撞，got %d: %+v", len(collisions), collisions)
	}
	// 碰撞对应是 a↔b
	pair := collisions[0]
	if !((pair.SkillA == "web-scan-a" && pair.SkillB == "web-scan-b") || (pair.SkillA == "web-scan-b" && pair.SkillB == "web-scan-a")) {
		t.Fatalf("碰撞对应是 web-scan-a↔b: %+v", pair)
	}
	if pair.Similarity <= 0.6 {
		t.Fatalf("相似度应 >0.6: %v", pair.Similarity)
	}
}

func TestCosineSimilarity(t *testing.T) {
	if cosineSimilarity("完全相同的描述文本", "完全相同的描述文本") != 1 {
		t.Fatal("相同文本相似度应为 1")
	}
	if cosineSimilarity("", "任何东西") != 0 {
		t.Fatal("空文本相似度应为 0")
	}
	if cosineSimilarity("无线破解 wifi", "无线上网路由器") >= 1 {
		t.Fatal("不同文本相似度应 <1")
	}
}

func TestFormatReport(t *testing.T) {
	r := FormatReport(
		[]StructureViolation{{Skill: "x", Issue: "缺 description", Line: 2}},
		[]CollisionViolation{{SkillA: "a", SkillB: "b", Similarity: 0.8}},
	)
	if !strings.Contains(r, "Tier 1") || !strings.Contains(r, "Tier 2") {
		t.Fatal("报告应含 Tier 1/Tier 2")
	}
}
