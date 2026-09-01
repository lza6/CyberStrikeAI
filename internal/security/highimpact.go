package security

import "strings"

// HighImpactTools 破坏性工具集——执行前需额外审批/确认。
// 移植自 mcpstrike HIGH_IMPACT_TOOLS frozenset 思想：确定性，无 LLM 在环，
// 命中即标记。真正阻断走 HITL 审批流程（已有机制），此处仅作为第二道"标记闸"
// 把 high_impact=true + risk 描述塞进执行结果元数据与审计日志，便于追溯。
var HighImpactTools = map[string]string{
	"exec":         "任意命令执行（含删除/修改文件）",
	"delete-file":  "删除文件",
	"modify-file":  "修改文件内容",
	"create-file":  "创建/覆盖文件",
	"sqlmap":       "SQL 注入自动化利用",
	"metasploit":   "漏洞利用框架",
	"msfvenom":     "payload 生成",
	"hydra":        "暴力破解",
	"hashcat":      "密码哈希破解",
	"john":         "密码破解",
	"ettercap":     "中间人攻击",
	"arpspoof":     "ARP 欺骗",
	"responder":    "LLMNR/NBT-NS 毒化",
	"aircrack-ng":  "无线破解",
	"aireplay-ng":  "无线注入（去认证）",
	"reaver":       "WPS PIN 暴力",
	"bettercap":    "中间人/SNIFF 多合一",
}

// IsHighImpactTool 判断工具是否属于破坏性/高影响集合。
// 命中时返回 risk 描述与 ok=true；未命中返回 ("", false)。
// 大小写不敏感，与 HITL 白名单判定口径一致（见 hitl.shouldInterrupt）。
func IsHighImpactTool(name string) (risk string, ok bool) {
	risk, ok = HighImpactTools[strings.ToLower(strings.TrimSpace(name))]
	return
}
