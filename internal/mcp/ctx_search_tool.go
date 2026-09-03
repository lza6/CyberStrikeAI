package mcp

import (
	"context"
	"fmt"
	"strings"

	"cyberstrike-ai/internal/ctxsandbox"
)

// ctxSearchToolName is the retrieval-side companion to ctx_execute: it runs a
// BM25 query against whatever the sandbox engine has indexed and returns the
// full content of matching sections (bounded), so the model can pull back
// specifics of a previously-indexed large output on demand.
const ctxSearchToolName = "ctx_search"

// ctxSearchToolShort is the short description shown in the tool list.
const ctxSearchToolShort = "按 BM25 检索 ctx_execute 已索引的大输出 section 全文"

// RegisterCtxSearchTool wires the retrieval companion to ctx_execute. It is
// what ctx_execute's pointer/verdict text "Use ctx_search(queries:[...])"
// points at — closing the loop so the model can retrieve full section content
// after a large output was indexed rather than returned wholesale.
//
// The tool takes queries (array of strings, OR semantics within a query,
// ranked across the union of results) and an optional source label to scope
// to a specific ctx_execute run's index. Results return the matching chunk's
// title + its full content, bounded to a safety cap so a single retrieval
// cannot re-flood the context budget.
func RegisterCtxSearchTool(server *Server, index ctxsandbox.Index) {
	if server == nil || index == nil {
		return
	}
	server.RegisterTool(Tool{
		Name:             ctxSearchToolName,
		ShortDescription: ctxSearchToolShort,
		Description: "检索 ctx_execute 已索引的大输出。当 ctx_execute 返回指针（\"Indexed N sections " +
			"from: execute:sh\"）或 verdict（\"matching sections: ...\"）时，用本工具取回匹配 section 的全文。" +
			"参数：queries（字符串数组，必填，每个 query 内部 OR，多 query 结果合并排序）、source（可选，" +
			"ctx_execute 返回的 label 如 \"execute:nmap\"，限定检索范围）、max_results（可选，默认 8，上限 32）。" +
			"返回：每条命中 = 标题 + section 全文（单条 ≤ 12KB，总 ≤ 48KB，超出截断并提示）。" +
			"语义对齐 context-mode ctx_search（server.ts）。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"queries": map[string]interface{}{
					"type": "array",
					"items": map[string]interface{}{"type": "string"},
					"description": "BM25 查询数组（每个 query 内部 OR；多 query 结果合并排序后取 top-N）",
				},
				"source": map[string]interface{}{
					"type": "string",
					"description": "限定检索范围，填 ctx_execute 返回的 label（如 \"execute:nmap\"）",
				},
				"max_results": map[string]interface{}{
					"type": "number",
					"description": "最多返回 section 数，默认 8，上限 32",
				},
			},
			"required": []string{"queries"},
		},
	}, func(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
		queries, ok := args["queries"].([]interface{})
		if !ok || len(queries) == 0 {
			return textToolResult("queries 必填（字符串数组，至少一个）", true), nil
		}
		source := strings.TrimSpace(stringArg(args, "source"))
		maxResults := intArg(args, "max_results", 8, 32)
		if maxResults <= 0 {
			maxResults = 8
		}

		var allHits []ctxsandboxHit
		for _, q := range queries {
			qs, _ := q.(string)
			qs = strings.TrimSpace(qs)
			if qs == "" {
				continue
			}
			hits := index.Search(qs, source, maxResults)
			for _, h := range hits {
				allHits = append(allHits, ctxsandboxHit{
					Title:   h.Doc.Title,
					Content: h.Doc.Content,
					Source:  h.Doc.Source,
					Score:   h.Score,
				})
			}
		}
		if len(allHits) == 0 {
			emptyMsg := "无匹配 section。"
			if source != "" {
				emptyMsg = fmt.Sprintf("source %q 下无匹配 section（确认 label 拼写与 ctx_execute 返回一致）", source)
			}
			return textToolResult(emptyMsg, false), nil
		}

		text := renderCtxSearchHits(allHits, maxResults)
		return textToolResult(text, false), nil
	})
}

// IsCtxSearchTool reports whether name is the ctx_search tool. Symmetric to
// IsCtxExecuteTool for audit/policy recognition.
func IsCtxSearchTool(name string) bool { return name == ctxSearchToolName }

// ctxsandboxHit is a transport copy of ctxindex.Scored's Doc fields, kept
// local so this file has no import of internal/ctxindex (only ctxsandbox's
// Index interface, which returns ctxindex.Scored — but we dereference Doc
// into a local struct to keep the rendering pure and testable).
type ctxsandboxHit struct {
	Title   string
	Content string
	Source  string
	Score   float64
}

// renderCtxSearchHitPerSectionCap bounds a single section's full content so
// one retrieval cannot re-flood the context budget; the total is bounded in
// renderCtxSearchHits. Mirrors context-mode's capBytes (truncate.ts:145).
const (
	ctxSearchPerSectionMaxBytes = 12_000
	ctxSearchTotalMaxBytes     = 48_000
)

// renderCtxSearchHits renders ranked hits into a bounded, model-facing text.
// Each section shows its title + full content (capped per-section); the total
// is capped and, on overflow, truncates with a pointer note.
func renderCtxSearchHits(hits []ctxsandboxHit, maxResults int) string {
	if len(hits) == 0 {
		return ""
	}
	if maxResults > 0 && len(hits) > maxResults {
		hits = hits[:maxResults]
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("命中 %d 个 section：\n\n", len(hits)))
	total := 0
	for i, h := range hits {
		header := fmt.Sprintf("[%d] %s (source: %s, score: %.4f)\n", i+1, h.Title, h.Source, h.Score)
		body := capBytesSafe(h.Content, ctxSearchPerSectionMaxBytes)
		section := header + body + "\n\n"
		if total+len(section) > ctxSearchTotalMaxBytes {
			room := ctxSearchTotalMaxBytes - total
			if room > len(header) {
				b.WriteString(header)
				b.WriteString(capBytesSafe(body, room-len(header)))
				b.WriteString("\n\n[...后续 section 因总字数上限截断；用更窄的 queries/source 分批检索]\n")
			} else {
				b.WriteString("\n[...后续 section 因总字数上限截断；用更窄的 queries/source 分批检索]\n")
			}
			break
		}
		b.WriteString(section)
		total += len(section)
	}
	return b.String()
}

// capBytesSafe trims s to at most maxBytes without cutting a multi-byte UTF-8
// glyph, appending an ellipsis when truncated. Mirrors context-mode's
// byteSafePrefix / capBytes (truncate.ts:22-44, 145-154).
func capBytesSafe(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	// Walk back to a rune boundary so we never split a multi-byte glyph.
	cut := maxBytes
	for cut > 0 && !runeStart(s[cut]) {
		cut--
	}
	if cut <= 0 {
		return "…"
	}
	return s[:cut] + "…"
}

// runeStart reports whether b is the first byte of an encoded rune. Lifted
// from unicode/utf8 to avoid an import here (the helper is trivial).
func runeStart(b byte) bool { return b&0xC0 != 0x80 }
