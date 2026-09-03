package skillpackage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRecursiveWalkLockAndGate 验证 K0c：lock 与 verbs_gate 递归扫描子目录 SKILL.md。
// 构造临时 skills 目录：
//
//	skills/top/SKILL.md            （顶层 skill，向后兼容）
//	skills/security/sub/SKILL.md  （子目录垂直包）
//	skills/office/test/SKILL.md    （子目录垂直包）
//	skills/no-skill-md/           （无 SKILL.md，应被跳过）
//	skills/.hidden/inner/SKILL.md  （隐藏目录，应被跳过）
//
// 期望：GenerateLock 收集 3 个 skill；VerifyLock 0 违规；
// ScanToolReferences 在子目录 SKILL.md 中也能检出幽灵工具。
func TestRecursiveWalkLockAndGate(t *testing.T) {
	root := t.TempDir()

	mustWriteSkillMD(t, filepath.Join(root, "top"), "top", "# top")
	mustWriteSkillMD(t, filepath.Join(root, "security", "sub"), "sub", "# sub")
	mustWriteSkillMD(t, filepath.Join(root, "office", "test"), "test", "# test")
	// 无 SKILL.md 目录
	if err := os.MkdirAll(filepath.Join(root, "no-skill-md"), 0755); err != nil {
		t.Fatal(err)
	}
	// 隐藏目录（应被跳过）
	mustWriteSkillMD(t, filepath.Join(root, ".hidden", "inner"), "inner", "# inner")

	// 1. GenerateLock 递归收集
	lock, err := GenerateLock(root)
	if err != nil {
		t.Fatal(err)
	}
	gotNames := map[string]bool{}
	for _, s := range lock.Skills {
		gotNames[s.Name] = true
	}
	wantNames := []string{"top", "security/sub", "office/test"}
	if len(lock.Skills) != len(wantNames) {
		t.Fatalf("GenerateLock 期望 %d skill（含子目录），实际 %d: %+v",
			len(wantNames), len(lock.Skills), lock.Skills)
	}
	for _, n := range wantNames {
		if !gotNames[n] {
			t.Fatalf("GenerateLock 未扫到子目录 skill %q，got %v", n, gotNames)
		}
	}
	// 隐藏目录的 skill 不应出现
	if gotNames["inner"] || gotNames[".hidden/inner"] {
		t.Fatalf("隐藏目录 .hidden/inner 不应被扫描")
	}

	// 2. VerifyLock 0 违规（递归扫描后与锁一致）
	lockPath := filepath.Join(root, "skills-lock.json")
	if err := WriteLock(lock, lockPath); err != nil {
		t.Fatal(err)
	}
	tampered, missing, unlocked, err := VerifyLock(root, lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(tampered) != 0 || len(missing) != 0 || len(unlocked) != 0 {
		t.Fatalf("递归 VerifyLock 应 0 违规，got tampered=%v missing=%v unlocked=%v",
			tampered, missing, unlocked)
	}

	// 3. 篡改子目录 skill，VerifyLock 应检出
	mustWriteSkillMD(t, filepath.Join(root, "security", "sub"), "sub", "# sub changed")
	tampered, _, _, _ = VerifyLock(root, lockPath)
	if len(tampered) != 1 || tampered[0] != "security/sub" {
		t.Fatalf("篡改 security/sub 应检出，got %v", tampered)
	}
	// 还原
	mustWriteSkillMD(t, filepath.Join(root, "security", "sub"), "sub", "# sub")

	// 4. 新增 unlocked 子目录 skill
	mustWriteSkillMD(t, filepath.Join(root, "office", "newone"), "newone", "# newone")
	_, _, unlocked, _ = VerifyLock(root, lockPath)
	if len(unlocked) != 1 || unlocked[0] != "office/newone" {
		t.Fatalf("未锁定应检出 office/newone，got %v", unlocked)
	}

	// 5. 删除子目录 skill，VerifyLock 应 missing
	os.RemoveAll(filepath.Join(root, "office", "test"))
	_, missing, _, _ = VerifyLock(root, lockPath)
	if len(missing) != 1 || missing[0] != "office/test" {
		t.Fatalf("缺失应检出 office/test，got %v", missing)
	}
}

// TestRecursiveScanToolReferences 验证 verbs_gate 递归扫描子目录 SKILL.md 的幽灵工具。
func TestRecursiveScanToolReferences(t *testing.T) {
	root := t.TempDir()
	// 顶层 alpha 引用真实工具 exec + 幽灵 ghost-top
	mustWriteSkillMDContent(t, filepath.Join(root, "alpha"), `---
name: alpha
description: 测试
tools: [exec, ghost-top]
---
# Alpha
用 `+"`exec`"+` 跑命令，也调 `+"`ghost-top`"+`。
`)
	// 子目录 security/sub 引用真实工具 exec + 幽灵 ghost-sub
	mustWriteSkillMDContent(t, filepath.Join(root, "security", "sub"), `---
name: sub
description: 测试子目录
tools: [exec, ghost-sub]
---
# Sub
用 `+"`exec`"+` 和 `+"`ghost-sub`"+`。
`)
	realTools := map[string]bool{"exec": true}
	violations, err := ScanToolReferences(root, realTools)
	if err != nil {
		t.Fatal(err)
	}
	wantGhost := map[string]bool{"ghost-top": false, "ghost-sub": false}
	for _, v := range violations {
		if _, ok := wantGhost[v.Referenced]; ok {
			wantGhost[v.Referenced] = true
		}
	}
	for ghost, found := range wantGhost {
		if !found {
			t.Fatalf("递归扫描未检出幽灵工具 %q（子目录 SKILL.md 也应被扫到），violations=%+v",
				ghost, violations)
		}
	}
}

// TestRecursiveListSkillDirNames 验证 ListSkillDirNames 递归返回子目录 name。
func TestRecursiveListSkillDirNames(t *testing.T) {
	root := t.TempDir()
	mustWriteSkillMD(t, filepath.Join(root, "top"), "top", "# top")
	mustWriteSkillMD(t, filepath.Join(root, "security", "sub"), "sub", "# sub")
	mustWriteSkillMD(t, filepath.Join(root, "office", "test"), "test", "# test")
	// 无 SKILL.md
	os.MkdirAll(filepath.Join(root, "empty"), 0755)

	names, err := ListSkillDirNames(root)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, n := range names {
		got[n] = true
	}
	want := []string{"top", "security/sub", "office/test"}
	if len(names) != len(want) {
		t.Fatalf("ListSkillDirNames 期望 %d 项，实际 %d: %v", len(want), len(names), names)
	}
	for _, w := range want {
		if !got[w] {
			t.Fatalf("ListSkillDirNames 未返回子目录 name %q，got %v", w, got)
		}
	}
}

// TestLockFileBackwardCompat 验证生成的 skills-lock.json 结构向后兼容
// （顶层 skill 的 skillPath 仍为 "<name>/SKILL.md"，name 仍为单段目录名）。
func TestLockFileBackwardCompat(t *testing.T) {
	root := t.TempDir()
	mustWriteSkillMD(t, filepath.Join(root, "alpha"), "alpha", "# alpha")
	mustWriteSkillMD(t, filepath.Join(root, "beta"), "beta", "# beta")
	// 子目录 skill
	mustWriteSkillMD(t, filepath.Join(root, "grp", "gamma"), "gamma", "# gamma")

	lock, err := GenerateLock(root)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(root, "skills-lock.json")
	if err := WriteLock(lock, lockPath); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	var parsed SkillLock
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	// 顶层 skill 的 skillPath 必须是 "<name>/SKILL.md"（向后兼容旧 skills-lock.json）
	for _, s := range parsed.Skills {
		if !strings.HasSuffix(s.SkillPath, "/SKILL.md") {
			t.Fatalf("skillPath 应以 /SKILL.md 结尾，got %q", s.SkillPath)
		}
		switch s.Name {
		case "alpha":
			if s.SkillPath != "alpha/SKILL.md" {
				t.Fatalf("顶层 alpha skillPath 应为 alpha/SKILL.md，got %q", s.SkillPath)
			}
		case "beta":
			if s.SkillPath != "beta/SKILL.md" {
				t.Fatalf("顶层 beta skillPath 应为 beta/SKILL.md，got %q", s.SkillPath)
			}
		case "grp/gamma":
			if s.SkillPath != "grp/gamma/SKILL.md" {
				t.Fatalf("子目录 gamma skillPath 应为 grp/gamma/SKILL.md，got %q", s.SkillPath)
			}
		default:
			t.Fatalf("未预期的 skill name %q", s.Name)
		}
	}
}

// mustWriteSkillMD 在 dir 下创建 SKILL.md，content 为 "# <name>\n<extra>"。
func mustWriteSkillMD(t *testing.T, dir, name, extra string) {
	t.Helper()
	mustWriteSkillMDContent(t, dir, "# "+name+"\n"+extra+"\n")
}

// mustWriteSkillMDContent 在 dir 下创建 SKILL.md，写入任意 content。
func mustWriteSkillMDContent(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
