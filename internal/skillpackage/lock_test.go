package skillpackage

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSkillFile(t *testing.T, dir, name, content string) {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateLockAndVerify(t *testing.T) {
	dir := t.TempDir()
	writeSkillFile(t, dir, "alpha", "# Alpha\nfirst")
	writeSkillFile(t, dir, "beta", "# Beta\nsecond")
	// 无 SKILL.md 的目录应被跳过
	if err := os.MkdirAll(filepath.Join(dir, "no-skill-md"), 0755); err != nil {
		t.Fatal(err)
	}

	lock, err := GenerateLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(lock.Skills) != 2 {
		t.Fatalf("期望 2 skill 锁，实际 %d", len(lock.Skills))
	}
	lockPath := filepath.Join(dir, "skills-lock.json")
	if err := WriteLock(lock, lockPath); err != nil {
		t.Fatal(err)
	}

	// 正常状态：0 违规
	tampered, missing, unlocked, err := VerifyLock(dir, lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(tampered) != 0 || len(missing) != 0 || len(unlocked) != 0 {
		t.Fatalf("正常状态应 0 违规，got tampered=%v missing=%v unlocked=%v", tampered, missing, unlocked)
	}

	// 篡改 alpha
	writeSkillFile(t, dir, "alpha", "# Alpha\nchanged")
	tampered, _, _, _ = VerifyLock(dir, lockPath)
	if len(tampered) != 1 || tampered[0] != "alpha" {
		t.Fatalf("篡改应检出 alpha，got %v", tampered)
	}

	// 还原 + 新增 unlocked
	writeSkillFile(t, dir, "alpha", "# Alpha\nfirst")
	writeSkillFile(t, dir, "gamma", "# Gamma\nnew")
	_, _, unlocked, _ = VerifyLock(dir, lockPath)
	if len(unlocked) != 1 || unlocked[0] != "gamma" {
		t.Fatalf("未锁定应检出 gamma，got %v", unlocked)
	}

	// 删除 beta
	os.RemoveAll(filepath.Join(dir, "beta"))
	_, missing, _, _ = VerifyLock(dir, lockPath)
	if len(missing) != 1 || missing[0] != "beta" {
		t.Fatalf("缺失应检出 beta，got %v", missing)
	}
}

func TestVerifyLockNoLockFile(t *testing.T) {
	dir := t.TempDir()
	writeSkillFile(t, dir, "x", "x")
	tampered, missing, unlocked, err := VerifyLock(dir, filepath.Join(dir, "nope.json"))
	if err != nil {
		t.Fatalf("锁不存在应返回 nil err，got %v", err)
	}
	if len(tampered) != 0 || len(missing) != 0 || len(unlocked) != 0 {
		t.Fatalf("无锁时应 0 违规")
	}
}

func TestGenerateLockEmptyDir(t *testing.T) {
	dir := t.TempDir()
	lock, err := GenerateLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(lock.Skills) != 0 {
		t.Fatalf("空目录应 0 skill，got %d", len(lock.Skills))
	}
}

func TestFormatViolations(t *testing.T) {
	got := FormatViolations([]string{"a"}, []string{"b"}, []string{"c", "d"})
	if got == "" {
		t.Fatal("应非空")
	}
}

// TestGenerateLockRecursiveSubdir K0a：验证递归扫描子目录生成锁。
// skills/security/alpha/SKILL.md 与 skills/office/beta/SKILL.md 都应进入锁。
func TestGenerateLockRecursiveSubdir(t *testing.T) {
	dir := t.TempDir()
	// 嵌套子目录
	nestedAlpha := filepath.Join(dir, "security", "alpha")
	if err := os.MkdirAll(nestedAlpha, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nestedAlpha, "SKILL.md"), []byte("# Alpha nested\n"), 0644); err != nil {
		t.Fatal(err)
	}
	nestedBeta := filepath.Join(dir, "office", "beta")
	if err := os.MkdirAll(nestedBeta, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nestedBeta, "SKILL.md"), []byte("# Beta nested\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// 顶层 skill（向后兼容）
	writeSkillFile(t, dir, "gamma", "# Gamma top\n")

	lock, err := GenerateLock(dir)
	if err != nil {
		t.Fatalf("递归生成锁失败: %v", err)
	}
	if len(lock.Skills) != 3 {
		t.Fatalf("期望 3 skill 锁（2 嵌套 + 1 顶层），实际 %d: %+v", len(lock.Skills), lock.Skills)
	}
	// 验证包名（相对路径形态）
	names := map[string]bool{}
	for _, s := range lock.Skills {
		names[s.Name] = true
	}
	if !names["security/alpha"] {
		t.Fatalf("锁应含 security/alpha，got %v", names)
	}
	if !names["office/beta"] {
		t.Fatalf("锁应含 office/beta，got %v", names)
	}
	if !names["gamma"] {
		t.Fatalf("锁应含 gamma（顶层），got %v", names)
	}
}

// TestVerifyLockRecursiveSubdir K0a：验证递归扫描下 VerifyLock 检出篡改/缺失/未锁定。
func TestVerifyLockRecursiveSubdir(t *testing.T) {
	dir := t.TempDir()
	nestedAlpha := filepath.Join(dir, "security", "alpha")
	if err := os.MkdirAll(nestedAlpha, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nestedAlpha, "SKILL.md"), []byte("# Alpha first\n"), 0644); err != nil {
		t.Fatal(err)
	}
	writeSkillFile(t, dir, "beta", "# Beta\n")

	lock, err := GenerateLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(lock.Skills) != 2 {
		t.Fatalf("基线锁应有 2 skill，got %d", len(lock.Skills))
	}
	lockPath := filepath.Join(dir, "skills-lock.json")
	if err := WriteLock(lock, lockPath); err != nil {
		t.Fatal(err)
	}

	// 正常状态：0 违规
	tampered, missing, unlocked, err := VerifyLock(dir, lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(tampered) != 0 || len(missing) != 0 || len(unlocked) != 0 {
		t.Fatalf("正常状态应 0 违规，got tampered=%v missing=%v unlocked=%v", tampered, missing, unlocked)
	}

	// 篡改嵌套 skill
	if err := os.WriteFile(filepath.Join(nestedAlpha, "SKILL.md"), []byte("# Alpha changed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tampered, _, _, _ = VerifyLock(dir, lockPath)
	if len(tampered) != 1 || tampered[0] != "security/alpha" {
		t.Fatalf("篡改应检出 security/alpha，got %v", tampered)
	}

	// 还原 + 新增 unlocked
	if err := os.WriteFile(filepath.Join(nestedAlpha, "SKILL.md"), []byte("# Alpha first\n"), 0644); err != nil {
		t.Fatal(err)
	}
	newNested := filepath.Join(dir, "security", "gamma")
	if err := os.MkdirAll(newNested, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newNested, "SKILL.md"), []byte("# Gamma new\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, _, unlocked, _ = VerifyLock(dir, lockPath)
	if len(unlocked) != 1 || unlocked[0] != "security/gamma" {
		t.Fatalf("未锁定应检出 security/gamma，got %v", unlocked)
	}

	// 删除顶层 beta
	if err := os.RemoveAll(filepath.Join(dir, "beta")); err != nil {
		t.Fatal(err)
	}
	_, missing, _, _ = VerifyLock(dir, lockPath)
	if len(missing) != 1 || missing[0] != "beta" {
		t.Fatalf("缺失应检出 beta，got %v", missing)
	}
}
