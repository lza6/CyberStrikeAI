package main

import (
	"fmt"
	"os"
	"path/filepath"

	"cyberstrike-ai/internal/skillpackage"
)

func main() {
	skillsDir := "skills"
	lockPath := "skills-lock.json"
	abs, _ := filepath.Abs(skillsDir)
	lock, err := skillpackage.GenerateLock(abs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "生成失败: %v\n", err)
		os.Exit(1)
	}
	if err := skillpackage.WriteLock(lock, lockPath); err != nil {
		fmt.Fprintf(os.Stderr, "写入失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("已生成 %s（%d skill）\n", lockPath, len(lock.Skills))
	for _, s := range lock.Skills {
		fmt.Printf("  - %s  %s\n", s.Name, s.ComputedHash[:12])
	}
	// 立即 Verify，应 0 违规
	tampered, missing, unlocked, err := skillpackage.VerifyLock(abs, lockPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "校验失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Verify: tampered=%d missing=%d unlocked=%d\n", len(tampered), len(missing), len(unlocked))
}
