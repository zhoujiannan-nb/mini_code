@echo off
setlocal

set APP_NAME=ucode

if "%1"=="linux" goto :build_linux
if "%1"=="mac" goto :build_mac
if "%1"=="all" goto :build_all

echo Building all binaries for Windows...
echo.

set OS=windows

REM Create build directory structure
if not exist "build\%OS%\interactive" mkdir "build\%OS%\interactive"
if not exist "build\%OS%\server" mkdir "build\%OS%\server"
if not exist "build\%OS%\goal" mkdir "build\%OS%\goal"
if not exist "build\%OS%\tui" mkdir "build\%OS%\tui"

REM Build interactive mode (default)
echo [1/4] Building %APP_NAME%.exe (interactive)...
go build -o "build\%OS%\interactive\%APP_NAME%.exe" .
if %errorlevel% neq 0 (
    echo [ERROR] Build %APP_NAME%.exe failed.
    exit /b 1
)
echo     Success: build\%OS%\interactive\%APP_NAME%.exe

REM Build server mode
echo [2/4] Building %APP_NAME%-server.exe (server)...
go build -o "build\%OS%\server\%APP_NAME%-server.exe" ./cmd/server
if %errorlevel% neq 0 (
    echo [ERROR] Build %APP_NAME%-server.exe failed.
    exit /b 1
)
echo     Success: build\%OS%\server\%APP_NAME%-server.exe

REM Build goal mode
echo [3/4] Building %APP_NAME%-goal.exe (goal)...
go build -o "build\%OS%\goal\%APP_NAME%-goal.exe" ./cmd/goal
if %errorlevel% neq 0 (
    echo [ERROR] Build %APP_NAME%-goal.exe failed.
    exit /b 1
)
echo     Success: build\%OS%\goal\%APP_NAME%-goal.exe

REM Build TUI mode
echo [4/4] Building %APP_NAME%-tui.exe (tui)...
go build -o "build\%OS%\tui\%APP_NAME%-tui.exe" ./ui
if %errorlevel% neq 0 (
    echo [ERROR] Build %APP_NAME%-tui.exe failed.
    exit /b 1
)
echo     Success: build\%OS%\tui\%APP_NAME%-tui.exe

echo.
echo Build completed successfully!
goto :end

:build_all
echo Building all binaries for all platforms...
echo.

REM Build Windows
echo [1/3] Building for Windows...
set GOOS=windows
set GOARCH=amd64
set OS=windows

REM Create build directory structure
if not exist "build\%OS%\interactive" mkdir "build\%OS%\interactive"
if not exist "build\%OS%\server" mkdir "build\%OS%\server"
if not exist "build\%OS%\goal" mkdir "build\%OS%\goal"
if not exist "build\%OS%\tui" mkdir "build\%OS%\tui"

REM Build interactive mode
echo [1/4] Building %APP_NAME%.exe (interactive)...
go build -o "build\%OS%\interactive\%APP_NAME%.exe" .
if %errorlevel% neq 0 (
    echo [ERROR] Build %APP_NAME%.exe failed.
    exit /b 1
)
echo     Success: build\%OS%\interactive\%APP_NAME%.exe

REM Build server mode
echo [2/4] Building %APP_NAME%-server.exe (server)...
go build -o "build\%OS%\server\%APP_NAME%-server.exe" ./cmd/server
if %errorlevel% neq 0 (
    echo [ERROR] Build %APP_NAME%-server.exe failed.
    exit /b 1
)
echo     Success: build\%OS%\server\%APP_NAME%-server.exe

REM Build goal mode
echo [3/4] Building %APP_NAME%-goal.exe (goal)...
go build -o "build\%OS%\goal\%APP_NAME%-goal.exe" ./cmd/goal
if %errorlevel% neq 0 (
    echo [ERROR] Build %APP_NAME%-goal.exe failed.
    exit /b 1
)
echo     Success: build\%OS%\goal\%APP_NAME%-goal.exe

REM Build TUI mode
echo [4/4] Building %APP_NAME%-tui.exe (tui)...
go build -o "build\%OS%\tui\%APP_NAME%-tui.exe" ./ui
if %errorlevel% neq 0 (
    echo [ERROR] Build %APP_NAME%-tui.exe failed.
    exit /b 1
)
echo     Success: build\%OS%\tui\%APP_NAME%-tui.exe

REM Build Linux
echo [2/3] Building for Linux...
set GOOS=linux
set GOARCH=amd64
set OS=linux

REM Create build directory structure
if not exist "build\%OS%\interactive" mkdir "build\%OS%\interactive"
if not exist "build\%OS%\server" mkdir "build\%OS%\server"
if not exist "build\%OS%\goal" mkdir "build\%OS%\goal"
if not exist "build\%OS%\tui" mkdir "build\%OS%\tui"

REM Build interactive mode
echo [1/4] Building %APP_NAME% (interactive)...
go build -o "build\%OS%\interactive\%APP_NAME%" .
if %errorlevel% neq 0 (
    echo [ERROR] Build %APP_NAME% failed.
    exit /b 1
)
echo     Success: build\%OS%\interactive\%APP_NAME%

REM Build server mode
echo [2/4] Building %APP_NAME%-server (server)...
go build -o "build\%OS%\server\%APP_NAME%-server" ./cmd/server
if %errorlevel% neq 0 (
    echo [ERROR] Build %APP_NAME%-server failed.
    exit /b 1
)
echo     Success: build\%OS%\server\%APP_NAME%-server

REM Build goal mode
echo [3/4] Building %APP_NAME%-goal (goal)...
go build -o "build\%OS%\goal\%APP_NAME%-goal" ./cmd/goal
if %errorlevel% neq 0 (
    echo [ERROR] Build %APP_NAME%-goal failed.
    exit /b 1
)
echo     Success: build\%OS%\goal\%APP_NAME%-goal

REM Build TUI mode
echo [4/4] Building %APP_NAME%-tui (tui)...
go build -o "build\%OS%\tui\%APP_NAME%-tui" ./ui
if %errorlevel% neq 0 (
    echo [ERROR] Build %APP_NAME%-tui failed.
    exit /b 1
)
echo     Success: build\%OS%\tui\%APP_NAME%-tui

REM Build macOS arm64
echo [3/3] Building for macOS arm64 (Apple Silicon)...
set GOOS=darwin
set GOARCH=arm64
set OS=darwin-arm64

REM Create build directory structure
if not exist "build\%OS%\interactive" mkdir "build\%OS%\interactive"
if not exist "build\%OS%\server" mkdir "build\%OS%\server"
if not exist "build\%OS%\goal" mkdir "build\%OS%\goal"
if not exist "build\%OS%\tui" mkdir "build\%OS%\tui"

REM Build interactive mode
echo [1/4] Building %APP_NAME% (interactive)...
go build -o "build\%OS%\interactive\%APP_NAME%" .
if %errorlevel% neq 0 (
    echo [ERROR] Build %APP_NAME% failed.
    exit /b 1
)
echo     Success: build\%OS%\interactive\%APP_NAME%

REM Build server mode
echo [2/4] Building %APP_NAME%-server (server)...
go build -o "build\%OS%\server\%APP_NAME%-server" ./cmd/server
if %errorlevel% neq 0 (
    echo [ERROR] Build %APP_NAME%-server failed.
    exit /b 1
)
echo     Success: build\%OS%\server\%APP_NAME%-server

REM Build goal mode
echo [3/4] Building %APP_NAME%-goal (goal)...
go build -o "build\%OS%\goal\%APP_NAME%-goal" ./cmd/goal
if %errorlevel% neq 0 (
    echo [ERROR] Build %APP_NAME%-goal failed.
    exit /b 1
)
echo     Success: build\%OS%\goal\%APP_NAME%-goal

REM Build TUI mode
echo [4/4] Building %APP_NAME%-tui (tui)...
go build -o "build\%OS%\tui\%APP_NAME%-tui" ./ui
if %errorlevel% neq 0 (
    echo [ERROR] Build %APP_NAME%-tui failed.
    exit /b 1
)
echo     Success: build\%OS%\tui\%APP_NAME%-tui

echo.
echo Build completed successfully!
goto :end

:build_linux
echo Building %APP_NAME% for linux/amd64...
set GOOS=linux
set GOARCH=amd64
set OS=linux

REM Create build directory structure
if not exist "build\%OS%\interactive" mkdir "build\%OS%\interactive"
if not exist "build\%OS%\server" mkdir "build\%OS%\server"
if not exist "build\%OS%\goal" mkdir "build\%OS%\goal"
if not exist "build\%OS%\tui" mkdir "build\%OS%\tui"

REM Build interactive mode
echo [1/4] Building %APP_NAME% (interactive)...
go build -o "build\%OS%\interactive\%APP_NAME%" .
if %errorlevel% neq 0 (
    echo [ERROR] Build %APP_NAME% failed.
    exit /b 1
)
echo     Success: build\%OS%\interactive\%APP_NAME%

REM Build server mode
echo [2/4] Building %APP_NAME%-server (server)...
go build -o "build\%OS%\server\%APP_NAME%-server" ./cmd/server
if %errorlevel% neq 0 (
    echo [ERROR] Build %APP_NAME%-server failed.
    exit /b 1
)
echo     Success: build\%OS%\server\%APP_NAME%-server

REM Build goal mode
echo [3/4] Building %APP_NAME%-goal (goal)...
go build -o "build\%OS%\goal\%APP_NAME%-goal" ./cmd/goal
if %errorlevel% neq 0 (
    echo [ERROR] Build %APP_NAME%-goal failed.
    exit /b 1
)
echo     Success: build\%OS%\goal\%APP_NAME%-goal

REM Build TUI mode
echo [4/4] Building %APP_NAME%-tui (tui)...
go build -o "build\%OS%\tui\%APP_NAME%-tui" ./ui
if %errorlevel% neq 0 (
    echo [ERROR] Build %APP_NAME%-tui failed.
    exit /b 1
)
echo     Success: build\%OS%\tui\%APP_NAME%-tui

echo.
echo Build completed successfully!

:build_mac
echo Building %APP_NAME% for macOS (arm64 + amd64)...
echo.

REM Build for arm64 (Apple Silicon)
echo [1/2] Building for darwin/arm64 (Apple Silicon)...
set GOOS=darwin
set GOARCH=arm64
set OS=darwin-arm64

REM Create build directory structure
if not exist "build\%OS%\interactive" mkdir "build\%OS%\interactive"
if not exist "build\%OS%\server" mkdir "build\%OS%\server"
if not exist "build\%OS%\goal" mkdir "build\%OS%\goal"
if not exist "build\%OS%\tui" mkdir "build\%OS%\tui"

REM Build interactive mode
echo [1/4] Building %APP_NAME% (interactive)...
go build -o "build\%OS%\interactive\%APP_NAME%" .
if %errorlevel% neq 0 (
    echo [ERROR] Build %APP_NAME% failed.
    exit /b 1
)
echo     Success: build\%OS%\interactive\%APP_NAME%

REM Build server mode
echo [2/4] Building %APP_NAME%-server (server)...
go build -o "build\%OS%\server\%APP_NAME%-server" ./cmd/server
if %errorlevel% neq 0 (
    echo [ERROR] Build %APP_NAME%-server failed.
    exit /b 1
)
echo     Success: build\%OS%\server\%APP_NAME%-server

REM Build goal mode
echo [3/4] Building %APP_NAME%-goal (goal)...
go build -o "build\%OS%\goal\%APP_NAME%-goal" ./cmd/goal
if %errorlevel% neq 0 (
    echo [ERROR] Build %APP_NAME%-goal failed.
    exit /b 1
)
echo     Success: build\%OS%\goal\%APP_NAME%-goal

REM Build TUI mode
echo [4/4] Building %APP_NAME%-tui (tui)...
go build -o "build\%OS%\tui\%APP_NAME%-tui" ./ui
if %errorlevel% neq 0 (
    echo [ERROR] Build %APP_NAME%-tui failed.
    exit /b 1
)
echo     Success: build\%OS%\tui\%APP_NAME%-tui

REM Build for amd64 (Intel)
echo [2/2] Building for darwin/amd64 (Intel)...
set GOOS=darwin
set GOARCH=amd64
set OS=darwin-amd64

REM Create build directory structure
if not exist "build\%OS%\interactive" mkdir "build\%OS%\interactive"
if not exist "build\%OS%\server" mkdir "build\%OS%\server"
if not exist "build\%OS%\goal" mkdir "build\%OS%\goal"
if not exist "build\%OS%\tui" mkdir "build\%OS%\tui"

REM Build interactive mode
echo [1/4] Building %APP_NAME% (interactive)...
go build -o "build\%OS%\interactive\%APP_NAME%" .
if %errorlevel% neq 0 (
    echo [ERROR] Build %APP_NAME% failed.
    exit /b 1
)
echo     Success: build\%OS%\interactive\%APP_NAME%

REM Build server mode
echo [2/4] Building %APP_NAME%-server (server)...
go build -o "build\%OS%\server\%APP_NAME%-server" ./cmd/server
if %errorlevel% neq 0 (
    echo [ERROR] Build %APP_NAME%-server failed.
    exit /b 1
)
echo     Success: build\%OS%\server\%APP_NAME%-server

REM Build goal mode
echo [3/4] Building %APP_NAME%-goal (goal)...
go build -o "build\%OS%\goal\%APP_NAME%-goal" ./cmd/goal
if %errorlevel% neq 0 (
    echo [ERROR] Build %APP_NAME%-goal failed.
    exit /b 1
)
echo     Success: build\%OS%\goal\%APP_NAME%-goal

REM Build TUI mode
echo [4/4] Building %APP_NAME%-tui (tui)...
go build -o "build\%OS%\tui\%APP_NAME%-tui" ./ui
if %errorlevel% neq 0 (
    echo [ERROR] Build %APP_NAME%-tui failed.
    exit /b 1
)
echo     Success: build\%OS%\tui\%APP_NAME%-tui

echo.
echo Build completed successfully!

goto :end

:end
endlocal
