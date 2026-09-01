// Package skillpackage 提供 skill 供应链锁：扫描 skills/ 目录下每个含 SKILL.md 的包，
// 对 SKILL.md 内容做 SHA256，生成 skills-lock.json。启动时若锁存在则 Verify，
// 检测新增/删除/篡改三类违规，违规只 Warn 不阻断启动（生产兼容优先）。
//
// 设计移植自 caveman 项目 skills-lock.json（source/sourceType/skillPath/computedHash）。
package skillpackage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SkillLockEntry 单个 skill 的锁条目。
type SkillLockEntry struct {
	SkillPath    string `json:"skillPath"`    // 相对 skillsDir 的 SKILL.md 路径
	Name         string `json:"name"`         // skill 包名（目录名）
	ComputedHash string `json:"computedHash"` // SKILL.md 内容 SHA256
	Source       string `json:"source"`       // 锁来源标记（local）
	SourceType   string `json:"sourceType"`   // 来源类型（directory）
	LockedAt     string `json:"lockedAt"`     // 生成时间 RFC3339
}

// SkillLock 锁文件结构。
type SkillLock struct {
	Version      int              `json:"version"`
	GeneratedAt  string           `json:"generatedAt"`
	Skills       []SkillLockEntry `json:"skills"`
}

// GenerateLock 遍历 skillsDir 下每个含 SKILL.md 的目录，计算内容哈希生成锁。
// 不存在或空目录返回空锁（无 skill）+ nil。
func GenerateLock(skillsDir string) (*SkillLock, error) {
	lock := &SkillLock{Version: 1, GeneratedAt: time.Now().UTC().Format(time.RFC3339)}
	if skillsDir == "" {
		return lock, nil
	}
	info, err := os.Stat(skillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return lock, nil
		}
		return nil, fmt.Errorf("读取 skills 目录失败: %w", err)
	}
	if !info.IsDir() {
		return lock, nil
	}
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return nil, fmt.Errorf("读取 skills 目录失败: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		skillFile := filepath.Join(skillsDir, e.Name(), "SKILL.md")
		data, err := os.ReadFile(skillFile)
		if err != nil {
			// 目录存在但无 SKILL.md：跳过（可能是 README/参考材料）
			continue
		}
		sum := sha256.Sum256(data)
		lock.Skills = append(lock.Skills, SkillLockEntry{
			SkillPath:    filepath.ToSlash(filepath.Join(e.Name(), "SKILL.md")),
			Name:         e.Name(),
			ComputedHash: hex.EncodeToString(sum[:]),
			Source:       "local",
			SourceType:   "directory",
			LockedAt:     now,
		})
	}
	sort.Slice(lock.Skills, func(i, j int) bool {
		return lock.Skills[i].Name < lock.Skills[j].Name
	})
	return lock, nil
}

// WriteLock 把锁序列化写到 lockPath（2 空格缩进，末尾换行）。
func WriteLock(lock *SkillLock, lockPath string) error {
	b, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(lockPath, append(b, '\n'), 0644)
}

// VerifyLock 比对锁与当前 skills/ 状态，返回三类违规清单：
// tampered（哈希变化）、missing（锁有但目录无）、unlocked（目录有但锁无）。
// 锁文件不存在时返回空清单 + nil（视为无锁可校验）。
func VerifyLock(skillsDir, lockPath string) (tampered, missing, unlocked []string, err error) {
	data, err := os.ReadFile(lockPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil, nil
		}
		return nil, nil, nil, fmt.Errorf("读取锁文件失败: %w", err)
	}
	var lock SkillLock
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil, nil, nil, fmt.Errorf("解析锁文件失败: %w", err)
	}

	// 当前状态
	current := map[string]string{} // name -> hash
	if skillsDir != "" {
		if entries, derr := os.ReadDir(skillsDir); derr == nil {
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				skillFile := filepath.Join(skillsDir, e.Name(), "SKILL.md")
				if b, rerr := os.ReadFile(skillFile); rerr == nil {
					sum := sha256.Sum256(b)
					current[e.Name()] = hex.EncodeToString(sum[:])
				}
			}
		}
	}

	lockedNames := map[string]bool{}
	for _, e := range lock.Skills {
		lockedNames[e.Name] = true
		got, exists := current[e.Name]
		if !exists {
			missing = append(missing, e.Name)
			continue
		}
		if got != e.ComputedHash {
			tampered = append(tampered, e.Name)
		}
	}
	for name := range current {
		if !lockedNames[name] {
			unlocked = append(unlocked, name)
		}
	}
	sort.Strings(tampered)
	sort.Strings(missing)
	sort.Strings(unlocked)
	return
}

// FormatViolations 把违规清单格式化为可读字符串（给日志用）。
func FormatViolations(tampered, missing, unlocked []string) string {
	var parts []string
	if len(tampered) > 0 {
		parts = append(parts, fmt.Sprintf("篡改(%d): %s", len(tampered), strings.Join(tampered, ", ")))
	}
	if len(missing) > 0 {
		parts = append(parts, fmt.Sprintf("缺失(%d): %s", len(missing), strings.Join(missing, ", ")))
	}
	if len(unlocked) > 0 {
		parts = append(parts, fmt.Sprintf("未锁定(%d): %s", len(unlocked), strings.Join(unlocked, ", ")))
	}
	return strings.Join(parts, " | ")
}
