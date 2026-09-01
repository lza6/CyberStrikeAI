# ADR-0004 local_mode 免登录模式与安全边界

**状态**：accepted  
**日期**：2026-09

## 背景

桌面版/本地部署场景，用户希望双击即用、不弹登录窗。但 `local_mode: true` 意味着所有 API 以内置 admin 全权限身份执行，若误暴露公网 = 全 API 免登录免 RBAC。需明确安全边界。

## 决策

**`auth.local_mode: true` 时，所有 API 以内置 admin 全权限 Session 执行，但强制服务绑定回环地址（127.0.0.1/localhost/::1），非回环地址自动改绑 127.0.0.1 + Warn 日志。**

- 逃生口：`CYBERSTRIKE_ALLOW_NONLOOPBACK_LOCALMODE=1` 环境变量显式允许非回环（语义明确的白名单变量，而非复用桌面壳的 `CYBERSTRIKE_NO_AUTO_OPEN`——后者任意进程可设，等于解除防护）。
- 桌面壳 `desktop/src/main.js` 设 `CYBERSTRIKE_NO_AUTO_OPEN=1` 抑制后端自动开浏览器（避免双开），不影响绑定防护。

## 备选方案对比

| 方案 | 优点 | 劣势 |
|------|------|------|
| **强制回环 + 专用逃生口（选定）** | 防 public 暴露、桌面壳不受影响、逃生口语义明确 | 局域网授权内网渗透需显式设环境变量 |
| 无防护（local_mode 任意绑定） | 局域网灵活 | 误配 = 全 API 裸奔 |
| 完全禁用 local_mode 公网 | 最安全 | 桌面版无法用 |

## 后果

- **正面**：桌面版双击即免登录进对话页；服务端误配 `host: 0.0.0.0` + `local_mode: true` 时自动改绑回环 + 日志 Warn。
- **负面**：局域网授权内网渗透场景需显式 `CYBERSTRIKE_ALLOW_NONLOOPBACK_LOCALMODE=1`（注释说明用法）。
- **审计**：local_mode Session 的 `UserID: "local-admin"`、`Token: "local-mode"`，审计日志可识别。
- **关闭建议**：生产部署前 `config.yaml` 设 `auth.local_mode: false`，启用 RBAC + 登录。
