# CyberStrikeAI 内嵌 Python 运行时引导脚本
# 在缺失 runtime\python\python-3.13.5\python.exe 时自动：
#   1) 下载 embeddable Python 3.13.5
#   2) 引导 pip + setuptools/wheel
#   3) 安装 requirements.txt 中的依赖
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8

$Root   = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$PyRoot = Join-Path $Root 'runtime\python'
$PyHome = Join-Path $PyRoot 'python-3.13.5'
$PyExe  = Join-Path $PyHome 'python.exe'
$Pth    = Join-Path $PyHome 'python313._pth'
$Req    = Join-Path $Root 'requirements.txt'

$PyVer = '3.13.5'
$EmbedUrl = "https://www.python.org/ftp/python/$PyVer/python-$PyVer-embed-amd64.zip"
$ZipPath  = Join-Path $PyRoot "python-$PyVer-embed-amd64.zip"
$GetPip   = Join-Path $PyRoot 'get-pip.py'

function Write-Step($m){ Write-Host "  [*] $m" -ForegroundColor Cyan }
function Write-Ok($m)  { Write-Host "  [+] $m" -ForegroundColor Green }
function Write-Err2($m) { Write-Host "  [x] $m" -ForegroundColor Red }

# Windows 默认 TLS 需显式启用 TLS1.2，否则下载会失败
try { [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12 } catch {}

New-Item -ItemType Directory -Force -Path $PyRoot | Out-Null

# 1. 下载 embeddable
if (-not (Test-Path $PyExe)) {
    Write-Step "下载 embeddable Python $PyVer ..."
    $ProgressPreference = 'SilentlyContinue'
    Invoke-WebRequest -Uri $EmbedUrl -OutFile $ZipPath -UseBasicParsing
    Expand-Archive -Path $ZipPath -DestinationPath $PyHome -Force
    Remove-Item $ZipPath -Force
    Write-Ok "embeddable Python 已就绪"
}

# 2. 修正 _pth：启用 site + 加入 Lib / site-packages / Scripts
if (-not (Test-Path $Pth) -or -not (Select-String -Path $Pth -Pattern 'import site' -Quiet)) {
    @(
        'python313.zip',
        '.',
        'Lib',
        'Lib\site-packages',
        'Scripts',
        '',
        'import site'
    ) | Set-Content -Path $Pth -Encoding ASCII
}

# 3. 下载 get-pip.py 并引导 pip
if (-not (Test-Path (Join-Path $PyHome 'Scripts\pip.exe'))) {
    if (-not (Test-Path $GetPip)) {
        Write-Step "下载 get-pip.py ..."
        $ProgressPreference = 'SilentlyContinue'
        Invoke-WebRequest -Uri 'https://bootstrap.pypa.io/get-pip.py' -OutFile $GetPip -UseBasicParsing
    }
    Write-Step "引导 pip ..."
    & $PyExe $GetPip --no-warn-script-location *>&1 | Out-Host
    Write-Ok "pip 已安装"
}

# 4. 安装依赖
if (Test-Path $Req) {
    Write-Step "安装 requirements.txt（清华镜像加速）..."
    $env:PIP_INDEX_URL = 'https://pypi.tuna.tsinghua.edu.cn/simple'
    & $PyExe -m pip install --no-warn-script-location -r $Req *>&1 |
        Select-Object -Last 5 | Out-Host
    Write-Ok "Python 依赖安装完成"
} else {
    Write-Err2 "未找到 requirements.txt，跳过依赖安装"
}
