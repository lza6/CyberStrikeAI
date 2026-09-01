package handler

import (
	"net/http"

	"cyberstrike-ai/internal/playbooks"

	"github.com/gin-gonic/gin"
)

// PlaybooksHandler 攻击剧本（playbooks）处理器。
//
// 与 RoleHandler 风格一致：构造时注入依赖（已加载的剧本切片），
// 不依赖 database / config，便于在 setupRoutes 中独立构造。
// 剧本在启动时一次性加载到内存，运行时只读，无需锁。
type PlaybooksHandler struct {
	items []playbooks.Playbook
}

// NewPlaybooksHandler 构造 PlaybooksHandler。items 可为 nil/空切片。
func NewPlaybooksHandler(items []playbooks.Playbook) *PlaybooksHandler {
	if items == nil {
		items = []playbooks.Playbook{}
	}
	return &PlaybooksHandler{items: items}
}

// playbookSummary 对外暴露的剧本摘要（不含 FilePath）。
type playbookSummary struct {
	Name        string                    `json:"name"`
	DisplayName string                    `json:"display_name"`
	Description string                    `json:"description"`
	Phases      []playbooks.PlaybookPhase `json:"phases"`
}

// toSummary 把内部 Playbook 转为对外摘要（剥离 FilePath）。
func toSummary(p playbooks.Playbook) playbookSummary {
	phases := p.Phases
	if phases == nil {
		phases = []playbooks.PlaybookPhase{}
	}
	return playbookSummary{
		Name:        p.Name,
		DisplayName: p.DisplayName,
		Description: p.Description,
		Phases:      phases,
	}
}

// ListPlaybooks GET /api/playbooks — 返回所有剧本摘要。
// 响应: { "playbooks": [ {name, display_name, description, phases: [...]}, ... ] }
func (h *PlaybooksHandler) ListPlaybooks(c *gin.Context) {
	out := make([]playbookSummary, 0, len(h.items))
	for _, p := range h.items {
		out = append(out, toSummary(p))
	}
	c.JSON(http.StatusOK, gin.H{
		"playbooks": out,
	})
}

// GetPlaybook GET /api/playbooks/:name — 按 name 查找单个剧本。找不到返回 404。
func (h *PlaybooksHandler) GetPlaybook(c *gin.Context) {
	name := c.Param("name")
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "剧本名称不能为空"})
		return
	}

	for _, p := range h.items {
		if p.Name == name {
			c.JSON(http.StatusOK, gin.H{
				"playbook": toSummary(p),
			})
			return
		}
	}

	c.JSON(http.StatusNotFound, gin.H{"error": "剧本不存在"})
}
