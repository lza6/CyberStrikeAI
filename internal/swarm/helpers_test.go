package swarm

import "os"

// writeFileSync 是 os.WriteFile 的薄封装（避免与 worktree_test 的 writeFile 命名冲突歧义）。
func writeFileSync(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
