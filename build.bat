@echo off
setlocal

set APP_NAME=mini_code

if "%1"=="windows" goto :build_windows
if "%1"=="linux" goto :build_linux
if "%1"=="mac" goto :build_mac

REM Default: build the single binary for every supported platform.
echo Building %APP_NAME% for all platforms...
echo.

:build_all
REM ---------------- windows/amd64 ----------------
echo [1/3] Building %APP_NAME%.exe (windows/amd64)...
set GOOS=windows
set GOARCH=amd64
if not exist "build\windows" mkdir "build\windows"
go build -ldflags="-s -w" -o "build\windows\%APP_NAME%.exe" .
if %errorlevel% neq 0 (
    echo [ERROR] Build %APP_NAME%.exe failed.
    exit /b 1
)
echo     Success: build\windows\%APP_NAME%.exe

REM ---------------- linux/amd64 ----------------
echo [2/3] Building %APP_NAME% (linux/amd64)...
set GOOS=linux
set GOARCH=amd64
if not exist "build\linux" mkdir "build\linux"
go build -ldflags="-s -w" -o "build\linux\%APP_NAME%" .
if %errorlevel% neq 0 (
    echo [ERROR] Build %APP_NAME% failed.
    exit /b 1
)
echo     Success: build\linux\%APP_NAME%

REM ---------------- darwin/arm64 (Apple Silicon) ----------------
echo [3/3] Building %APP_NAME% (darwin/arm64)...
set GOOS=darwin
set GOARCH=arm64
if not exist "build\darwin-arm64" mkdir "build\darwin-arm64"
go build -ldflags="-s -w" -o "build\darwin-arm64\%APP_NAME%" .
if %errorlevel% neq 0 (
    echo [ERROR] Build %APP_NAME% failed.
    exit /b 1
)
echo     Success: build\darwin-arm64\%APP_NAME%

echo.
echo Build completed successfully!
echo   build\windows\%APP_NAME%.exe
echo   build\linux\%APP_NAME%
echo   build\darwin-arm64\%APP_NAME%
echo.
echo Run: double-click %APP_NAME%.exe (or ./%APP_NAME% on linux) and open http://127.0.0.1:7500
goto :end

:build_windows
echo Building %APP_NAME% for windows/amd64...
set GOOS=windows
set GOARCH=amd64
if not exist "build\windows" mkdir "build\windows"
go build -ldflags="-s -w" -o "build\windows\%APP_NAME%.exe" .
if %errorlevel% neq 0 (
    echo [ERROR] Build %APP_NAME%.exe failed.
    exit /b 1
)
echo     Success: build\windows\%APP_NAME%.exe
goto :end

:build_linux
echo Building %APP_NAME% for linux/amd64...
set GOOS=linux
set GOARCH=amd64
if not exist "build\linux" mkdir "build\linux"
go build -ldflags="-s -w" -o "build\linux\%APP_NAME%" .
if %errorlevel% neq 0 (
    echo [ERROR] Build %APP_NAME% failed.
    exit /b 1
)
echo     Success: build\linux\%APP_NAME%
goto :end

:build_mac
echo Building %APP_NAME% for darwin/arm64 (Apple Silicon)...
set GOOS=darwin
set GOARCH=arm64
if not exist "build\darwin-arm64" mkdir "build\darwin-arm64"
go build -ldflags="-s -w" -o "build\darwin-arm64\%APP_NAME%" .
if %errorlevel% neq 0 (
    echo [ERROR] Build %APP_NAME% failed.
    exit /b 1
)
echo     Success: build\darwin-arm64\%APP_NAME%
goto :end

:end
endlocal
