# CyberStrikeAI Windows 桌面版打包

把 CyberStrikeAI 封装成一个原生 Windows GUI 程序（`CyberStrikeAI.exe`），小白双击即可使用，无需配置 Go/Python/GCC。

## 前置
- 已编译 `cyberstrike-ai.exe`（项目根，CGO + SQLite）
- 已就绪 `runtime/python/python-3.13.5/`（内嵌 Python + 依赖）
- Node.js 20+（仅打包时需要，用户不需要）

## 打包
```bat
cd desktop
call npm install
call npm run dist
```
产物：`desktop/dist/CyberStrikeAI Setup 1.7.17.exe`（NSIS 安装包）。

## 安装包内容
- Electron 壳（BrowserWindow 指向本地后端）
- `cyberstrike-ai.exe` + `runtime/` + `tools/` + `skills/` + `agents/` + `roles/` + `web/` + `knowledge_base/`
- `start.bat` / `stop.bat`（命令行备用入口）

## 运行
安装后双击桌面快捷方式「CyberStrikeAI」：
1. 主进程拉起 `cyberstrike-ai.exe --http`
2. 等待 ONLINE（默认 8080）
3. 打开内嵌窗口加载 `http://127.0.0.1:8080/`
4. 首启控制台日志含 admin 初始密码；桌面版通过日志文件 `data/logs/server.log` 暴露

## 关闭
关闭窗口即退出主进程并 `taskkill cyberstrike-ai.exe`。
