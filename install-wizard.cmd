@echo off
setlocal
title wecom-mcp-v2 Setup
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0install-wizard.ps1"
set "wizard_exit=%ERRORLEVEL%"
if not "%wizard_exit%"=="0" pause
exit /b %wizard_exit%
