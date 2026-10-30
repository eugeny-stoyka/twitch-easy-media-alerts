@echo off
rem Builds dist\TwitchEasyMediaAlerts.exe (no console window).
rem Needs Go 1.26+; go-winres is only needed if winres\winres.json or the icon changed:
rem   go install github.com/tc-hib/go-winres@latest
rem   go generate .\src
setlocal
cd /d "%~dp0"
set CGO_ENABLED=0
set GOOS=windows
set GOARCH=amd64
go build -trimpath -ldflags "-H windowsgui -s -w" -o dist\TwitchEasyMediaAlerts.exe .\src || exit /b 1
echo Built dist\TwitchEasyMediaAlerts.exe
