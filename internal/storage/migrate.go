// Package storage 提供统一 home 目录与遗留数据迁移工具。
//
// 默认数据目录仍为项目根 data/（保持向后兼容）。运维可选配置 storage.home_dir
// 指向一个集中位置（如 ~/.cyberstrikeai），启动时由 app.go 调用 MigrateLegacyData
// 将散落的 data/ 内容迁移到统一 home，已存在的目标文件自动跳过（幂等可重试）。
package storage

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// HomeEnv 覆盖默认 ~/.cyberstrikeai 的环境变量名（与 config.storage.home_dir 等价，env 优先级更低）。
const HomeEnv = "CYBERSTRIKEAI_HOME"

const defaultHomeName = ".cyberstrikeai"

// HomeDir 返回统一 home 目录路径：优先 $CYBERSTRIKEAI_HOME，其次 $HOME/.cyberstrikeai
// （Windows 为 %USERPROFILE%\.cyberstrikeai）；均不可得时返回空串。
func HomeDir() string {
	if env := strings.TrimSpace(os.Getenv(HomeEnv)); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, defaultHomeName)
}

// EnsureHome 创建 home 目录（含 0700 权限）；home 为空时返回错误。
func EnsureHome(home string) error {
	home = strings.TrimSpace(home)
	if home == "" {
		return errors.New("storage home is empty")
	}
	return os.MkdirAll(home, 0700)
}

// MigrateLegacyData 将 legacyDir 下的所有内容迁移到 homeDir，已存在的目标文件自动跳过。
//
// 设计目标（移植自 autopentest-go migrate.go）：
//   - 幂等：目标已存在时跳过，不覆盖；可安全重试
//   - SQLite 友好：检测 *-wal/*-shm sidecar，先迁 sidecar 再迁主库，避免主库作为完成标记时挂错 sidecar
//   - legacyDir 不存在时返回 nil（无迁移需求）
//   - legacyDir 与 homeDir 相同或互为父目录时跳过，防止误把目标当源迁
//
// 迁移失败收集多错误一并返回（errors.Join），不因单文件失败中断整体。
func MigrateLegacyData(legacyDir, homeDir string) error {
	legacyDir = strings.TrimSpace(legacyDir)
	homeDir = strings.TrimSpace(homeDir)
	if legacyDir == "" || homeDir == "" {
		return nil
	}
	legacyDir = filepath.Clean(legacyDir)
	homeDir = filepath.Clean(homeDir)
	if sameFilesystemPath(legacyDir, homeDir) {
		return nil
	}
	// 源不存在视为无需迁移
	if _, err := os.Lstat(legacyDir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(homeDir, 0700); err != nil {
		return err
	}
	return mergePath(legacyDir, homeDir)
}

// mergePath 递归把 source 合并进 destination（目录则按条目逐个 merge，文件则 move-if-missing）。
func mergePath(source, destination string) error {
	sourceInfo, err := os.Lstat(source)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	destinationInfo, err := os.Lstat(destination)
	if os.IsNotExist(err) {
		return moveIfDestinationMissing(source, destination)
	}
	if err != nil {
		return err
	}
	if sourceInfo.IsDir() && destinationInfo.IsDir() {
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		var errs []error
		for _, entry := range entries {
			if err := mergePath(
				filepath.Join(source, entry.Name()),
				filepath.Join(destination, entry.Name()),
			); err != nil {
				errs = append(errs, err)
			}
		}
		if len(errs) == 0 {
			// 全部迁移完成且目标目录仍为空目录时尝试删除源空目录
			_ = os.Remove(source)
		}
		return errors.Join(errs...)
	}
	// 文件 vs 文件：相同则删除源；不同则冲突（保留目标，跳过源）
	equal, err := equalPaths(source, destination)
	if err != nil {
		// 目标无法读取比较时，保守跳过（不覆盖）
		return nil
	}
	if equal {
		return os.RemoveAll(source)
	}
	// 文件名相同但内容不同：保留目标不覆盖，直接跳过源（不报错）
	return nil
}

// moveIfDestinationMissing 在目标不存在时把 source 移到 destination；跨设备时回退 copy+remove。
func moveIfDestinationMissing(source, destination string) error {
	if _, err := os.Lstat(source); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if _, err := os.Lstat(destination); err == nil {
		return nil // 目标已存在跳过
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	if err := os.Rename(source, destination); err == nil {
		return nil
	}
	// 跨设备 rename 失败，回退 copy + remove
	if err := copyPath(source, destination); err != nil {
		return err
	}
	return os.RemoveAll(source)
}

// sameFilesystemPath 判断两个路径是否指向同一位置（绝对路径 Clean 后比较）。
func sameFilesystemPath(a, b string) bool {
	absA, errA := filepath.Abs(strings.TrimSpace(a))
	absB, errB := filepath.Abs(strings.TrimSpace(b))
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return filepath.Clean(absA) == filepath.Clean(absB)
}

// equalPaths 判断两个普通文件内容是否相同（用于 merge 时决定是否可安全覆盖）。
func equalPaths(a, b string) (bool, error) {
	aInfo, err := os.Lstat(a)
	if err != nil {
		return false, err
	}
	bInfo, err := os.Lstat(b)
	if err != nil {
		return false, err
	}
	if !aInfo.Mode().IsRegular() || !bInfo.Mode().IsRegular() {
		return false, nil
	}
	if aInfo.Size() != bInfo.Size() {
		return false, nil
	}
	aData, err := os.ReadFile(a)
	if err != nil {
		return false, err
	}
	bData, err := os.ReadFile(b)
	if err != nil {
		return false, err
	}
	return string(aData) == string(bData), nil
}

// copyPath 递归复制文件/目录（用于跨设备 rename 回退）。
func copyPath(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(source)
		if err != nil {
			return err
		}
		return os.Symlink(target, destination)
	}
	if !info.IsDir() {
		return copyFile(source, destination, info.Mode().Perm())
	}
	if err := os.MkdirAll(destination, info.Mode().Perm()); err != nil {
		return err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := copyPath(filepath.Join(source, entry.Name()), filepath.Join(destination, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

// copyFile 复制单个普通文件，保留权限位。
func copyFile(source, destination string, mode os.FileMode) error {
	src, err := os.Open(source)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	copied := false
	defer func() {
		_ = dst.Close()
		if !copied {
			_ = os.Remove(destination)
		}
	}()
	if _, err := io.Copy(dst, src); err != nil {
		return err
	}
	if err := dst.Sync(); err != nil {
		return err
	}
	copied = true
	return nil
}
