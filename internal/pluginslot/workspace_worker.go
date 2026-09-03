package pluginslot

// SlotWorkspace 工厂注册（AO-3c）。
//
// 扩展 slot.go:30 预留的 SlotWorkspace 槽位：真实注册 directory + git-worktree
// 两个 Factory，DetectAvailable 探测 git 可用性。通过 init() 自动注册（与
// desktop_notifier/webhook_notifier 的注册范式一致）。
//
// 使用：
//
//	inst := pluginslot.Get(pluginslot.SlotWorkspace, "git-worktree", map[string]interface{}{
//	    "managed_root": "tmp/workspace/workers",
//	})
//	if ws, ok := inst.(pluginslot.Workspace); ok { ws.Create(cfg) }
func init() {
	RegisterWithManifest(Manifest{
		Name:        "directory",
		Slot:        SlotWorkspace,
		Description: "纯目录隔离（零 git 依赖，与 project/workspace.go 机制一致）",
		Version:     "1.0.0",
		DisplayName: "Directory Isolation",
	}, directoryWorkspaceFactory, nil) // 无外部依赖，恒可用

	RegisterWithManifest(Manifest{
		Name:        "git-worktree",
		Slot:        SlotWorkspace,
		Description: "git worktree + 独立 branch 隔离（os/exec git，需系统装 git）",
		Version:     "1.0.0",
		DisplayName: "Git Worktree Isolation",
	}, gitWorkspaceFactory, func() bool { return gitAvailable() })
}

// workspaceCfg 从 Factory cfg map 提取 managed_root（缺省 tmp/workspace/workers）。
func workspaceCfg(cfg map[string]interface{}) string {
	if cfg == nil {
		return ""
	}
	if mr, ok := cfg["managed_root"].(string); ok && mr != "" {
		return mr
	}
	return ""
}

// RegisterWorkspaceFactories 幂等注册 directory + git-worktree 两个 Factory。
// init() 已自动注册；但同包测试的 Reset() 会清空注册表，此函数供恢复
// （幂等：重复 Register 覆盖同 key，无副作用）。
func RegisterWorkspaceFactories() {
	RegisterWithManifest(Manifest{
		Name:        "directory",
		Slot:        SlotWorkspace,
		Description: "纯目录隔离（零 git 依赖，与 project/workspace.go 机制一致）",
		Version:     "1.0.0",
		DisplayName: "Directory Isolation",
	}, directoryWorkspaceFactory, nil)

	RegisterWithManifest(Manifest{
		Name:        "git-worktree",
		Slot:        SlotWorkspace,
		Description: "git worktree + 独立 branch 隔离（os/exec git，需系统装 git）",
		Version:     "1.0.0",
		DisplayName: "Git Worktree Isolation",
	}, gitWorkspaceFactory, func() bool { return gitAvailable() })
}

// directoryWorkspaceFactory 构造 DirectoryWorkspace。
func directoryWorkspaceFactory(cfg map[string]interface{}) interface{} {
	return NewDirectoryWorkspace(workspaceCfg(cfg))
}

// gitWorkspaceFactory 构造 GitWorkspace。cfg 可选 git_bin 覆盖。
func gitWorkspaceFactory(cfg map[string]interface{}) interface{} {
	gitBin := ""
	if cfg != nil {
		if gb, ok := cfg["git_bin"].(string); ok {
			gitBin = gb
		}
	}
	return NewGitWorkspace(workspaceCfg(cfg), gitBin)
}
