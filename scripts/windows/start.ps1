# CyberStrikeAI Windows 启动器（开箱即用，双击 start.bat 即可）
# 负责：检查/引导 Python 运行时 -> 生成 config.yaml -> 启动后端 -> 捕获首启 admin 密码 -> 打开浏览器
# 兼容 Windows PowerShell 5.1+ 与 PowerShell 7+

$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8

# 切换到脚本所在目录（项目根）
$Root = Split-Path -Parent $PSScriptRoot        # scripts/windows -> scripts -> 项目根
$Root = Split-Path -Parent $Root
Set-Location $Root

$Exe        = Join-Path $Root 'cyberstrike-ai.exe'
$PyDir      = Join-Path $Root 'runtime\python\python-3.13.5'
$PyExe      = Join-Path $PyDir 'python.exe'
$Cfg        = Join-Path $Root 'config.yaml'
$CfgExample = Join-Path $Root 'config.example.yaml'
$DataDir    = Join-Path $Root 'data'
$LogDir     = Join-Path $DataDir 'logs'
$ServerLog  = Join-Path $LogDir 'server.log'
$ServerError= Join-Path $LogDir 'server.err'

function Write-Step($msg) { Write-Host "[*] $msg" -ForegroundColor Cyan }
function Write-Ok($msg)   { Write-Host "[+] $msg" -ForegroundColor Green }
function Write-Warn2($msg){ Write-Host "[!] $msg" -ForegroundColor Yellow }
function Write-Err($msg) { Write-Host "[x] $msg" -ForegroundColor Red }

# 1. 检查后端二进制
if (-not (Test-Path $Exe)) {
    Write-Err "未找到 cyberstrike-ai.exe"
    Write-Host "    请先编译：go build -o cyberstrike-ai.exe cmd\server\main.go"
    Write-Host "    或从 GitHub Release 下载已编译的版本覆盖本目录。"
    Read-Host "按回车退出"
    exit 1
}

# 2. 确保数据/日志目录
New-Item -ItemType Directory -Force -Path $DataDir | Out-Null
New-Item -ItemType Directory -Force -Path $LogDir  | Out-Null

# 3. 引导内嵌 Python（缺失时自动下载安装依赖）
if (-not (Test-Path $PyExe)) {
    Write-Step "首次运行：准备内嵌 Python 运行时..."
    & (Join-Path $PSScriptRoot 'bootstrap-python.ps1')
    if ($LASTEXITCODE -ne 0) {
        Write-Warn2 "内嵌 Python 准备失败，部分 Python 工具（api-fuzzer 等）将不可用，主服务仍可启动。"
    }
}

# 4. 生成 config.yaml（首次）
if (-not (Test-Path $Cfg)) {
    if (Test-Path $CfgExample) {
        Copy-Item $CfgExample $Cfg
        Write-Ok "已从 config.example.yaml 生成 config.yaml"
    } else {
        Write-Err "缺少 config.example.yaml，无法生成配置"
        Read-Host "按回车退出"
        exit 1
    }
}

# 5. 若 8080 已被占用，尝试清理旧进程（仅清理本程序实例）
$portBusy = (Get-NetTCPConnection -LocalPort 8080 -State Listen -ErrorAction SilentlyContinue)
if ($portBusy) {
    Write-Warn2 "端口 8080 已被占用，尝试结束旧实例..."
    foreach ($c in $portBusy) {
        try { Stop-Process -Id $c.OwningProcess -Force -ErrorAction SilentlyContinue } catch {}
    }
    Start-Sleep -Milliseconds 800
}

# 6. 构造子进程环境（内嵌 Python 优先）
$env:PATH = "$PyDir;$PyDir\Scripts;" + $env:PATH
# 默认走明文 HTTP，避免小白被自签证书警告劝退
$env:CYBERSTRIKE_HTTPS = '0'

Write-Step "启动 CyberStrikeAI 后端（HTTP）..."
if (Test-Path $ServerLog) { Clear-Content $ServerLog -ErrorAction SilentlyContinue }
if (Test-Path $ServerError) { Clear-Content $ServerError -ErrorAction SilentlyContinue }

$proc = Start-Process -FilePath $Exe `
    -ArgumentList '-config',$Cfg,'--http' `
    -RedirectStandardOutput $ServerLog `
    -RedirectStandardError  $ServerError `
    -WindowStyle Hidden -PassThru

# 7. 轮询日志，等待 ONLINE，并捕获首次 admin 密码
$online = $false
$adminPwd = $null
$deadline = (Get-Date).AddSeconds(40)
while ((-not $online) -and (Get-Date) -lt $deadline) {
    Start-Sleep -Milliseconds 400
    if (Test-Path $ServerLog) {
        $lines = Get-Content $ServerLog -ErrorAction SilentlyContinue
        foreach ($l in $lines) {
            if ($l -match 'ONLINE') { $online = $true }
            if ($null -eq $adminPwd -and $l -match 'Password\s+(.+)') {
                $adminPwd = $matches[1].Trim()
            }
        }
    }
    if ($proc.HasExited) {
        Write-Err "后端进程已退出（exit $($proc.ExitCode)），日志："
        if (Test-Path $ServerError) { Get-Content $ServerError -Tail 30 | ForEach-Object { Write-Host "    $_" } }
        Read-Host "按回车退出"
        exit 1
    }
}

if (-not $online) {
    Write-Warn2 "40 秒内未检测到 ONLINE，可能仍在启动；请查看 $ServerLog"
}

# 8. 打印首启 admin 密码
Write-Host ""
Write-Host "============================================" -ForegroundColor Green
Write-Host "  CyberStrikeAI 已启动" -ForegroundColor Green
Write-Host "  访问地址: http://127.0.0.1:8080/" -ForegroundColor Green
if ($adminPwd) {
    Write-Host "  首次登录  admin / $adminPwd" -ForegroundColor Yellow
    Write-Host "  (仅本次显示，登录后请立即在「系统设置」修改)" -ForegroundColor Yellow
} else {
    Write-Host "  登录账号 admin（密码为此前已设置的密码）" -ForegroundColor Green
}
Write-Host "============================================" -ForegroundColor Green
Write-Host ""
Write-Host "  首次使用前，请在「系统设置 -> 基本设置 -> AI 通道配置」" -ForegroundColor Cyan
Write-Host "  填写你的 LLM API Key（OpenAI / DeepSeek / 通义 等）。" -ForegroundColor Cyan
Write-Host ""

# 9. 打开浏览器
Start-Process 'http://127.0.0.1:8080/'

Write-Host "后端正在后台运行（PID $($proc.Id)）。关闭本窗口不会停止服务。"
Write-Host "如需停止：执行 stop.bat，或在任务管理器结束 cyberstrike-ai.exe。"
Write-Host ""
Read-Host "按回车关闭本窗口（后端继续后台运行）"
