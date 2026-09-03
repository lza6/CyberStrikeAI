package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"cyberstrike-ai/internal/config"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// githubRelease 上游 releases/latest 响应中用到的字段。
type githubRelease struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
	Body    string `json:"body"`
}

// updateCheckResponse GET /api/update/check 响应。
type updateCheckResponse struct {
	CurrentVersion string `json:"current_version"`
	LatestVersion  string `json:"latest_version"`
	HasUpdate      bool   `json:"has_update"`
	ReleaseURL     string `json:"release_url"`
	ReleaseNotes   string `json:"release_notes"`
	CheckedAt      string `json:"checked_at"`
}

// UpdateHandler 版本更新检测处理器：查 GitHub releases/latest 并与当前配置版本语义比较。
type UpdateHandler struct {
	currentVersion string
	apiBase        string // GitHub API 根；测试注入 httptest server 地址
	httpClient     *http.Client
	// 10 分钟内存缓存，避免每次点击都打 GitHub
	mu             sync.Mutex
	cachedResponse *updateCheckResponse
	cachedAt       time.Time
}

// updateCacheTTL 更新检查缓存时长。
const updateCacheTTL = 10 * time.Minute

// defaultUpdateAPIBase 上游仓库 API（原项目 lza6/CyberStrikeAI）。
const defaultUpdateAPIBase = "https://api.github.com"

// normalizeVersionTag 去掉前导 v/V 与空白，返回纯数字段版本串。
func normalizeVersionTag(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "V")
	return strings.TrimSpace(v)
}

// NewUpdateHandler 构造 UpdateHandler；currentVersion 取 cfg.Version（空则 0.0.0）。
func NewUpdateHandler(cfg *config.Config, logger *zap.Logger) *UpdateHandler {
	v := ""
	if cfg != nil {
		v = normalizeVersionTag(cfg.Version)
	}
	if v == "" {
		v = "0.0.0"
	}
	_ = logger
	return &UpdateHandler{
		currentVersion: v,
		apiBase:        defaultUpdateAPIBase,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
			// 系统代理（HTTP_PROXY 等）常用于访问 GitHub；本地 mock 测试地址走 NO_PROXY 豁免。
			Transport: http.DefaultTransport,
		},
	}
}

// SetAPIBase 供测试注入 mock API 地址。
func (h *UpdateHandler) SetAPIBase(base string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.apiBase = base
	h.cachedResponse = nil
	h.cachedAt = time.Time{}
}

// CheckUpdate GET /api/update/check
func (h *UpdateHandler) CheckUpdate(c *gin.Context) {
	h.mu.Lock()
	cached := h.cachedResponse
	fresh := h.cachedAt.Add(updateCacheTTL).After(time.Now())
	h.mu.Unlock()
	if cached != nil && fresh {
		c.JSON(http.StatusOK, *cached)
		return
	}

	// apiBase 可带子路径（测试注入 mock 前缀）；GitHub 真实地址为裸根。
	url := strings.TrimSuffix(h.apiBase, "/") + "/repos/lza6/CyberStrikeAI/releases/latest"
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, url, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "构造更新请求失败"})
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := h.httpClient.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "无法连接更新服务器"})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		c.JSON(http.StatusBadGateway, gin.H{"error": "无法连接更新服务器"})
		return
	}

	var rel githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "无法连接更新服务器"})
		return
	}

	latest := normalizeVersionTag(rel.TagName)
	out := updateCheckResponse{
		CurrentVersion: h.currentVersion,
		LatestVersion:  latest,
		HasUpdate:      latest != "" && compareVersionStrings(latest, h.currentVersion) > 0,
		ReleaseURL:     rel.HTMLURL,
		ReleaseNotes:   rel.Body,
		CheckedAt:      time.Now().Format(time.RFC3339),
	}

	h.mu.Lock()
	h.cachedResponse = &out
	h.cachedAt = time.Now()
	h.mu.Unlock()

	c.JSON(http.StatusOK, out)
}

// compareVersionStrings 按 "." 分段数字比较；a>b 返回 1，a<b 返回 -1，相等 0。
// 缺失段按 0 处理；空串视为 0。
func compareVersionStrings(a, b string) int {
	as := splitVersionSegments(a)
	bs := splitVersionSegments(b)
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		var av, bv int
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		if av != bv {
			if av > bv {
				return 1
			}
			return -1
		}
	}
	return 0
}

// splitVersionSegments 按 "." 分段转 int；空串返回 [0]。
// 预发布标记（-rc.1 / -beta / -alpha 等）单独处理：带预发布标记的版本小于同号正式版
// （如 1.8.0-rc.1 < 1.8.0），避免把 rc 段误判成更大版本。
func splitVersionSegments(v string) []int {
	v = normalizeVersionTag(v)
	if v == "" {
		return []int{0}
	}
	// 分离预发布标记
	prerelease := false
	if idx := strings.IndexAny(v, "-+"); idx >= 0 {
		prerelease = strings.IndexByte(v[idx:], '-') == 0 // 只有 '-' 是预发布，'+' 是构建元数据（不参与比较，直接忽略）
		v = v[:idx]
	}
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, _ := strconv.Atoi(strings.TrimSpace(p))
		out = append(out, n)
	}
	if prerelease {
		// 追加一个哨兵段：预发布版本在数字段相等时小于正式版
		out = append(out, -1)
	}
	return out
}
