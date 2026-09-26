@echo off
rem Double-click entry point for retry-failed-jobs.ps1 — Windows won't run a
rem .ps1 directly on double-click, and default execution policies block .ps1
rem files entirely, so this wraps the call with -ExecutionPolicy Bypass
rem (per-invocation only; no system policy is changed).
rem
rem   retry-failed-jobs.bat            -> dry run (shows what would be retried)
rem   retry-failed-jobs.bat -Apply     -> actually POSTs the retries
rem
rem Prerequisite: log in to the providers first (Dashboard -> Browser
rem Sessions), otherwise jobs pause again at manual_login_required.

powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0retry-failed-jobs.ps1" %*
if errorlevel 1 (
  echo.
  echo Retry sweep failed - see the message above.
  pause
)
