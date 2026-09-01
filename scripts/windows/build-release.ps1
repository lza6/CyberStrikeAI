#requires -Version 5.1
<#
.SYNOPSIS
  CyberStrikeAI Windows 一键打包脚本：编译后端 + 准备 runtime + 打 NSIS 安装包
.DESCRIPTION
  1. 用 WinLibs MinGW gcc 编译 cyberstrike-ai.exe（CGO + SQLite）
  2. 准备内嵌 Python 运行时（runtime/python/python-3.13.5）
  3. 用 electron-builder 打 NSIS 安装包到 desktop/dist/
  4. 可选：gh release create 上传到 GitHub
.PARAMETER SkipBuild
  跳过后端编译（已有 cyberstrike-ai.exe 时）
.PARAMETER SkipDesktop
  跳过桌面 NSIS 打包
.PARAMETER UploadGithub
  打包后用 gh CLI 上传到 GitHub Release（需先 gh auth login）
.PARAMETER Tag
  Release tag，默认 v1.7.17
.EXAMPLE
  ./scripts/windows/build-release.ps1
  ./scripts/windows/build-release.ps1 -UploadGithub -Tag v1.7.18
#>
[CmdletBinding()]
param(
    [switch]$SkipBuild,
    [switch]$SkipDesktop,
    [switch]$UploadGithub,
    [string]$Tag = 'v1.7.17'
)

$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$Root = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
Set-Location $Root

function Step($m){ Write-Host "[*] $m" -ForegroundColor Cyan }
function Ok($m){ Write-Host "[+] $m" -ForegroundColor Green }
function Warn($m){ Write-Host "[!] $m" -ForegroundColor Yellow }
function Err($m){ Write-Host "[x] $m" -ForegroundColor Red; exit 1 }

# 1. 检查 Go
Step '检查 Go 环境...'
$go = (Get-Command go -ErrorAction SilentlyContinue)
if (-not $go) { Err '未找到 go，请先安装 Go 1.25+ (https://go.dev/dl/)' }
$goVer = (& go version) 2>&1
Ok "Go: $goVer"

# 2. 检查/定位 gcc（CGO 必需）
Step '检查 gcc（CGO + SQLite 必需）...'
$gcc = (Get-Command gcc -ErrorAction SilentlyContinue)
if (-not $gcc) {
    # 尝试常见 WinLibs 路径
    $candidates = @(
        "$env:USERPROFILE\mingw\extracted\mingw64\bin",
        "C:\mingw64\bin",
        "C:\msys64\mingw64\bin"
    )
    foreach ($p in $candidates) {
        if (Test-Path "$p\gcc.exe") { $env:PATH = "$p;$env:PATH"; $gcc = $true; Ok "使用 $p\gcc.exe"; break }
    }
}
if (-not $gcc) {
    Err '未找到 gcc。请下载 WinLibs MinGW：https://github.com/brechtsanders/winlibs_mingw/releases 解压后把 mingw64\bin 加入 PATH'
}
Ok "gcc: $(gcc --version 2>&1 | Select-Object -First 1)"

# 3. 编译后端
if (-not $SkipBuild) {
    Step '编译 cyberstrike-ai.exe（CGO_ENABLED=1）...'
    $env:GOPROXY = 'https://goproxy.cn,direct'
    $env:CGO_ENABLED = '1'
    $env:CC = 'gcc'
    $env:CXX = 'g++'
    & go build -o cyberstrike-ai.exe cmd/server/main.go
    if ($LASTEXITCODE -ne 0) { Err '后端编译失败' }
    Ok '后端编译完成'
} else {
    Warn '跳过后端编译'
}

# 4. 准备 runtime（首次）
Step '准备内嵌 Python 运行时...'
& (Join-Path $PSScriptRoot 'bootstrap-python.ps1')
Ok 'runtime 就绪'

# 5. 桌面打包
if (-not $SkipDesktop) {
    Step '桌面版 NSIS 打包...'
    Push-Location (Join-Path $Root 'desktop')
    if (-not (Test-Path 'node_modules')) {
        & npm install --no-audit --no-fund
        if ($LASTEXITCODE -ne 0) { Pop-Location; Err 'npm install 失败' }
    }
    & npx electron-builder --win nsis
    if ($LASTEXITCODE -ne 0) { Pop-Location; Err 'electron-builder 打包失败' }
    $installer = Join-Path $Root "desktop\dist\CyberStrikeAI Setup $($Tag -replace '^v','').exe"
    if (Test-Path $installer) {
        $sz = [math]::Round((Get-Item $installer).Length/1MB,1)
        Ok "安装包已生成：$installer ($sz MB)"
    } else {
        Warn "未找到 $installer，请检查 desktop/dist/"
    }
    Pop-Location
}

# 6. 上传 GitHub Release
if ($UploadGithub) {
    Step '上传到 GitHub Release...'
    $gh = (Get-Command gh -ErrorAction SilentlyContinue)
    if (-not $gh) { Warn '未找到 gh CLI，跳过上传；请手动安装 gh 并 gh auth login 后重试' }
    else {
        $installer = Join-Path $Root "desktop\dist\CyberStrikeAI Setup $($Tag -replace '^v','').exe"
        & gh release create $Tag "$installer" --title $Tag --notes "CyberStrikeAI $Tag Windows 桌面版" --generate-notes
        if ($LASTEXITCODE -eq 0) { Ok "Release $Tag 已创建" } else { Warn 'gh release 创建失败，请检查登录状态' }
    }
}

Ok '全部完成'
