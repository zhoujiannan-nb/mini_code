@echo off
cd /d D:\project\mini_code
start "" build\windows\mini_code.exe -p 7531
timeout /t 12 /nobreak >nul
echo --- healthz:
curl -s -m 5 http://127.0.0.1:7531/api/healthz
echo.
echo --- process:
tasklist /fi "imagename eq mini_code.exe" /fo csv | findstr mini_code
taskkill /f /im mini_code.exe >nul 2>&1
echo --- killed
