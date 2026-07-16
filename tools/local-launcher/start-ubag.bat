@echo off
rem Thin double-click entry point. Windows won't run a .ps1 directly on
rem double-click (it opens in an editor by default), so this just bypasses
rem that and hands off to the real script.
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0start-ubag.ps1"
if errorlevel 1 (
  echo.
  echo UBAG failed to start - see the message above.
  pause
)
