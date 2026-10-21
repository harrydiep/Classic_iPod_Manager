@echo off
rem Builds PodManager.exe (requires Go 1.22+ from https://go.dev/dl/)
set GOOS=windows
set GOARCH=amd64
set CGO_ENABLED=0
go build -trimpath -ldflags "-s -w -H windowsgui" -o PodManager.exe .
if errorlevel 1 exit /b 1
echo Built PodManager.exe
