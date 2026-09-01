@echo off
REM 停止后台运行的 CyberStrikeAI 后端
setlocal
echo [*] 正在查找 cyberstrike-ai.exe 进程...
tasklist /FI "IMAGENAME eq cyberstrike-ai.exe" 2>nul | findstr /I "cyberstrike-ai.exe" >nul
if %errorlevel%==0 (
    taskkill /F /IM cyberstrike-ai.exe >nul 2>&1
    echo [+] cyberstrike-ai.exe 已停止
) else (
    echo [!] 未发现正在运行的 cyberstrike-ai.exe
)
endlocal
pause
