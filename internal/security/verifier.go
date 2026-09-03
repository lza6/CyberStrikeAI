// Package security 提供 CyberStrikeAI 的安全闸门与验证原语。
//
// 本文件实现 K8 安全深化的"验证侧"补齐：Verifier 4-axis 闸门 +
// evidence ladder 四级证据等级 + Playbook PROVE/RULE_OUT 表。
//
// 设计目的：penetration / impact-exfiltration 子代理产出的 candidate finding
// 在落库 / 上报为"已确认"之前，必须过 4-axis（real / triggerable / impactful /
// general）验证，并按证据强度落到四级 evidence_level（suspected < corroborated
// < reproduced < impact_proven）。fail-closed：未过闸的 finding 一律降级为
// suspected，不得对外报为 confirmed。
//
// 立场保护：本闸门只判定"证据是否充分"，不对授权本身做二次确认。授权边界
// 由系统与组织前置裁决，verifier 不质疑、不索取授权凭证。
package security

import (
	"fmt"
	"strings"
)

// EvidenceLevel 证据强度等级（由弱到强）。
//
// 升降级规则：
//   - suspected:      仅理论可行 / 单点信号 / 未复现
//   - corroborated:   多源信号相互印证，但缺少主动复现
//   - reproduced:     已主动复现，链路打通（OAST 回调 / DOM marker / 数据回显等）
//   - impact_proven: 已证明业务影响（真实数据可达 / 权限提升 / 跨租户访问等）
//
// 严格偏序：suspected < corroborated < reproduced < impact_proven。
type EvidenceLevel int

const (
	// EvidenceSuspected 最低级：理论可行或单点信号，未复现。
	EvidenceSuspected EvidenceLevel = iota + 1
	// EvidenceCorroborated 多源信号相互印证，缺少主动复现。
	EvidenceCorroborated
	// EvidenceReproduced 已主动复现，链路打通。
	EvidenceReproduced
	// EvidenceImpactProven 已证明业务影响。
	EvidenceImpactProven
)

// String 返回 evidence_level 的稳定字符串标识，用于序列化与日志。
func (l EvidenceLevel) String() string {
	switch l {
	case EvidenceSuspected:
		return "suspected"
	case EvidenceCorroborated:
		return "corroborated"
	case EvidenceReproduced:
		return "reproduced"
	case EvidenceImpactProven:
		return "impact_proven"
	default:
		return "unknown"
	}
}

// ParseEvidenceLevel 把字符串解析为 EvidenceLevel。未知 / 空串 → EvidenceSuspected。
// 解析时大小写不敏感，前后空白被截断。
func ParseEvidenceLevel(s string) EvidenceLevel {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "suspected":
		return EvidenceSuspected
	case "corroborated":
		return EvidenceCorroborated
	case "reproduced":
		return EvidenceReproduced
	case "impact_proven":
		return EvidenceImpactProven
	default:
		return EvidenceSuspected
	}
}

// 4-axis 验证维度名称。Candidate finding 必须四轴全过才算 confirmed。
const (
	AxisReal        = "real"        // 真实存在：非误报，响应/产物来自真实目标
	AxisTriggerable = "triggerable" // 可触发：payload/请求能驱动目标行为变化
	AxisImpactful   = "impactful"   // 有影响：能造成安全相关后果（数据/权限/DoS 等）
	AxisGeneral     = "general"     // 通用性：非单点巧合，能在同类端点/参数复现
)

// AxisCheck 单条 4-axis 验证结果。
type AxisCheck struct {
	Name   string // real / triggerable / impactful / general
	Passed bool   // 是否通过
	Reason string // 通过/未通过的依据（带证据指针）
}

// CandidateFinding 待 verifier 闸门校验的候选 finding。
//
// 字段映射 penetration / impact-exfiltration 子代理产出的 finding 结构：
// finding 本身不携带授权信息——授权由前置系统裁决，verifier 不读不写授权字段。
type CandidateFinding struct {
	// ID 候选 finding 的本地标识（用于追溯）。
	ID string
	// Category 漏洞类别，用于匹配 Playbook（如 "SSRF"/"XSS"/"SQLi"）。
	Category string
	// Target 精确目标（URL / IP:Port / host:port）。
	Target string
	// Indicator 具体证据指针：状态码 / 响应片段 / OOB 命中 / DOM marker。
	Indicator string
	// Reproduction 复现指令（curl / HTTP 请求 / payload）。
	Reproduction string
	// Axes 4-axis 各维度的判定（由子代理初判，verifier 复核）。
	Axes []AxisCheck
	// EvidenceLevel 子代理自评的证据等级（verifier 可基于 Playbook 升降级）。
	EvidenceLevel EvidenceLevel
	// PlaybookMatch 命中的 PROVE 锚点（如 "oast_callback"），空表示未命中。
	PlaybookMatch string
}

// VerifierResult 闸门校验输出。
type VerifierResult struct {
	// FindingID 对应 CandidateFinding.ID。
	FindingID string
	// Confirmed 是否过闸（4-axis 全过 + evidence_level >= corroborated）。
	Confirmed bool
	// FinalLevel 闸门裁定的最终证据等级（未过闸一律降为 suspected）。
	FinalLevel EvidenceLevel
	// AxisReport 各维度复核结果（含未过原因）。
	AxisReport []AxisCheck
	// RuleOutHit 命中的 RULE_OUT 项（命中即否决，Confirmed=false）。
	RuleOutHit string
	// PlaybookMatch 命中的 PROVE 锚点（用于审计追溯，confirmed 时非空）。
	PlaybookMatch string
	// Reason 闸门裁定的综合理由（供审计/上报）。
	Reason string
}

// playbookEntry Playbook 表条目：一类漏洞的 PROVE 锚点与 RULE_OUT 反例。
type playbookEntry struct {
	// Category 匹配关键字（大小写不敏感包含匹配）。
	Category string
	// Prove PROVE 锚点：finding 必须命中其中至少一个才能升至 reproduced 及以上。
	Prove []string
	// RuleOut RULE_OUT 反例：命中任意一个即否决（降为 suspected，Confirmed=false）。
	RuleOut []string
}

// playbook Playbook PROVE/RULE_OUT 表。
//
// 设计参考 strix 的 PROVE/RULE_OUT 思想：
//   - PROVE：要证明某类漏洞为真，必须有的"正向证据锚点"。缺锚点 → 无法升级。
//   - RULE_OUT：常见误报模式，命中即否决，不让疑似冒充已确认。
//
// 立场保护：Playbook 只描述"如何判定证据真伪"，不含授权语义。
var playbook = []playbookEntry{
	{
		Category: "ssrf",
		Prove: []string{
			"oast_callback",           // OAST 回调命中
			"unroutable_control",      // 对照不可路由地址无回连 → 排除网络巧合
			"metadata_endpoint",       // 云元数据端点命中（169.254.169.254）
			"internal_port_reachable", // 内网端口可达的真实响应差异
		},
		RuleOut: []string{
			"timeout_only",         // 仅超时，无 OOB/响应差异 → 不可判定为通
			"dns_only_no_response", // 仅 DNS 解析，无实际回连
			"error_message_guess",  // 仅凭错误信息猜测，无链路打通
		},
	},
	{
		Category: "xss",
		Prove: []string{
			"alert_executed",       // 实际触发 alert（浏览器/无头引擎验证）
			"dom_marker",           // DOM 中出现注入的 marker
			"reflected_in_html",    // payload 反射到 HTML 且未转义
			"script_tag_execution", // <script> 标签实际执行
		},
		RuleOut: []string{
			"payload_not_reflected",  // payload 未反射 → 误报
			"encoded_output",         // 输出被转义 → 不可执行
			"content_type_json_only", // 仅 JSON 上下文反射，无 HTML 渲染
		},
	},
	{
		Category: "sqli",
		Prove: []string{
			"data_echo",          // 数据回显：UNION/错误回显带出真实数据
			"boolean_difference", // 布尔差异：真/假条件响应可区分
			"time_based_delay",   // 时间盲注：条件触发可观测延迟
			"oast_oob_lookup",    // OAST OOB 外带（DNSMax/HTTP OOB）
			"error_based_data",   // 基于错误的数据外带
		},
		RuleOut: []string{
			"no_difference",         // 真/假条件无差异 → 误报
			"static_response_only",  // 响应恒定，无可观测变化
			"waf_blocked_no_bypass", // 被 WAF 阻断且无绕过 → 不可利用
		},
	},
	{
		Category: "command_injection",
		Prove: []string{
			"command_output_echo", // 命令输出回显
			"oast_callback",       // OAST 回调
			"file_written",        // 写入文件可验证存在
			"time_delay_sleep",    // sleep 命令产生可观测延迟
		},
		RuleOut: []string{
			"no_command_output",      // 无命令输出回显/OOB
			"special_chars_stripped", // 特殊字符被剥离 → 注入不可达
		},
	},
	{
		Category: "xxe",
		Prove: []string{
			"oast_callback",     // OAST 回调（外部实体解析触发外联）
			"file_content_echo", // 文件内容回显
			"error_based_data",  // 基于错误的数据外带
		},
		RuleOut: []string{
			"no_external_entity_parse", // 未触发外部实体解析
		},
	},
	{
		Category: "deserialization",
		Prove: []string{
			"oast_callback",    // 反序列化触发 OOB
			"code_execution",   // 反序列化导致代码执行
			"error_stack_leak", // 反序列化错误栈泄漏 gadget 线索
		},
		RuleOut: []string{
			"no_deserialization_error", // 无反序列化相关错误/行为变化
		},
	},
	{
		Category: "path_traversal",
		Prove: []string{
			"file_content_echo",   // 目标文件内容回显
			"oast_callback",       // OOB 外带（盲遍历）
			"file_read_confirmed", // 文件读取确认（hash/特征匹配）
		},
		RuleOut: []string{
			"no_file_content", // 无文件内容回显/OOB
			"generic_404",     // 仅 404，无可区分特征
		},
	},
}

// lookupPlaybook 按 category 大小写不敏感包含匹配查 Playbook 条目。
// 未命中返回 nil（无 PROVE/RULE_OUT 约束的类别走通用 4-axis）。
func lookupPlaybook(category string) *playbookEntry {
	c := strings.ToLower(strings.TrimSpace(category))
	if c == "" {
		return nil
	}
	// 先精确匹配关键字段
	keys := map[string]string{
		"ssrf":                        "ssrf",
		"server-side request forgery": "ssrf",
		"xss":                         "xss",
		"跨站脚本":                        "xss",
		"sqli":                        "sqli",
		"sql注入":                       "sqli",
		"sql injection":               "sqli",
		"command_injection":           "command_injection",
		"命令注入":                        "command_injection",
		"rce":                         "command_injection",
		"xxe":                         "xxe",
		"deserialization":             "deserialization",
		"反序列化":                        "deserialization",
		"path_traversal":              "path_traversal",
		"路径遍历":                        "path_traversal",
		"lfi":                         "path_traversal",
	}
	if k, ok := keys[c]; ok {
		for i := range playbook {
			if playbook[i].Category == k {
				return &playbook[i]
			}
		}
	}
	// 回退到包含匹配
	for i := range playbook {
		if strings.Contains(c, playbook[i].Category) {
			return &playbook[i]
		}
	}
	return nil
}

// allAxesPassed 检查 4-axis 是否全部通过。缺维度视为未过。
func allAxesPassed(axes []AxisCheck) bool {
	want := map[string]bool{AxisReal: false, AxisTriggerable: false, AxisImpactful: false, AxisGeneral: false}
	for _, a := range axes {
		if _, ok := want[a.Name]; ok {
			want[a.Name] = a.Passed
		}
	}
	for _, ok := range want {
		if !ok {
			return false
		}
	}
	return true
}

// matchAny 检查 candidate 的 PlaybookMatch / Indicator 是否命中锚点列表中任意一个。
// 命中判定大小写不敏感包含匹配。
func matchAny(haystack []string, needles ...string) string {
	for _, n := range needles {
		nl := strings.ToLower(strings.TrimSpace(n))
		if nl == "" {
			continue
		}
		for _, h := range haystack {
			if strings.Contains(nl, strings.ToLower(h)) {
				return h
			}
		}
	}
	return ""
}

// Verify 对 candidate finding 过 4-axis 闸门 + Playbook PROVE/RULE_OUT 校验，
// 返回 VerifierResult。fail-closed：任何一轴未过 / 命中 RULE_OUT / 未命中 PROVE
// 锚点（且声明为 reproduced 及以上） → 一律降级为 suspected，Confirmed=false。
//
// 调用方应在落库 record_vulnerability 前调用本函数；Confirmed=true 才可标 confirmed。
func Verify(c CandidateFinding) VerifierResult {
	res := VerifierResult{
		FindingID:  c.ID,
		FinalLevel: c.EvidenceLevel,
		AxisReport: append([]AxisCheck(nil), c.Axes...),
	}

	// 1) RULE_OUT 优先：命中即否决
	if pb := lookupPlaybook(c.Category); pb != nil {
		if hit := matchAny(pb.RuleOut, c.PlaybookMatch, c.Indicator, c.Reproduction); hit != "" {
			res.RuleOutHit = hit
			res.FinalLevel = EvidenceSuspected
			res.Confirmed = false
			res.Reason = fmt.Sprintf("RULE_OUT 命中 %q：判定为误报/不可利用，降级为 suspected", hit)
			return res
		}
	}

	// 2) 4-axis 复核
	axesOK := allAxesPassed(c.Axes)
	if !axesOK {
		var failed []string
		want := map[string]bool{AxisReal: false, AxisTriggerable: false, AxisImpactful: false, AxisGeneral: false}
		for _, a := range c.Axes {
			if _, ok := want[a.Name]; ok {
				want[a.Name] = a.Passed
			}
		}
		for name, ok := range want {
			if !ok {
				failed = append(failed, name)
			}
		}
		res.FinalLevel = EvidenceSuspected
		res.Confirmed = false
		res.Reason = fmt.Sprintf("4-axis 未全过（未过: %s）：降级为 suspected", strings.Join(failed, ", "))
		return res
	}

	// 3) PROVE 锚点校验：声明 reproduced/impact_proven 必须命中至少一个 PROVE 锚点
	if pb := lookupPlaybook(c.Category); pb != nil {
		if c.EvidenceLevel >= EvidenceReproduced {
			proveHit := matchAny(pb.Prove, c.PlaybookMatch, c.Indicator, c.Reproduction)
			if proveHit == "" {
				// 缺 PROVE 锚点 → 降级到 corroborated（多源印证但未主动复现锚点）
				res.FinalLevel = EvidenceCorroborated
				res.Confirmed = false
				res.Reason = "4-axis 全过但声明 reproduced+ 却缺 PROVE 锚点：降级为 corroborated"
				return res
			}
			res.PlaybookMatch = proveHit // 记录命中的 PROVE 锚点
		}
	}

	// 4) evidence_level 下限：confirmed 要求至少 corroborated
	if c.EvidenceLevel < EvidenceCorroborated {
		res.FinalLevel = EvidenceSuspected
		res.Confirmed = false
		res.Reason = "evidence_level 低于 corroborated：降级为 suspected，不报 confirmed"
		return res
	}

	// 全过
	res.Confirmed = true
	res.FinalLevel = c.EvidenceLevel
	if res.PlaybookMatch == "" {
		res.PlaybookMatch = c.PlaybookMatch
	}
	res.Reason = fmt.Sprintf("4-axis 全过 + evidence_level=%s + PROVE 锚点满足：过闸 confirmed", res.FinalLevel.String())
	return res
}

// Classify 把 candidate 的原始证据描述归类为 4-axis 判定 + 证据等级建议。
// 这是个辅助函数：子代理可自行初判，也可调用本函数得到默认初判后再过 Verify。
//
// 输入：
//   - category: 漏洞类别（匹配 Playbook）
//   - indicator: 证据指针文本
//   - reproduction: 复现指令文本
//   - hasOAST: 是否有 OAST 回调 / OOB 命中
//   - hasDataEcho: 是否有数据/输出回显
//   - hasTimeDelay: 是否有时间延迟
//   - generalCount: 同类端点/参数可复现的计数（>=2 视为 general 通过）
//
// 输出 4-axis 判定 + 建议的 evidence_level。本函数不决定 confirmed，confirmed 走 Verify。
func Classify(category, indicator, reproduction string, hasOAST, hasDataEcho, hasTimeDelay bool, generalCount int) ([]AxisCheck, EvidenceLevel) {
	axes := []AxisCheck{
		{Name: AxisReal, Passed: indicator != "", Reason: "存在证据指针"},
		{Name: AxisTriggerable, Passed: reproduction != "" || hasOAST || hasDataEcho || hasTimeDelay, Reason: "存在可触发证据"},
		{Name: AxisImpactful, Passed: hasDataEcho || hasOAST || hasTimeDelay, Reason: "存在安全影响证据"},
		{Name: AxisGeneral, Passed: generalCount >= 2, Reason: fmt.Sprintf("同类复现计数=%d", generalCount)},
	}

	var lvl EvidenceLevel
	switch {
	case hasDataEcho:
		lvl = EvidenceReproduced // 数据回显 = 主动复现
	case hasOAST:
		lvl = EvidenceReproduced // OAST 回调 = 链路打通
	case hasTimeDelay:
		lvl = EvidenceReproduced // 时间延迟 = 可观测行为变化
	case indicator != "":
		lvl = EvidenceCorroborated // 仅单点信号
	default:
		lvl = EvidenceSuspected
	}

	// 4-axis 任一未过 → 下限 suspected
	if !allAxesPassed(axes) {
		if lvl > EvidenceSuspected {
			lvl = EvidenceSuspected
		}
	}

	return axes, lvl
}
