@echo off
powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%~dp0scripts\dev.ps1" %*
if errorlevel 1 (
  echo Startup failed. See the error above.
  pause
  exit /b 1
)
