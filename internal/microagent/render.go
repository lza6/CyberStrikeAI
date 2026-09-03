package microagent

import (
	"strings"
)

// RenderExtraInfo 渲染命中的 knowledge microagent 为上下文块。
// 移植自 openhands/agenthub/codeact_agent/prompts/microagent_info.j2 + utils/prompt.py:122-134。
// 每个 Knowledge 包一个 <EXTRA_INFO> section，说明触发关键词 + 内容。
func RenderExtraInfo(agents []Knowledge) string {
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
	return b.String()
}

// RenderRepoInstructions 渲染 always-on repo microagent 内容为指令块。
// 移植自 openhands/agenthub/codeact_agent/prompts/additional_info.j2 的
// <REPOSITORY_INSTRUCTIONS> 块 + utils/prompt.py:107-120 build_workspace_context。
// content 为 Registry.RepoContent() 返回的拼接体；空则返回空串。
func RenderRepoInstructions(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("<REPOSITORY_INSTRUCTIONS>\n")
	b.WriteString(content)
	b.WriteString("\n</REPOSITORY_INSTRUCTIONS>\n")
	return b.String()
}
