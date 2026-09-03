package skillpackage

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// SkillDir returns the absolute path to a skill package directory.
func SkillDir(skillsRoot, skillID string) string {
	return filepath.Join(skillsRoot, skillID)
}

// ResolveSKILLPath returns SKILL.md path or error if missing.
func ResolveSKILLPath(skillPath string) (string, error) {
	md := filepath.Join(skillPath, "SKILL.md")
	if st, err := os.Stat(md); err != nil || st.IsDir() {
		return "", fmt.Errorf("missing SKILL.md in %q (Agent Skills standard)", filepath.Base(skillPath))
	}
	return md, nil
}

// SkillsRootFromConfig resolves cfg.SkillsDir relative to the config file directory.
func SkillsRootFromConfig(skillsDir string, configPath string) string {
	if skillsDir == "" {
		skillsDir = "skills"
	}
	configDir := filepath.Dir(configPath)
	if !filepath.IsAbs(skillsDir) {
		skillsDir = filepath.Join(configDir, skillsDir)
	}
	return skillsDir
}

// DirLister lists skill package directory names under SkillsRoot.
type DirLister struct {
	SkillsRoot string
}

// ListSkills returns skill package directory names that contain SKILL.md.
func (d DirLister) ListSkills() ([]string, error) {
	return ListSkillDirNames(d.SkillsRoot)
}

// ListSkillDirNames returns subdirectory names under skillsRoot that contain SKILL.md.
// K0c：改用 filepath.WalkDir 递归扫描，支持子目录垂直包
// （skills/security/alpha/SKILL.md → "security/alpha"）。
// 顶层 skill 目录（skills/beta/SKILL.md → "beta"）保持向后兼容。
// 返回的 name 是 SKILL.md 所在目录相对 skillsRoot 的路径（ToSlash）。
func ListSkillDirNames(skillsRoot string) ([]string, error) {
	if _, err := os.Stat(skillsRoot); os.IsNotExist(err) {
		return nil, nil
	}
	var names []string
	err := filepath.WalkDir(skillsRoot, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			// 单个 entry 读不动时跳过，不中断整体扫描
			return nil
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && d.Name() != "." {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "SKILL.md" {
			return nil
		}
		rel, e := filepath.Rel(skillsRoot, path)
		if e != nil {
			return nil
		}
		// name = SKILL.md 所在目录相对 skillsRoot 的路径
		name := filepath.ToSlash(filepath.Dir(rel))
		if name == "." || name == "" {
			return nil
		}
		names = append(names, name)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read skills directory: %w", err)
	}
	return names, nil
}
