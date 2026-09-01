@echo off
REM CyberStrikeAI 一键启动（双击即可）
REM 通过 PowerShell 调用 start.ps1，自动引导环境并启动后端
setlocal
cd /d "%~dp0..\.."
powershell -NoProfile -ExecutionPolicy Bypass -File "scripts\windows\start.ps1"
endlocal
