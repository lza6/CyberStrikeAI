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
