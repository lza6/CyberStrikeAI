# 从 Release 包恢复运行时目录（小白拿到 Release 解压后双击 start.bat 前不需要这步）
# 本脚本用于：在源码仓库中重新打包 Release 前清理临时数据

$ErrorActionPreference = 'SilentlyContinue'
$Root = Split-Path -Parent $PSScriptRoot
Set-Location $Root

Write-Host "[*] 清理 build/cache..."
Remove-Item -Recurse -Force "tmp" -ErrorAction SilentlyContinue
Write-Host "[+] done"
