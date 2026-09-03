package microagent

import (
	"fmt"
	"sync"
)

// Registry microagent 注册表——三层 bundle（global→user→repo）覆盖语义。
// 移植自 openhands/memory/memory.py:42-85 Memory 持有的 repo_microagents/knowledge_microagents。
// 三层加载顺序：global（产品内置）→ user（~/.cyberstrike/microagents）→ repo（workspace/.cyberstrike/microagents），
// 后加载者覆盖同名（microagent.py 的 user/repo 覆盖 global 语义）。
type Registry struct {
	mu        sync.RWMutex
	repo      map[string]*Microagent
	knowledge map[string]*Microagent
	// disabled 禁用名单（运行时过滤，移植自 AgentConfig.disabled_microagents）。
	disabled map[string]struct{}
	// perConversationSeen 按会话记录已注入的 knowledge microagent 名，避免跨轮重复注入。
	// 移植自 openhands/memory/conversation_memory.py:711-757 _filter_agents_in_microagent_obs。
	perConversationSeen map[string]map[string]struct{}
	seenMu              sync.Mutex
}

// NewRegistry 构造空注册表。调用方随后用 LoadGlobal/LoadUser/LoadRepo 分层加载。
func NewRegistry() *Registry {
	return &Registry{
		repo:                make(map[string]*Microagent),
		knowledge:           make(map[string]*Microagent),
		disabled:            make(map[string]struct{}),
		perConversationSeen: make(map[string]map[string]struct{}),
	}
}

// SetDisabled 设置禁用名单。nil/空清空。移植自 AgentConfig.disabled_microagents。
func (r *Registry) SetDisabled(names []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.disabled = make(map[string]struct{}, len(names))
	for _, n := range names {
		if n != "" {
			r.disabled[n] = struct{}{}
		}
	}
}

// LoadLayer 加载一层目录并合并（后加载覆盖同名）。
// layer 为目录路径；返回本层加载错误（单个文件失败不中断，聚合返回）。
func (r *Registry) LoadLayer(layer string) error {
	repo, knowledge, err := LoadFromDir(layer)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, v := range repo {
		r.repo[k] = v
	}
	for k, v := range knowledge {
		r.knowledge[k] = v
	}
	return nil
}

// RepoContent 返回所有 always-on repo microagent 拼接内容。
// 移植自 openhands/memory/memory.py:155-162 _on_workspace_context_recall 拼接 repo_instructions。
// 过滤 disabled。顺序按 Name 排序保证幂等。
func (r *Registry) RepoContent() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var names []string
	for n := range r.repo {
		if r.isDisabled(n) {
			continue
		}
		names = append(names, n)
	}
	// 简单排序保证幂等（无依赖 sort 包，保持 leaf）。
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	var b []byte
	for _, n := range names {
		ma := r.repo[n]
		if ma == nil || ma.Content == "" {
			continue
		}
		b = append(b, '\n')
		b = append(b, ma.Content...)
		b = append(b, '\n')
	}
	return string(b)
}

// Retrieve 按用户消息检索命中的 knowledge microagent。
// 移植自 openhands/memory/memory.py:243-270 _find_microagent_knowledge。
// conversationID 为空时不做"已注入去重"（每次都返回）。
// 返回的 Knowledge 列表已过滤 disabled 与（若 conversationID 非空）本轮/历史已注入。
func (r *Registry) Retrieve(conversationID, message string) []Knowledge {
	if message == "" {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	var hits []Knowledge
	var names []string
	for n := range r.knowledge {
		names = append(names, n)
	}
	// 排序保证幂等。
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	for _, n := range names {
		ma := r.knowledge[n]
		if ma == nil || r.isDisabled(n) {
			continue
		}
		trigger := ma.MatchTrigger(message)
		if trigger == "" {
			continue
		}
		if conversationID != "" && r.alreadySeen(conversationID, n) {
			continue
		}
		hits = append(hits, Knowledge{Name: n, Trigger: trigger, Content: ma.Content})
		if conversationID != "" {
			r.markSeen(conversationID, n)
		}
	}
	return hits
}

// ResetSeen 清空某会话的"已注入"记录（如会话重置/重放）。
func (r *Registry) ResetSeen(conversationID string) {
	r.seenMu.Lock()
	defer r.seenMu.Unlock()
	delete(r.perConversationSeen, conversationID)
}

func (r *Registry) isDisabled(name string) bool {
	_, ok := r.disabled[name]
	return ok
}

func (r *Registry) alreadySeen(conversationID, name string) bool {
	r.seenMu.Lock()
	defer r.seenMu.Unlock()
	m, ok := r.perConversationSeen[conversationID]
	if !ok {
		return false
	}
	_, seen := m[name]
	return seen
}

func (r *Registry) markSeen(conversationID, name string) {
	r.seenMu.Lock()
	defer r.seenMu.Unlock()
	m, ok := r.perConversationSeen[conversationID]
	if !ok {
		m = make(map[string]struct{})
		r.perConversationSeen[conversationID] = m
	}
	m[name] = struct{}{}
}

// Stats 返回已加载的 repo/knowledge 数量（供启动日志与测试）。
func (r *Registry) Stats() (repoCount, knowledgeCount int) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.repo), len(r.knowledge)
}

// Has 是否已加载某名字（任意层）。
func (r *Registry) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if _, ok := r.repo[name]; ok {
		return true
	}
	_, ok := r.knowledge[name]
	return ok
}

// String 便于日志。
func (r *Registry) String() string {
	repo, knowledge := r.Stats()
	return fmt.Sprintf("microagent.Registry{repo=%d, knowledge=%d}", repo, knowledge)
}
