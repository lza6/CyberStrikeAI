# CyberStrikeAI Windows Desktop (zero-setup)

CyberStrikeAI ships a **Windows desktop installer** that bundles every runtime dependency, so non-technical users can double-click and use it — no Go / Python / GCC install required.

## Download

Go to [Releases](https://github.com/lza6/CyberStrikeAI/releases/latest) and download:

- `CyberStrikeAI-Setup-1.7.17.exe` (~157 MB, Windows x64)

## Install & run

1. Double-click `CyberStrikeAI-Setup-1.7.17.exe`, pick an install dir.
2. A desktop shortcut `CyberStrikeAI` is created.
3. Double-click the shortcut:
   - Backend `cyberstrike-ai.exe` starts in the background (HTTP, port 8080)
   - An Electron window opens and loads `http://127.0.0.1:8080/`
4. **First run**: an `admin` password is auto-generated and written to `data/logs/server.log` (grep `Password`) inside the install dir.
5. **Configure an AI channel** (required): sign in, go to *System Settings → Basic → AI Channels*, add an OpenAI-compatible LLM API key. Without it, chat returns 401.

## Bundled contents

| Component | Notes |
|-----------|-------|
| Electron shell | BrowserWindow pointed at the local backend; closing the window stops the service |
| `cyberstrike-ai.exe` | Go + CGO + SQLite backend, prebuilt for Windows x64 |
| Embedded Python 3.13.5 | embeddable + pip + `requirements.txt` deps, used by `api-fuzzer`/`dnslog`/`http-framework-test` etc. |
| Tools/Skills/Agents/Roles | 100+ YAML tool recipes, Skills, Agents, Roles, knowledge base |

## Command-line fallback

The install dir also ships:
- `start.bat` — bootstraps env + starts backend + opens browser (prints the first-run admin password to the terminal)
- `stop.bat` — stops the background backend

## Build from source

```powershell
# Requires Go 1.25+ and WinLibs MinGW (gcc)
./scripts/windows/build-release.ps1                 # build + runtime + NSIS
./scripts/windows/build-release.ps1 -UploadGithub  # also upload to GitHub Release
```

See [`scripts/windows/`](../scripts/windows/) and [`desktop/`](../desktop/).

## Security notice

> Only use CyberStrikeAI on systems you own or are explicitly authorized to test.
> WebShell, C2 and other high-risk capabilities are for owned/authorized test envs only. See [SECURITY.md](../SECURITY.md).
