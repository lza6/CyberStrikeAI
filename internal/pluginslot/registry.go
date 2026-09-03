package pluginslot

import (
	"strings"
	"sync"
)

// registry 全局插件注册表。key = "slot:name"（移植自 agent-orchestrator makeKey）。
// init 期各插件包 Register，运行期 app.go Get/List 只读。
var (
	registryMu sync.RWMutex
	registry   = make(map[string]entry)
)

// makeKey 生成注册 key。移植自 agent-orchestrator plugin-registry.ts:28-30。
func makeKey(slot Slot, name string) string {
	return string(slot) + ":" + strings.TrimSpace(name)
}

// Register 注册插件。重复注册覆盖（移植自 plugin-registry.ts:363-368）。
// detect 可为 nil（视为恒可用）。
func Register(slot Slot, name string, factory Factory, detect DetectFunc) {
	if factory == nil {
		return
	}
	m := Manifest{
		Name:        strings.TrimSpace(name),
		Slot:        slot,
		Description: "", // 由 RegisterWithManifest 填充
		Version:     "0.0.1",
		DisplayName: strings.TrimSpace(name),
	}
	registerEntry(m, factory, detect)
}

// RegisterWithManifest 注册带完整 manifest 的插件（推荐，供展示名/版本）。
func RegisterWithManifest(m Manifest, factory Factory, detect DetectFunc) {
	if factory == nil {
		return
	}
	registerEntry(m, factory, detect)
}

func registerEntry(m Manifest, factory Factory, detect DetectFunc) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[makeKey(m.Slot, m.Name)] = entry{manifest: m, factory: factory, detect: detect}
}

// Get 按槽位+名取插件实例（移植自 plugin-registry.ts:370-373）。
// cfg 透传给 factory。未注册返回 nil。
func Get(slot Slot, name string, cfg map[string]interface{}) interface{} {
	registryMu.RLock()
	e, ok := registry[makeKey(slot, name)]
	registryMu.RUnlock()
	if !ok {
		return nil
	}
	return e.factory(cfg)
}

// List 按槽位列出所有已注册插件 manifest（移植自 plugin-registry.ts:375-383）。
func List(slot Slot) []Manifest {
	registryMu.RLock()
	defer registryMu.RUnlock()
	prefix := string(slot) + ":"
	out := make([]Manifest, 0)
	for k, e := range registry {
		if strings.HasPrefix(k, prefix) {
			out = append(out, e.manifest)
		}
	}
	return out
}

// DetectAvailable 按槽位返回 detect()=true 的插件名（移植自 loadBuiltins 的"只加载可用的"语义）。
// detect=nil 视为恒可用。
func DetectAvailable(slot Slot) []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	prefix := string(slot) + ":"
	out := make([]string, 0)
	for k, e := range registry {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		if e.detect == nil || e.detect() {
			out = append(out, e.manifest.Name)
		}
	}
	return out
}

// Reset 测试辅助：清空注册表。仅供 _test.go 调用，不导出到生产路径。
func Reset() {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = make(map[string]entry)
}

// Count 返回已注册插件总数（测试用）。
func Count() int {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return len(registry)
}
