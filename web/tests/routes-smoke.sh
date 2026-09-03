#!/usr/bin/env bash
# CyberStrikeAI A5 路由注册真实 E2E 验收（local_mode 免鉴权）
# 运行：bash web/tests/routes-smoke.sh [host:port]
# 依赖：curl, node（可选，用于限流并发压测）
# 不依赖真实 DB 写入；全部 GET 只读 + 安全头 + 限流验证。
# 顺序：路由+安全头先跑，限流压测放最后（会撑爆限流窗口污染后续请求）。
set -uo pipefail
BASE="http://${1:-127.0.0.1:8080}"

PASS=0; FAIL=0; SKIP=0
declare -a RESULTS=()

check() {
  local method="$1" path="$2" expect="$3" label="$4"
  local code
  code=$(curl -s -o /dev/null -w "%{http_code}" --max-time 6 -X "$method" "${BASE}${path}" 2>/dev/null)
  if [[ "$code" == "$expect" ]]; then
    RESULTS+=("  PASS  ${method} ${path} → ${code}  # ${label}"); PASS=$((PASS+1))
  else
    RESULTS+=("  FAIL  ${method} ${path} → ${code} (期望 ${expect})  # ${label}"); FAIL=$((FAIL+1))
  fi
}

echo "=== CyberStrikeAI A5 路由注册 E2E（base=${BASE}）==="

# 1. 静态/文档路由（公开）
check GET "/" "200" "首页 index.html"
check GET "/api-docs" "200" "API 文档页"
check GET "/static/css/style.css" "200" "静态 CSS"
check GET "/static/js/chat.js" "200" "静态 JS(chat)"
check GET "/static/dist/manifest.json" "200" "F1 构建产物 manifest"

# 2. local_mode 免鉴权 GET 路由（只读列表类）
check GET "/api/auth/validate" "200" "鉴权校验(local_mode 免登录)"
check GET "/api/rbac/me" "200" "当前会话身份"
check GET "/api/rbac/metadata" "200" "RBAC 元数据"
check GET "/api/assets" "200" "资产列表"
check GET "/api/assets/stats" "200" "资产统计"
check GET "/api/conversations" "200" "会话列表"
check GET "/api/projects" "200" "项目列表"
check GET "/api/projects/dashboard-summary" "200" "项目仪表盘摘要"
check GET "/api/skills" "200" "技能列表"
check GET "/api/skills/stats" "200" "技能统计"
check GET "/api/system-prompts" "200" "系统提示词列表"
check GET "/api/system-prompts/current" "200" "当前系统提示词"
check GET "/api/external-mcp" "200" "外部 MCP 列表"
check GET "/api/external-mcp/stats" "200" "外部 MCP 统计"
check GET "/api/playbooks" "200" "剧本列表"
check GET "/api/workflows" "200" "工作流列表"
check GET "/api/roles" "200" "角色列表"
check GET "/api/monitor" "200" "监控数据"
check GET "/api/monitor/stats" "200" "监控统计"
check GET "/api/audit/meta" "200" "审计元数据"
check GET "/api/audit/summary" "200" "审计摘要"
check GET "/api/multi-agent/markdown-agents" "200" "Markdown agent 列表"
check GET "/api/config" "200" "配置读取"
check GET "/api/config/tools" "200" "工具配置"
check GET "/api/usage/tokens" "200" "token 用量"
check GET "/api/notifications/summary" "200" "通知摘要"
check GET "/api/vulnerabilities" "200" "漏洞列表"
check GET "/api/vulnerabilities/stats" "200" "漏洞统计"
check GET "/api/blackboard/findings" "200" "blackboard findings"
check GET "/api/webshell/connections" "200" "webshell 连接列表"
check GET "/api/update/check" "200" "更新检查"
check GET "/metrics" "200" "Prometheus 指标"

# 3. 不存在路由 → 404
check GET "/api/nonexistent-route-xyz" "404" "不存在路由应 404"

# 4. 安全头验证（HTTP 模式，HSTS 合理缺失）
echo ""
echo "=== 安全头验证（curl -I /）==="
HEADERS=$(curl -s -I --max-time 6 "${BASE}/" 2>/dev/null)
check_header() {
  local name="$1"
  if echo "$HEADERS" | grep -qi "^${name}:"; then
    echo "  PASS  响应头 ${name}"
    PASS=$((PASS+1))
  else
    echo "  FAIL  缺失响应头 ${name}"; FAIL=$((FAIL+1))
  fi
}
check_header "Content-Security-Policy"
check_header "X-Content-Type-Options"
check_header "X-Frame-Options"
check_header "Referrer-Policy"
check_header "Permissions-Policy"
echo "  (HSTS 仅 HTTPS 时出现；当前 HTTP 模式合理缺失)"

echo ""
echo "=== 结果汇总 ==="
for r in "${RESULTS[@]}"; do echo "$r"; done
echo ""
echo "PASS=${PASS}  FAIL=${FAIL}  SKIP=${SKIP}"
[[ $FAIL -eq 0 ]] && echo "ALL PASS" || echo "HAS FAIL"

# 5. 限流验证放最后（会撑爆限流窗口，跑完后需等待 60s 复位）
echo ""
echo "=== 限流验证（700 并发 GET /api/rbac/metadata，期望部分 429）==="
echo "  注：本步会把 IP 推入限流窗口，跑完后需等待 60s 复位再复测路由。"
if command -v node >/dev/null 2>&1; then
  node -e '
    const http=require("http");const N=700,t={host:"127.0.0.1",port:8080,path:"/api/rbac/metadata",method:"GET"};
    const codes={};let done=0,sent=0;
    function fire(){if(sent>=N)return;sent++;const r=http.request(t,x=>{codes[x.statusCode]=(codes[x.statusCode]||0)+1;x.resume();fin()});r.on("error",()=>{codes.ERR=(codes.ERR||0)+1;fin()});r.end();}
    function fin(){done++;if(done>=N){report()}else if(sent<N)fire();}
    function report(){console.log("  sent="+sent+" 200="+(codes[200]||0)+" 429="+(codes[429]||0)+" other="+JSON.stringify(Object.fromEntries(Object.entries(codes).filter(([k])=>k!="200"&&k!="429"))));
      if((codes[429]||0)>0){console.log("  PASS  限流生效（429 触发）")}else{console.log("  FAIL  限流未生效（无 429）")}}
    for(let i=0;i<50&&i<N;i++)fire();
  ' 2>/dev/null
else
  echo "  SKIP  node 不可用，跳过限流压测"; SKIP=$((SKIP+1))
fi

exit $FAIL
