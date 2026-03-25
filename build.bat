@echo off
setlocal

echo === SQLC Generate ===
sqlc generate
if %ERRORLEVEL% neq 0 (
    echo SQLC generate failed
    exit /b 1
)

echo === Go Build ===
go build -o monitor.exe ./cmd/server/
if %ERRORLEVEL% neq 0 (
    echo Build failed
    exit /b 1
)

echo === Loading .env ===
if not exist .env (
    echo No .env file found. Copy .env.example to .env and edit it.
    exit /b 1
)
for /f "usebackq tokens=1,* delims==" %%a in (".env") do (
    set "line=%%a"
    if not "!line:~0,1!"=="#" (
        set "%%a=%%b"
    )
)

echo === Build complete: monitor.exe ===
echo === Starting monitor ===
monitor.exe
