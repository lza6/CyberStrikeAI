package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"go.uber.org/zap"
)

// GraphExtractor 从 chunk 文本抽取实体/关系（对齐 LightRAG entity_extraction）。
// 默认正则启发式抽取（无 LLM 依赖、确定性）；可选 LLM 抽取通过 SetLLMExtractor 注入。
//
// 抽取契约：
//   - 实体：name（title case）+ type + description；
//   - 关系：src+tgt（entity name）+ keywords（高层关键词）+ description；
//   - 来源：sourceID（知识项 ID）+ chunkIDs。
type GraphExtractor struct {
	entityTypes []string
	logger      *zap.Logger

	llm LLMGraphExtractor
}

// LLMGraphExtractor 可选 LLM 抽取接口（与 LightRAG 的 LLM 调用对齐）。
// 实现方负责把 chunk 文本送入 LLM，解析为实体/关系结构。
type LLMGraphExtractor interface {
	Extract(ctx context.Context, text string, entityTypes []string) (llmExtraction, error)
}

// llmExtraction LLM 抽取结果中间结构（内部）。
type llmExtraction struct {
	Entities  []llmEntity
	Relations []llmRelation
}

type llmEntity struct {
	Name        string
	Type        string
	Description string
}

type llmRelation struct {
	Src         string
	Tgt         string
	Keywords    string
	Description string
}

// NewGraphExtractor 构造；entityTypes 空则回退默认安全领域类型。
func NewGraphExtractor(entityTypes []string, logger *zap.Logger) *GraphExtractor {
	return &GraphExtractor{
		entityTypes: normalizeGraphEntityTypes(entityTypes),
		logger:      logger,
	}
}

// SetLLMExtractor 注入可选 LLM 抽取器；nil 表示纯启发式。
func (g *GraphExtractor) SetLLMExtractor(llm LLMGraphExtractor) {
	if g == nil {
		return
	}
	g.llm = llm
}

// Extract 从单条 chunk 抽取实体与关系。
//   - sourceID：来源知识项 ID；
//   - chunkID：来源 chunk ID；
//   - text：chunk 正文。
//
// 返回 Entity/Relation 列表（已填 SourceID/ChunkID）。
func (g *GraphExtractor) Extract(ctx context.Context, sourceID, chunkID, text string) ([]*Entity, []*Relation, error) {
	if g == nil {
		return nil, nil, fmt.Errorf("graph extractor: nil")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil, nil
	}

	// LLM 路径（可选）
	if g.llm != nil {
		le, err := g.llm.Extract(ctx, text, g.entityTypes)
		if err == nil {
			ents, rels := g.adaptLLMExtraction(le, sourceID, chunkID)
			return ents, rels, nil
		}
		if g.logger != nil {
			g.logger.Warn("LLM 图抽取失败，回退启发式", zap.Error(err))
		}
	}

	he, hr := g.heuristicExtract(sourceID, chunkID, text)
	return he, hr, nil
}

// adaptLLMExtraction 将 LLM 输出适配为 Entity/Relation（填 SourceID/ChunkID）。
func (g *GraphExtractor) adaptLLMExtraction(le llmExtraction, sourceID, chunkID string) ([]*Entity, []*Relation) {
	now := time.Now().UTC()
	ents := make([]*Entity, 0, len(le.Entities))
	for _, e := range le.Entities {
		name := titleCaseName(strings.TrimSpace(e.Name))
		if name == "" {
			continue
		}
		ents = append(ents, &Entity{
			Name:        name,
			Type:        strings.TrimSpace(e.Type),
			Description: strings.TrimSpace(e.Description),
			SourceID:    sourceID,
			ChunkIDs:    []string{chunkID},
			CreatedAt:   now,
			UpdatedAt:   now,
		})
	}
	rels := make([]*Relation, 0, len(le.Relations))
	for _, r := range le.Relations {
		src := titleCaseName(strings.TrimSpace(r.Src))
		tgt := titleCaseName(strings.TrimSpace(r.Tgt))
		if src == "" || tgt == "" || src == tgt {
			continue
		}
		rels = append(rels, &Relation{
			SrcID:       src,
			TgtID:       tgt,
			Keywords:    strings.TrimSpace(r.Keywords),
			Description: strings.TrimSpace(r.Description),
			Weight:      1.0,
			SourceID:    sourceID,
			ChunkIDs:    []string{chunkID},
			CreatedAt:   now,
			UpdatedAt:   now,
		})
	}
	return ents, rels
}

// heuristicExtract 启发式抽取：识别 CVE 编号、常见技术名词、标题级实体，
// 以及"A 影响/利用 B"、"A 导致 B"等关系模式。
//
// 适用场景：无 LLM 可用时的确定性兜底；不追求召回率，只保证可运行与可测。
func (g *GraphExtractor) heuristicExtract(sourceID, chunkID, text string) ([]*Entity, []*Relation) {
	now := time.Now().UTC()
	var ents []*Entity
	var rels []*Relation

	// 1. CVE 编号实体
	cveRe := regexp.MustCompile(`CVE-\d{4}-\d{3,7}`)
	cves := uniqueMatches(cveRe.FindAllString(text, -1))
	for _, c := range cves {
		ents = append(ents, &Entity{
			Name:        titleCaseName(c),
			Type:        "CVE",
			Description: fmt.Sprintf("%s 漏洞编号，出现在知识项 %s", c, sourceID),
			SourceID:    sourceID,
			ChunkIDs:    []string{chunkID},
			CreatedAt:   now,
			UpdatedAt:   now,
		})
	}

	// 2. 标题级实体：Markdown 标题行（# / ## / ###）首行作为实体
	titleRe := regexp.MustCompile(`(?m)^#{1,4}\s+(.+?)\s*$`)
	titles := titleRe.FindAllStringSubmatch(text, -1)
	for _, m := range titles {
		name := strings.TrimSpace(m[1])
		if name == "" {
			continue
		}
		ents = append(ents, &Entity{
			Name:        titleCaseName(name),
			Type:        classifyEntityType(name, g.entityTypes),
			Description: fmt.Sprintf("知识项 %s 的标题：%s", sourceID, name),
			SourceID:    sourceID,
			ChunkIDs:    []string{chunkID},
			CreatedAt:   now,
			UpdatedAt:   now,
		})
	}

	// 3. 关系模式：X(影响|导致|利用|依赖于|配合|触发|缓解)Y
	// 启发式：以中文字符为主的实体名也需支持，故捕获组允许中文/字母/数字/连字符/空格。
	relRe := regexp.MustCompile(`([\p{L}][\p{L}\p{N}\w\- ]{0,40}?)\s*(影响|导致|利用|依赖于|配合|触发|缓解)\s*([\p{L}][\p{L}\p{N}\w\- ]{0,40}?)`)
	nameSet := make(map[string]bool, len(ents))
	for _, e := range ents {
		nameSet[e.Name] = true
	}
	for _, m := range relRe.FindAllStringSubmatch(text, -1) {
		src := titleCaseName(strings.TrimSpace(m[1]))
		tgt := titleCaseName(strings.TrimSpace(m[3]))
		verb := strings.TrimSpace(m[2])
		if src == "" || tgt == "" || src == tgt {
			continue
		}
		// 放宽：任一端在实体集即建立关系（另一端自动补为实体）
		if !nameSet[src] {
			ents = append(ents, &Entity{
				Name:        src,
				Type:        "其他",
				Description: fmt.Sprintf("从知识项 %s 关系中抽取", sourceID),
				SourceID:    sourceID,
				ChunkIDs:    []string{chunkID},
				CreatedAt:   now,
				UpdatedAt:   now,
			})
			nameSet[src] = true
		}
		if !nameSet[tgt] {
			ents = append(ents, &Entity{
				Name:        tgt,
				Type:        "其他",
				Description: fmt.Sprintf("从知识项 %s 关系中抽取", sourceID),
				SourceID:    sourceID,
				ChunkIDs:    []string{chunkID},
				CreatedAt:   now,
				UpdatedAt:   now,
			})
			nameSet[tgt] = true
		}
		rels = append(rels, &Relation{
			SrcID:       src,
			TgtID:       tgt,
			Keywords:    verb,
			Description: fmt.Sprintf("%s %s %s", src, verb, tgt),
			Weight:      1.0,
			SourceID:    sourceID,
			ChunkIDs:    []string{chunkID},
			CreatedAt:   now,
			UpdatedAt:   now,
		})
	}

	return ents, rels
}

// titleCaseName 将实体名规范化为 Title Case（首字母大写）。
// 用于保证跨文档实体名一致，便于图合并与向量召回。
func titleCaseName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// CVE-XXXX-XXXXX 保持原样（大写）
	if strings.HasPrefix(strings.ToUpper(s), "CVE-") {
		return strings.ToUpper(s)
	}
	// 全大写或全小写的短 token 直接返回（如 XSS、SQL）
	upper := strings.ToUpper(s)
	if s == upper || s == strings.ToLower(s) {
		return upper
	}
	// 否则按空格分词，每词首字母大写
	parts := strings.Fields(s)
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}

// classifyEntityType 按名称启发式分类（CVE / 漏洞 / 攻击技术 / 防御措施 / 其他）。
func classifyEntityType(name string, entityTypes []string) string {
	n := strings.ToUpper(name)
	switch {
	case strings.HasPrefix(n, "CVE-"):
		return "CVE"
	case strings.Contains(n, "注入") || strings.Contains(n, "XSS") || strings.Contains(n, "SSRF") ||
		strings.Contains(n, "RCE") || strings.Contains(n, "提权") || strings.Contains(n, "绕过"):
		return "漏洞"
	case strings.Contains(n, "扫描") || strings.Contains(n, "枚举") || strings.Contains(n, "爆破") ||
		strings.Contains(n, "钓鱼") || strings.Contains(n, "后门") || strings.Contains(n, "横向"):
		return "攻击技术"
	case strings.Contains(n, "防护") || strings.Contains(n, "检测") || strings.Contains(n, "加固") ||
		strings.Contains(n, "修复") || strings.Contains(n, "监控"):
		return "防御措施"
	}
	return "其他"
}

// uniqueMatches 字符串切片去重保序。
func uniqueMatches(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// EncodeEntities 将实体列表序列化为 JSON（调试/日志用）。
func EncodeEntities(ents []*Entity) string {
	b, _ := json.Marshal(ents)
	return string(b)
}
