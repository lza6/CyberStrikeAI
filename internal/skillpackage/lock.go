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
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// walkSkillMDs 递归遍历 skillsDir，收集每个 SKILL.md 文件的信息。
// 返回 slice 的元素顺序与 WalkDir 的字典序一致（按相对路径排序）。
// 遇到的每个 SKILL.md 都会计算其内容 SHA256。
// 若 skillsDir 不存在或不是目录，返回空 slice + nil。
//
// 设计：用 filepath.WalkDir 替代原先的 os.ReadDir 顶层扫描，
// 以支持子目录垂直包（skills/security/sub/SKILL.md）。
// 顶层 skills/<name>/SKILL.md 与子目录 skills/<grp>/<name>/SKILL.md
// 均会被扫到，保持向后兼容。
//
// name 字段使用 SKILL.md 相对 skillsDir 的目录路径（如 "pentesterflow/recon"），
// 而非仅直接父目录名（"recon"），以避免不同垂直包下同名子目录冲突。
// 顶层 skill 的 name 仍为单段目录名（如 "active-directory-attack"），向后兼容。
type walkedSkill struct {
	relPath string // 相对 skillsDir 的 SKILL.md 路径（ToSlash）
	name    string // skill 包名：SKILL.md 所在目录相对 skillsDir 的路径（ToSlash）
	hash    string // SKILL.md 内容 SHA256
}

func walkSkillMDs(skillsDir string) ([]walkedSkill, error) {
	var out []walkedSkill
	if skillsDir == "" {
		return out, nil
	}
	info, err := os.Stat(skillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, fmt.Errorf("读取 skills 目录失败: %w", err)
	}
	if !info.IsDir() {
		return out, nil
	}
	err = filepath.WalkDir(skillsDir, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			// 单个 entry 读不动时跳过，不中断整体扫描
			return nil
		}
		if d.IsDir() {
			// 跳过隐藏目录（.eino / .git 等）
			if strings.HasPrefix(d.Name(), ".") && d.Name() != "." {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "SKILL.md" {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		rel, e := filepath.Rel(skillsDir, path)
		if e != nil {
			return nil
		}
		// name = SKILL.md 所在目录相对 skillsDir 的路径（ToSlash）
		// 顶层 skill → "active-directory-attack"；嵌套 → "pentesterflow/recon"
		name := filepath.ToSlash(filepath.Dir(rel))
		sum := sha256.Sum256(data)
		out = append(out, walkedSkill{
			relPath: filepath.ToSlash(rel),
			name:    name,
			hash:    hex.EncodeToString(sum[:]),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("读取 skills 目录失败: %w", err)
	}
	return out, nil
}

// SkillLockEntry 单个 skill 的锁条目。
type SkillLockEntry struct {
	SkillPath    string `json:"skillPath"`    // 相对 skillsDir 的 SKILL.md 路径
	Name         string `json:"name"`         // skill 包名（SKILL.md 所在目录名）
	ComputedHash string `json:"computedHash"` // SKILL.md 内容 SHA256
	Source       string `json:"source"`       // 锁来源标记（local）
	SourceType   string `json:"sourceType"`   // 来源类型（directory）
	LockedAt     string `json:"lockedAt"`     // 生成时间 RFC3339
}

// SkillLock 锁文件结构。
type SkillLock struct {
	Version     int              `json:"version"`
	GeneratedAt string           `json:"generatedAt"`
	Skills      []SkillLockEntry `json:"skills"`
}

// GenerateLock 递归遍历 skillsDir 下每个 SKILL.md，计算内容哈希生成锁。
// 支持顶层 skills/<name>/SKILL.md 与子目录垂直包 skills/<grp>/<name>/SKILL.md。
// 不存在或空目录返回空锁（无 skill）+ nil。
func GenerateLock(skillsDir string) (*SkillLock, error) {
	lock := &SkillLock{Version: 1, GeneratedAt: time.Now().UTC().Format(time.RFC3339)}
	walked, err := walkSkillMDs(skillsDir)
	if err != nil {
		return nil, err
	}
	if len(walked) == 0 {
		return lock, nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	lock.Skills = make([]SkillLockEntry, 0, len(walked))
	for _, w := range walked {
		lock.Skills = append(lock.Skills, SkillLockEntry{
			SkillPath:    w.relPath,
			Name:         w.name,
			ComputedHash: w.hash,
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

	// 当前状态（递归扫描，支持子目录垂直包）
	current := map[string]string{} // name -> hash
	walked, werr := walkSkillMDs(skillsDir)
	if werr == nil {
		for _, w := range walked {
			current[w.name] = w.hash
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
