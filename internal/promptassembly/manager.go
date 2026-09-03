package promptassembly

import (
	"strings"
	"text/template"
)

// Manager prompt 组装管理器。移植自 openhands/utils/prompt.py:43-152 PromptManager。
// Go 版用 text/template（非 Jinja2），模板内联为常量（与 projectprompt 一样不依赖外部 .j2 文件），
// 保持 leaf 包无文件依赖。
type Manager struct {
	tmpl *template.Template
}

// NewManager 构造并解析内置模板。解析失败会 panic（编译期保证模板正确）。
func NewManager() *Manager {
	m := &Manager{}
	tmpl := template.Must(template.New("promptassembly").Parse(workspaceContextTmpl))
	m.tmpl = tmpl
	return m
}

// Render 渲染 Context 为 prompt 块字符串。
// 移植自 openhands/utils/prompt.py:107-134 build_workspace_context + build_microagent_info。
// 各字段为空时条件跳过（避免空标签污染 prompt）。幂等：同一 Context 多次渲染结果一致。
func (m *Manager) Render(ctx Context) string {
	var b strings.Builder
	if err := m.tmpl.Execute(&b, ctx); err != nil {
		// 模板已编译期校验，运行时错误极罕见；返回空串避免污染 prompt。
		return ""
	}
	return strings.TrimSpace(b.String())
}

// RenderWorkspaceContext 仅渲染 workspace_context 块（repo info + runtime info + conv instr + repo instructions）。
// 等价于 OpenHands build_workspace_context。
func (m *Manager) RenderWorkspaceContext(ctx Context) string {
	return m.Render(ctx)
}

// RenderMicroagentInfo 仅渲染 microagent 触发块（EXTRA_INFO）。
// 等价于 OpenHands build_microagent_info。
func (m *Manager) RenderMicroagentInfo(agents []MicroagentKnowledge) string {
	if len(agents) == 0 {
		return ""
	}
	var b strings.Builder
	for _, a := range agents {
		b.WriteString("<EXTRA_INFO>\n")
		b.WriteString("The following information has been included based on a keyword match for \"")
		b.WriteString(a.Trigger)
		b.WriteString("\" (microagent: ")
		b.WriteString(a.Name)
		b.WriteString(").\n\n")
		b.WriteString(a.Content)
		b.WriteString("\n</EXTRA_INFO>\n")
	}
	return strings.TrimSpace(b.String())
}

// workspaceContextTmpl workspace 上下文模板。移植自 openhands/agenthub/codeact_agent/prompts/additional_info.j2。
// 用 Go template 语法复刻 <REPOSITORY_INFO> / <RUNTIME_INFORMATION> / <CONVERSATION_INSTRUCTIONS> / <REPOSITORY_INSTRUCTIONS> 块。
// 条件渲染：字段为空跳过整块。
const workspaceContextTmpl = `{{- /* REPOSITORY_INFO */ -}}
{{if or .RepositoryInfo.RepoName .RepositoryInfo.RepoDirectory}}<REPOSITORY_INFO>
Repository: {{.RepositoryInfo.RepoName}}
Directory: {{.RepositoryInfo.RepoDirectory}}{{if .RepositoryInfo.BranchName}}
Branch: {{.RepositoryInfo.BranchName}}{{end}}
The repository has been cloned to the working directory.
</REPOSITORY_INFO>
{{end}}{{- /* RUNTIME_INFORMATION */ -}}
{{if not .RuntimeInfo.IsEmpty}}<RUNTIME_INFORMATION>
{{if .RuntimeInfo.Date}}Date: {{.RuntimeInfo.Date}}
{{end}}{{if .RuntimeInfo.WorkingDir}}Working Directory: {{.RuntimeInfo.WorkingDir}}
{{end}}{{if .RuntimeInfo.AdditionalAgentInstructions}}Additional Instructions: {{.RuntimeInfo.AdditionalAgentInstructions}}
{{end}}{{if .RuntimeInfo.AvailableHosts}}Available Hosts:{{range $host, $port := .RuntimeInfo.AvailableHosts}}
  {{$host}}:{{$port}}{{end}}
{{end}}{{if .RuntimeInfo.CustomSecretsDescriptions}}<CUSTOM_SECRETS>{{range $name, $desc := .RuntimeInfo.CustomSecretsDescriptions}}
{{$name}}: {{$desc}}{{end}}
</CUSTOM_SECRETS>
{{end}}</RUNTIME_INFORMATION>
{{end}}{{- /* CONVERSATION_INSTRUCTIONS */ -}}
{{if .ConversationInstructions.Content}}<CONVERSATION_INSTRUCTIONS>
{{.ConversationInstructions.Content}}
</CONVERSATION_INSTRUCTIONS>
{{end}}{{- /* REPOSITORY_INSTRUCTIONS */ -}}
{{if .RepoInstructions}}<REPOSITORY_INSTRUCTIONS>
{{.RepoInstructions}}
</REPOSITORY_INSTRUCTIONS>
{{end}}`
