# CyberStrikeAI Windows 桌面版（小白开箱即用）

CyberStrikeAI 现已提供 **Windows 桌面安装包**，内置全部运行依赖，小白双击即可使用，无需手动安装 Go / Python / GCC。

## 下载

前往 [Releases](https://github.com/lza6/CyberStrikeAI/releases/latest)，下载：

- `CyberStrikeAI-Setup-1.7.17.exe`（约 157 MB，Windows x64）

## 安装与使用

1. 双击 `CyberStrikeAI-Setup-1.7.17.exe`，选择安装目录（默认 `%LOCALAPPDATA%\Programs\CyberStrikeAI`）。
2. 安装完成会自动创建桌面快捷方式「CyberStrikeAI」。
3. 双击快捷方式启动：
   - 后端 `cyberstrike-ai.exe` 自动在后台启动（HTTP，端口 8080）
   - Electron 窗口自动打开并加载 `http://127.0.0.1:8080/`
4. **首次启动**：自动生成 `admin` 初始密码，记录在安装目录下 `data/logs/server.log`（搜 `Password`）。
5. **配置 AI 通道**（必需）：登录后进入「系统设置 → 基本设置 → AI 通道配置」，填入你的 LLM API Key（OpenAI / DeepSeek / 通义千问等兼容 OpenAI 协议的服务）。未配置 AI 通道时对话会报 401。

## 内置内容

| 组件 | 说明 |
|------|------|
| Electron 桌面壳 | BrowserWindow 指向本地后端，关闭窗口即停止服务 |
| `cyberstrike-ai.exe` | Go + CGO + SQLite 后端，预编译为 Windows x64 |
| 内嵌 Python 3.13.5 | embeddable + pip + `requirements.txt` 依赖，供 `api-fuzzer`/`dnslog`/`http-framework-test` 等工具调用 |
| 工具/Skills/Agents/Roles | 100+ YAML 工具配方、Skills、Agents、Roles、知识库 |

## 命令行备用入口

安装目录下也提供：
- `start.bat` — 引导环境 + 启动后端 + 开浏览器（捕获首启 admin 密码到终端）
- `stop.bat` — 停止后台后端进程

## 从源码自行打包

```powershell
# 需 Go 1.25+ 与 WinLibs MinGW（gcc）
./scripts/windows/build-release.ps1                 # 编译 + runtime + NSIS
./scripts/windows/build-release.ps1 -UploadGithub  # 同时上传到 GitHub Release
```

详见 [`scripts/windows/`](../scripts/windows/) 与 [`desktop/`](../desktop/)。

## 安全声明

> 仅可对自有系统或已获得明确授权的目标使用 CyberStrikeAI。
> WebShell、C2 等高风险能力仅限自有/授权测试环境。详见 [SECURITY.md](../SECURITY.md)。

## 关闭与卸载

- 关闭 Electron 窗口即退出主进程并 `taskkill cyberstrike-ai.exe`。
- 卸载从「设置 → 应用」或安装目录的卸载器执行；用户数据 `data/` 默认保留在安装目录，可手动删除。
