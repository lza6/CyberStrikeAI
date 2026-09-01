package handler

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"cyberstrike-ai/internal/audit"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

// systemPromptFilenameRe 限制提示词文件名：字母数字开头，仅含字母数字 _ . -，必须 .yaml 后缀。
var systemPromptFilenameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*\.yaml$`)

// builtinSystemPromptFilename 列表中代表"内置默认提示"的虚拟文件名。
const builtinSystemPromptFilename = "__builtin__"

// systemPromptFile 提示词 yaml 文件的磁盘格式。
type systemPromptFile struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Content     string `yaml:"content"`
}

// SystemPromptsHandler 管理单代理系统提示词（prompts/ 目录 yaml 增删改查 + 激活）。
// 激活 = 更新内存 config.Agent.SystemPromptPath（agent 每轮读取，热生效）；不直接改写 config.yaml（保留用户注释）。
type SystemPromptsHandler struct {
	dir string // prompts/ 绝对路径
	// relPromptsDir 激活时写入 config.Agent.SystemPromptPath 的相对目录前缀
	//（相对 config.yaml 所在目录，与 agent.promptBaseDir 对齐），默认 "prompts"。
	relPromptsDir string
	config        ConfigAgentConfigAccessor
	audit         *audit.Service
	logger        *zap.Logger
}

// ConfigAgentConfigAccessor 激活提示词所需的最小配置接口（由 *config.Config 满足）。
// 通过接口解耦，便于测试注入。
type ConfigAgentConfigAccessor interface {
	GetAgentSystemPromptPath() string
	SetAgentSystemPromptPath(path string)
}

// NewSystemPromptsHandler dir 为 prompts 目录（可为相对 config.yaml 的路径，构造前由调用方解析为绝对路径）。
func NewSystemPromptsHandler(dir string) *SystemPromptsHandler {
	return &SystemPromptsHandler{dir: strings.TrimSpace(dir), relPromptsDir: "prompts"}
}

// SetConfig 注入配置访问器（读写 agent.system_prompt_path）。
func (h *SystemPromptsHandler) SetConfig(acc ConfigAgentConfigAccessor) {
	h.config = acc
}

// SetAudit wires platform audit logging.
func (h *SystemPromptsHandler) SetAudit(s *audit.Service) {
	h.audit = s
}

// SetLogger wires a logger for sandboxed 5xx errors via internalError.
func (h *SystemPromptsHandler) SetLogger(l *zap.Logger) {
	h.logger = l
}

func (h *SystemPromptsHandler) safeJoin(filename string) (string, error) {
	filename = strings.TrimSpace(filename)
	if filename == "" || !systemPromptFilenameRe.MatchString(filename) {
		return "", fmt.Errorf("非法文件名：仅允许字母数字开头的 .yaml 文件")
	}
	clean := filepath.Clean(filename)
	if clean != filename || strings.Contains(clean, "..") {
		return "", fmt.Errorf("非法文件名")
	}
	return filepath.Join(h.dir, clean), nil
}

// promptEntry 对外暴露的提示词条目。
type promptEntry struct {
	Filename    string `json:"filename"`
	Name        string `json:"name"`
	Description string `json:"description"`
	IsActive    bool   `json:"is_active"`
	IsBuiltin   bool   `json:"is_builtin"`
}

// currentActiveFilename 当前生效的提示词文件名；未激活内置提示时返回空串。
func (h *SystemPromptsHandler) currentActiveFilename() string {
	if h.config == nil {
		return ""
	}
	p := strings.TrimSpace(h.config.GetAgentSystemPromptPath())
	if p == "" {
		return ""
	}
	return filepath.Base(p)
}

func (h *SystemPromptsHandler) loadPromptFile(path string) (*systemPromptFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f systemPromptFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("解析 yaml 失败: %w", err)
	}
	return &f, nil
}

// ListSystemPrompts GET /api/system-prompts
// 扫描 prompts/ 目录返回 [{filename,name,description,is_active,is_builtin}]；额外附带内置提示兜底条目。
func (h *SystemPromptsHandler) ListSystemPrompts(c *gin.Context) {
	active := h.currentActiveFilename()
	out := make([]promptEntry, 0, 8)

	out = append(out, promptEntry{
		Filename:    builtinSystemPromptFilename,
		Name:        "内置默认提示",
		Description: "程序内置的单代理系统提示（未激活任何文件时使用）",
		IsActive:    active == "",
		IsBuiltin:   true,
	})

	if h.dir != "" {
		if entries, err := os.ReadDir(h.dir); err == nil {
			for _, e := range entries {
				if e.IsDir() || !systemPromptFilenameRe.MatchString(e.Name()) {
					continue
				}
				f, err := h.loadPromptFile(filepath.Join(h.dir, e.Name()))
				if err != nil {
					// 单文件损坏不阻断列表，仅暴露文件名供用户修复或删除
					out = append(out, promptEntry{
						Filename:    e.Name(),
						Name:        e.Name(),
						Description: fmt.Sprintf("（文件解析失败：%v）", err),
						IsActive:    active == e.Name(),
						IsBuiltin:   false,
					})
					continue
				}
				name := strings.TrimSpace(f.Name)
				if name == "" {
					name = e.Name()
				}
				out = append(out, promptEntry{
					Filename:    e.Name(),
					Name:        name,
					Description: strings.TrimSpace(f.Description),
					IsActive:    active == e.Name(),
					IsBuiltin:   false,
				})
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{"prompts": out, "dir": h.dir})
}

// GetSystemPrompt GET /api/system-prompts/:filename — 返回完整内容供编辑。
func (h *SystemPromptsHandler) GetSystemPrompt(c *gin.Context) {
	filename := c.Param("filename")
	path, err := h.safeJoin(filename)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	f, err := h.loadPromptFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "文件不存在"})
			return
		}
		internalError(c, h.logger, "systemprompts.GetSystemPrompt:loadFile", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"filename":    filepath.Base(path),
		"name":        f.Name,
		"description": f.Description,
		"content":     f.Content,
	})
}

type systemPromptBody struct {
	Filename    string `json:"filename"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Content     string `json:"content"`
}

func (h *SystemPromptsHandler) writePromptFile(path string, body systemPromptBody) error {
	name := strings.TrimSpace(body.Name)
	if name == "" {
		return fmt.Errorf("name 必填")
	}
	if strings.TrimSpace(body.Content) == "" {
		return fmt.Errorf("content 必填")
	}
	out, err := yaml.Marshal(&systemPromptFile{
		Name:        name,
		Description: strings.TrimSpace(body.Description),
		Content:     body.Content,
	})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(h.dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(path, out, 0644)
}

// CreateSystemPrompt POST /api/system-prompts — body {filename,name,description,content}。
func (h *SystemPromptsHandler) CreateSystemPrompt(c *gin.Context) {
	if h.dir == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "未配置 prompts 目录"})
		return
	}
	var body systemPromptBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	filename := strings.TrimSpace(body.Filename)
	if filename == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "filename 必填"})
		return
	}
	// 强制 .yaml 后缀；文件名必须整体满足白名单正则（不允许路径分隔符/..，
	// 因此 ../evil.yaml 在这里直接被拒绝而不是被 Base 吞掉前缀）。
	if !strings.HasSuffix(filename, ".yaml") {
		filename += ".yaml"
	}
	path, err := h.safeJoin(filename)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if _, err := os.Stat(path); err == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "文件已存在"})
		return
	}
	if err := h.writePromptFile(path, body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if h.audit != nil {
		h.audit.RecordOK(c, "config", "system_prompt_create", "创建系统提示词", "system_prompt", filepath.Base(path), nil)
	}
	c.JSON(http.StatusOK, gin.H{"filename": filepath.Base(path), "message": "已创建"})
}

// UpdateSystemPrompt PUT /api/system-prompts/:filename
func (h *SystemPromptsHandler) UpdateSystemPrompt(c *gin.Context) {
	filename := c.Param("filename")
	path, err := h.safeJoin(filename)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var body systemPromptBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		c.JSON(http.StatusNotFound, gin.H{"error": "文件不存在"})
		return
	}
	if err := h.writePromptFile(path, body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if h.audit != nil {
		h.audit.RecordOK(c, "config", "system_prompt_update", "更新系统提示词", "system_prompt", filepath.Base(path), nil)
	}
	c.JSON(http.StatusOK, gin.H{"message": "已保存"})
}

// DeleteSystemPrompt DELETE /api/system-prompts/:filename
// 删除当前激活文件时自动回退到内置提示（system_prompt_path 置空）。
func (h *SystemPromptsHandler) DeleteSystemPrompt(c *gin.Context) {
	filename := c.Param("filename")
	path, err := h.safeJoin(filename)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "文件不存在"})
			return
		}
		internalError(c, h.logger, "systemprompts.DeleteSystemPrompt:remove", err)
		return
	}
	if h.config != nil && h.currentActiveFilename() == filepath.Base(path) {
		h.config.SetAgentSystemPromptPath("")
	}
	if h.audit != nil {
		h.audit.RecordOK(c, "config", "system_prompt_delete", "删除系统提示词", "system_prompt", filepath.Base(path), nil)
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// ActivateSystemPrompt POST /api/system-prompts/:filename/activate
// 更新内存 config.Agent.SystemPromptPath（每轮对话读取，热生效）。为持久化到磁盘，
// 建议在 config.yaml 固化 system_prompt_path；接口返回提示文案。
// filename = __builtin__ 时激活内置提示（路径置空）。
// 存储值 = "prompts/<filename>"（相对 config.yaml 所在目录），与 agent.promptBaseDir
//（= configDir）拼路径约定一致；此前只存文件名会让 agent 去 configDir 根下找文件而读不到。
func (h *SystemPromptsHandler) ActivateSystemPrompt(c *gin.Context) {
	filename := c.Param("filename")
	var target string
	if filename != builtinSystemPromptFilename {
		path, err := h.safeJoin(filename)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if _, err := os.Stat(path); os.IsNotExist(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "文件不存在"})
			return
		}
		target = filepath.ToSlash(filepath.Join(h.relPromptsDir, filepath.Base(path)))
	}
	if h.config == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "配置访问器未注入，无法激活"})
		return
	}
	h.config.SetAgentSystemPromptPath(target)
	if h.audit != nil {
		h.audit.RecordOK(c, "config", "system_prompt_activate", "激活系统提示词", "system_prompt", target, nil)
	}
	c.JSON(http.StatusOK, gin.H{
		"message":       "已激活（新对话立即生效）。如需持久化，请在 config.yaml 的 agent.system_prompt_path 固化该值。",
		"active_prompt": target,
	})
}

// CurrentSystemPrompt GET /api/system-prompts/current — 返回当前生效路径与内置提示兜底说明。
func (h *SystemPromptsHandler) CurrentSystemPrompt(c *gin.Context) {
	p := ""
	if h.config != nil {
		p = strings.TrimSpace(h.config.GetAgentSystemPromptPath())
	}
	if p == "" {
		c.JSON(http.StatusOK, gin.H{
			"active_filename": builtinSystemPromptFilename,
			"prompt_path":     "",
			"is_builtin":      true,
			"name":            "内置默认提示",
		})
		return
	}
	base := filepath.Base(p)
	name := base
	if h.dir != "" {
		if f, err := h.loadPromptFile(filepath.Join(h.dir, base)); err == nil && strings.TrimSpace(f.Name) != "" {
			name = strings.TrimSpace(f.Name)
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"active_filename": base,
		"prompt_path":     p,
		"is_builtin":      false,
		"name":            name,
	})
}
