@echo off
setlocal EnableDelayedExpansion

:: install.bat installs update-detector's pieces as native Windows
:: Services. Download it first, then run it from an elevated
:: (Administrator) Command Prompt -- unlike install.sh's `curl | sudo sh`,
:: cmd.exe has no way to run a script piped straight from stdin, so this
:: is two steps, not one:
::
::   curl -fsSL https://forgejo.winar.to/winarto/update-detector/raw/branch/main/install.bat -o install.bat
::   install.bat
::
:: A .bat, not a .ps1: cmd.exe runs a .bat directly, while a .ps1 run via
:: `powershell -File` is blocked outright on any host still at
:: PowerShell's historical default ExecutionPolicy (Restricted) unless
:: every invocation also passes -ExecutionPolicy Bypass -- and this script
:: is also re-invoked non-interactively by the companion's own
:: self-update feature (see internal/companion/selfupdate_windows.go),
:: with no operator present to grant an exception if that bites. A few
:: individual steps below still shell out to `powershell -Command` for
:: JSON parsing and one-off registry reads (PowerShell has real cmdlets
:: for both; hand-rolled batch string parsing of nested JSON or
:: backslash-zero-delimited multi-strings would be far more fragile) --
:: but that's an ad-hoc -Command string, never a -File load of an actual
:: .ps1, so it isn't subject to the same ExecutionPolicy gate.
::
:: There is no Docker-based path on Windows at all (no Windows container
:: images exist in this repo) -- every component here is always a native
:: Windows Service, so unlike install.sh this script never needs to
:: discover or avoid touching a Docker deployment.
::
:: Set INSTALL_VERSION to pin a release instead of "latest". Set
:: INSTALL_COMPONENTS (comma-separated: aggregator,agent,companion) for a
:: non-interactive install of any of those three -- this is also how the
:: companion's own self-update feature re-invokes this exact script to
:: update whichever component it's actually updating.
::
:: To remove a native install instead:
::
::   install.bat --uninstall
::
:: or set UNINSTALL_COMPONENTS the same way as INSTALL_COMPONENTS above,
:: for non-interactive use.

set "FORGEJO_API=https://forgejo.winar.to/api/v1/repos/winarto/update-detector"
set "INSTALL_BAT_RAW_URL=https://forgejo.winar.to/winarto/update-detector/raw/branch/main/install.bat"
if not defined INSTALL_VERSION set "INSTALL_VERSION=latest"
set "BIN_DIR=%ProgramFiles%\update-detector"
:: Where the companion caches its own copy of this script for
:: self-update use (see internal/companion/selfupdate_windows.go) -- it
:: re-invokes this file non-interactively rather than duplicating the
:: download/service-recreate logic already tested and shipped here.
set "CACHED_INSTALL_BAT=%BIN_DIR%\install.bat"

:: ---- admin check (equivalent of install.sh's `id -u` == 0 check) ----
net session >nul 2>&1
if errorlevel 1 (
  echo install.bat: must be run from an elevated ^(Administrator^) prompt >&2
  exit /b 1
)

:: amd64-only, same as install.sh's arch case -- no windows/arm64
:: release asset is published.
if not "%PROCESSOR_ARCHITECTURE%"=="AMD64" if not "%PROCESSOR_ARCHITEW6432%"=="AMD64" (
  echo install.bat: unsupported architecture -- only amd64 is published >&2
  exit /b 1
)

if not exist "%BIN_DIR%" mkdir "%BIN_DIR%"

set "uninstall_requested=0"
if "%~1"=="--uninstall" set "uninstall_requested=1"
if defined UNINSTALL_COMPONENTS set "uninstall_requested=1"

if "%uninstall_requested%"=="1" (
  call :prompt_uninstall_components
  if not defined components (
    echo install.bat: nothing to uninstall
    exit /b 0
  )
  rem Reverse of the install order below, same reasoning as install.sh:
  rem companion first, so uninstall_agent's "companion still installed"
  rem note only fires for the genuinely useful case, not as noise
  rem during a full teardown.
  set "haystack=,!components!,"
  echo !haystack!|find ",companion,">nul && call :uninstall_companion
  echo !haystack!|find ",agent,">nul && call :uninstall_agent
  echo !haystack!|find ",aggregator,">nul && call :uninstall_aggregator
  exit /b 0
)

call :prompt_components
set "haystack=,!components!,"
echo !haystack!|find ",aggregator,">nul && call :install_aggregator_native
echo !haystack!|find ",agent,">nul && call :install_agent_native
echo !haystack!|find ",companion,">nul && call :install_companion

exit /b 0

:: ============================================================
:: Helpers
:: ============================================================

:: resolve_asset_url NAME -> sets "download_url" to that asset's
:: browser_download_url for %INSTALL_VERSION%, or clears it if not found.
:: Uses Invoke-RestMethod (real JSON parsing) rather than hand-rolled
:: batch string slicing, the same spirit as install.sh calling out to
:: grep/sed rather than reimplementing JSON parsing in POSIX sh.
:resolve_asset_url
set "download_url="
if "%INSTALL_VERSION%"=="latest" (
  set "release_url=%FORGEJO_API%/releases/latest"
) else (
  set "release_url=%FORGEJO_API%/releases/tags/%INSTALL_VERSION%"
)
for /f "delims=" %%U in ('powershell -NoProfile -Command "try { ((Invoke-RestMethod -Uri '%release_url%').assets | Where-Object { $_.name -eq '%~1' } | Select-Object -First 1).browser_download_url } catch { }"') do set "download_url=%%U"
goto :eof

:: download_binary NAME DEST -> downloads NAME-windows-amd64.exe to DEST,
:: atomically (via a .new + move, so a partial download never replaces a
:: working binary).
:download_binary
set "asset_name=%~1-windows-amd64.exe"
echo install.bat: resolving %asset_name% from release %INSTALL_VERSION%...
call :resolve_asset_url "%asset_name%"
if not defined download_url (
  echo install.bat: could not find asset %asset_name% in release %INSTALL_VERSION% >&2
  exit /b 1
)
echo install.bat: downloading %download_url%
curl -fsSL "%download_url%" -o "%~2.new"
if errorlevel 1 (
  echo install.bat: download failed >&2
  exit /b 1
)
move /y "%~2.new" "%~2" >nul
goto :eof

:: cache_install_bat_for_companion -> saves a fresh copy of this exact
:: script to CACHED_INSTALL_BAT, for the companion's own self-update
:: feature to re-invoke later. Best-effort: a failure here is a warning,
:: not fatal -- the companion still works for apply/recheck either way.
:cache_install_bat_for_companion
echo install.bat: caching a copy of install.bat for the companion's own self-update use...
if "%INSTALL_VERSION%"=="latest" (
  set "raw_url=%INSTALL_BAT_RAW_URL%"
) else (
  set "raw_url=https://forgejo.winar.to/winarto/update-detector/raw/tag/%INSTALL_VERSION%/install.bat"
)
curl -fsSL "!raw_url!" -o "%CACHED_INSTALL_BAT%.new"
if errorlevel 1 (
  echo install.bat: warning: could not cache a copy of install.bat -- self-update via the companion won't work until this succeeds >&2
  goto :eof
)
move /y "%CACHED_INSTALL_BAT%.new" "%CACHED_INSTALL_BAT%" >nul
goto :eof

:: stop_if_running NAME -> stops NAME if it's currently running, and
:: waits (up to 30s) for it to actually reach STOPPED before returning.
:: Unlike Linux, where a running binary's file can be replaced/renamed
:: out from under it, Windows won't let a running .exe's file be
:: overwritten -- so unlike install.sh (which downloads first, then
:: restarts at the end), every install_* function here must stop the
:: service *before* replacing its binary, not after.
:stop_if_running
sc query "%~1" >nul 2>&1
if errorlevel 1 goto :eof
sc query "%~1" | find "RUNNING" >nul
if errorlevel 1 goto :eof
sc stop "%~1" >nul 2>&1
set "tries=0"
:stop_wait_loop
timeout /t 1 /nobreak >nul
sc query "%~1" | find "STOPPED" >nul
if not errorlevel 1 goto :eof
set /a tries+=1
if !tries! LSS 30 goto stop_wait_loop
echo install.bat: warning: %~1 did not stop within 30s, continuing anyway >&2
goto :eof

:: start_service NAME -> starts NAME, printing sc.exe's own error text
:: (not swallowing it) if it fails to actually reach RUNNING -- most
:: commonly Error 1053 ("The service did not respond to the start or
:: control request in a timely fashion"), which means the installed
:: .exe itself never called into the Windows Service Control Protocol
:: at all (a build too old to have that support, or a build for the
:: wrong OS/arch) -- not something re-running install.bat again can fix.
:start_service
sc start "%~1"
if errorlevel 1 (
  echo install.bat: warning: %~1 did not start -- see the error above. >&2
  echo   Common cause: this build of %~1.exe predates Windows Service support. >&2
  echo   Try re-downloading with INSTALL_VERSION=latest and re-running install.bat. >&2
  goto :eof
)
sc query "%~1" | find "RUNNING" >nul
if errorlevel 1 (
  echo install.bat: warning: %~1 reports started but isn't RUNNING -- check: sc query %~1 >&2
)
goto :eof

:: create_or_update_service NAME DISPLAYNAME BINPATH -> creates NAME if
:: it doesn't exist yet, or reconfigures it in place if it does (so
:: re-running this script to pick up a config or version change works,
:: same as install.sh's install_unit using `restart`, never just
:: `enable --now`, for exactly this reason). Auto-restarts on failure
:: (reset after 1 day, 5s delay) -- the closest native equivalent of
:: systemd's Restart=on-failure / RestartSec=5.
:create_or_update_service
sc query "%~1" >nul 2>&1
if errorlevel 1 (
  sc create "%~1" binPath= "\"%~3\"" start= auto DisplayName= "%~2" >nul
) else (
  sc config "%~1" binPath= "\"%~3\"" start= auto >nul
)
sc failure "%~1" reset= 86400 actions= restart/5000 >nul
goto :eof

:: read_service_var SERVICE KEY -> sets "read_val" to KEY's current value
:: in SERVICE's own registry Environment (REG_MULTI_SZ), or clears it if
:: SERVICE, its Environment value, or that particular KEY don't exist.
:: The native equivalent of install.sh's env_value FILE KEY, reading a
:: Windows Service's registry Environment instead of a flat env file.
:read_service_var
set "read_val="
for /f "delims=" %%V in ('powershell -NoProfile -Command "$k='%~2='; try { $e = (Get-ItemProperty -Path 'HKLM:\SYSTEM\CurrentControlSet\Services\%~1' -Name Environment -ErrorAction Stop).Environment; $m = $e | Where-Object { $_.StartsWith($k) } | Select-Object -First 1; if ($m) { $m.Substring($k.Length) } } catch { }"') do set "read_val=%%V"
goto :eof

:: prompt_aggregator_url -> sets "resolved_aggregator_url" to
:: %AGGREGATOR_URL% if already set, otherwise prompts for one
:: interactively. Unlike install.sh, there's no /dev/tty trick needed
:: here: this script can't be invoked via a stdin pipe the way `curl |
:: sh` works on Linux, so a real console is always attached whenever a
:: human runs this directly. AGGREGATOR_URL is mandatory for both the
:: agent and the companion -- without one, neither has any purpose.
:prompt_aggregator_url
if defined AGGREGATOR_URL (
  set "resolved_aggregator_url=%AGGREGATOR_URL%"
  goto :eof
)
set "resolved_aggregator_url="
echo install.bat: no AGGREGATOR_URL could be found automatically. >&2
echo   You only need one aggregator, reachable from this host -- it doesn't >&2
echo   have to be on the same network as this host; anywhere reachable over >&2
echo   the internet works fine too, as long as this installer can reach it. >&2
set /p "resolved_aggregator_url=Enter the aggregator's URL (e.g. http://aggregator-host:9090): "
goto :eof

:: prompt_components -> sets "components" to a comma-separated list of
:: components to install, read from INSTALL_COMPONENTS if set (for
:: non-interactive use), otherwise prompted interactively. Unlike
:: install.sh, this always prompts (or requires INSTALL_COMPONENTS) --
:: there's no Windows equivalent of "an agent container is presumably
:: already running, so just install the companion against it" to
:: default to, since there's no Docker path on Windows at all.
:prompt_components
if defined INSTALL_COMPONENTS (
  set "components=%INSTALL_COMPONENTS%"
  goto :eof
)
set "components="
echo install.bat can install the aggregator, the detector ^(agent^), the >&2
echo companion, or all three, each as a native Windows Service: >&2
echo. >&2
echo   1^) aggregator only >&2
echo   2^) detector ^(agent^) only >&2
echo   3^) companion only >&2
echo   4^) all three >&2
set /p "choice=Choose [1-4]: "
if "%choice%"=="1" set "components=aggregator"
if "%choice%"=="2" set "components=agent"
if "%choice%"=="3" set "components=companion"
if "%choice%"=="4" set "components=aggregator,agent,companion"
if not defined components (
  echo install.bat: invalid choice: %choice% >&2
  exit /b 1
)
goto :eof

:: prompt_uninstall_components -> like prompt_components, but for
:: uninstall: prints what was actually detected before offering the
:: menu, and leaves "components" unset (not exit -- this can run before
:: the caller decides whether that's fatal) if nothing is found at all.
:prompt_uninstall_components
if defined UNINSTALL_COMPONENTS (
  set "components=%UNINSTALL_COMPONENTS%"
  goto :eof
)
set "found="
sc query update-aggregator >nul 2>&1 && set "found=%found% aggregator"
sc query update-detector >nul 2>&1 && set "found=%found% agent"
sc query update-detector-companion >nul 2>&1 && set "found=%found% companion"
set "components="
if not defined found (
  echo install.bat: --uninstall requested, but no update-detector components >&2
  echo   were found on this host. >&2
  goto :eof
)
echo Found installed:%found% >&2
echo Which would you like to uninstall? >&2
echo. >&2
echo   1^) aggregator >&2
echo   2^) detector ^(agent^) >&2
echo   3^) companion >&2
echo   4^) all three >&2
set /p "choice=Choose [1-4]: "
if "%choice%"=="1" set "components=aggregator"
if "%choice%"=="2" set "components=agent"
if "%choice%"=="3" set "components=companion"
if "%choice%"=="4" set "components=aggregator,agent,companion"
if not defined components (
  echo install.bat: invalid choice: %choice% >&2
  exit /b 1
)
goto :eof

:: ============================================================
:: Install
:: ============================================================

:install_agent_native
echo install.bat: installing update-detector (agent)...
call :stop_if_running update-detector

set "bin_path=%BIN_DIR%\update-detector.exe"
call :download_binary update-detector "%bin_path%"

set "data_dir=%ProgramData%\update-detector"
if not exist "%data_dir%" mkdir "%data_dir%"

if defined AGGREGATOR_URL (
  set "resolved_aggregator_url=%AGGREGATOR_URL%"
) else (
  call :prompt_aggregator_url
)
if not defined resolved_aggregator_url (
  echo install.bat: AGGREGATOR_URL is required -- set it and re-run. >&2
  exit /b 1
)
if not defined LISTEN_ADDR set "LISTEN_ADDR=:8080"
if not defined CHECK_INTERVAL set "CHECK_INTERVAL=6h"

call :create_or_update_service update-detector "update-detector agent" "%bin_path%"
set "envval=LISTEN_ADDR=%LISTEN_ADDR%\0HOSTNAME_OVERRIDE=%HOSTNAME_OVERRIDE%\0CHECK_INTERVAL=%CHECK_INTERVAL%\0TELEGRAM_BOT_TOKEN=%TELEGRAM_BOT_TOKEN%\0TELEGRAM_CHAT_ID=%TELEGRAM_CHAT_ID%\0AGGREGATOR_URL=!resolved_aggregator_url!\0STATE_FILE=%data_dir%\state.json\0AGENT_IDENTITY_FILE=%data_dir%\agent-identity.json"
reg add "HKLM\SYSTEM\CurrentControlSet\Services\update-detector" /v Environment /t REG_MULTI_SZ /d "!envval!" /f >nul
call :start_service update-detector

echo install.bat: update-detector installed and started. Check: sc query update-detector
goto :eof

:install_aggregator_native
echo install.bat: installing update-aggregator...
call :stop_if_running update-aggregator

set "bin_path=%BIN_DIR%\update-aggregator.exe"
call :download_binary update-aggregator "%bin_path%"

set "data_dir=%ProgramData%\update-aggregator"
if not exist "%data_dir%" mkdir "%data_dir%"

rem Prefixed AGGREGATOR_* input names (distinct from the agent's own
rem plain LISTEN_ADDR/TELEGRAM_* above), specifically so installing both
rem agent and aggregator in the same run can't accidentally share one
rem secret/token meant for only one of them -- same reasoning, and same
rem naming convention, as install.sh's own install_aggregator_native.
if not defined AGGREGATOR_LISTEN_ADDR set "AGGREGATOR_LISTEN_ADDR=:9090"

call :create_or_update_service update-aggregator "update-aggregator" "%bin_path%"
set "envval=LISTEN_ADDR=%AGGREGATOR_LISTEN_ADDR%\0REGISTRY_FILE=%data_dir%\registry.json\0TELEGRAM_BOT_TOKEN=%AGGREGATOR_TELEGRAM_BOT_TOKEN%\0TELEGRAM_CHAT_ID=%AGGREGATOR_TELEGRAM_CHAT_ID%\0ADMIN_APPLY_SHARED_SECRET=%ADMIN_APPLY_SHARED_SECRET%"
reg add "HKLM\SYSTEM\CurrentControlSet\Services\update-aggregator" /v Environment /t REG_MULTI_SZ /d "!envval!" /f >nul
call :start_service update-aggregator

echo install.bat: update-aggregator installed and started. Check: sc query update-aggregator
if defined ADMIN_APPLY_SHARED_SECRET (
  echo install.bat: ADMIN_APPLY_SHARED_SECRET=%ADMIN_APPLY_SHARED_SECRET% >&2
  echo   Keep this somewhere safe ^(e.g. a password manager^) -- it's the only >&2
  echo   credential gating apply/self-update actions, it's stored in this >&2
  echo   service's own registry Environment value and never printed again >&2
  echo   after this, and every browser or script that triggers an apply >&2
  echo   needs this exact value. >&2
) else (
  echo install.bat: no ADMIN_APPLY_SHARED_SECRET set -- apply/self-update stay >&2
  echo   disabled ^(501^) until you set one and restart update-aggregator. >&2
)
goto :eof

:: install_companion -> unlike install.sh, discovery only ever looks for
:: a *native* update-detector Windows Service, never a Docker container:
:: winget applies to the Windows host itself, so a companion here can
:: only ever make sense paired with a host-native Windows agent, not a
:: containerized one (there's no Windows container image in this repo
:: at all, and even if there were, a container's own OS package state
:: has nothing to do with the host's winget-visible package state).
::
:: KNOWN LIMITATION, confirmed live: this service defaults to running as
:: LocalSystem (see create_or_update_service), but winget.exe is an App
:: Execution Alias registered per-*user* (it lives under that user's own
:: AppData\Local\Microsoft\WindowsApps, on *that user's* PATH only) --
:: SYSTEM has no such registration at all, so `winget upgrade` fails
:: outright with "executable file not found in %PATH%", confirmed on a
:: real install (this hits update-detector's own detection just as much
:: as this companion's apply path -- see README.md). Workaround:
:: reconfigure this service (and update-detector, for the same reason)
:: to run as a real user account instead of LocalSystem:
::
::   sc config update-detector-companion obj= ".\<user>" password= "<password>"
::   sc stop update-detector-companion & sc start update-detector-companion
::
:: If that fails with Error 1069 ("logon failure"), the account first
:: needs the "Log on as a service" right: secpol.msc -> Local Policies ->
:: User Rights Assignment -> "Log on as a service" -> add that user.
:install_companion
echo install.bat: installing update-detector-companion...
call :stop_if_running update-detector-companion

set "bin_path=%BIN_DIR%\update-detector-companion.exe"
call :download_binary update-detector-companion "%bin_path%"

sc query update-detector >nul 2>&1
if errorlevel 1 (
  echo install.bat: no update-detector ^(agent^) Windows Service found on this host -- install it first ^(see README^). >&2
  exit /b 1
)

call :read_service_var update-detector AGGREGATOR_URL
set "agg_url=%read_val%"
call :read_service_var update-detector LISTEN_ADDR
set "agent_listen_addr=%read_val%"
if not defined agent_listen_addr set "agent_listen_addr=:8080"
for /f "tokens=2 delims=:" %%P in ("%agent_listen_addr%") do set "agent_port=%%P"
if not defined agent_port set "agent_port=8080"
set "agent_status_url=http://localhost:%agent_port%/status"

if not defined agg_url (
  call :prompt_aggregator_url
  set "agg_url=!resolved_aggregator_url!"
)
if not defined agg_url (
  echo install.bat: no AGGREGATOR_URL available -- the companion has no purpose without one. >&2
  echo   Set AGGREGATOR_URL and re-run, or configure it on the update-detector >&2
  echo   Windows Service this host runs first, and re-run. >&2
  exit /b 1
)

echo install.bat: aggregator=!agg_url! agent_status=!agent_status_url!

call :create_or_update_service update-detector-companion "update-detector-companion" "%bin_path%"
set "envval=COMPANION_SOCKET_PATH=\\.\pipe\update-detector\companion-token\0AGGREGATOR_URL=!agg_url!\0AGENT_STATUS_URL=!agent_status_url!"
reg add "HKLM\SYSTEM\CurrentControlSet\Services\update-detector-companion" /v Environment /t REG_MULTI_SZ /d "!envval!" /f >nul
call :start_service update-detector-companion

call :cache_install_bat_for_companion
echo install.bat: done. Check status with: sc query update-detector-companion
goto :eof

:: ============================================================
:: Uninstall
:: ============================================================

:uninstall_agent
sc query update-detector >nul 2>&1
if errorlevel 1 (
  echo install.bat: no update-detector ^(agent^) install found
) else (
  echo install.bat: removing update-detector ^(agent^)...
  sc stop update-detector >nul 2>&1
  timeout /t 2 /nobreak >nul
  sc delete update-detector >nul 2>&1
  del /f /q "%BIN_DIR%\update-detector.exe" >nul 2>&1
  echo install.bat: removing %ProgramData%\update-detector ^(includes this agent's aggregator identity^)
  rd /s /q "%ProgramData%\update-detector" >nul 2>&1
)
sc query update-detector-companion >nul 2>&1
if not errorlevel 1 (
  echo install.bat: note: update-detector-companion is still installed on this >&2
  echo   host and depends on the agent -- consider uninstalling it too. >&2
)
goto :eof

:uninstall_aggregator
sc query update-aggregator >nul 2>&1
if errorlevel 1 (
  echo install.bat: no update-aggregator install found
) else (
  echo install.bat: removing update-aggregator...
  sc stop update-aggregator >nul 2>&1
  timeout /t 2 /nobreak >nul
  sc delete update-aggregator >nul 2>&1
  del /f /q "%BIN_DIR%\update-aggregator.exe" >nul 2>&1
  echo install.bat: removing %ProgramData%\update-aggregator ^(includes the fleet registry -- all enrolled/approved hosts^)
  rd /s /q "%ProgramData%\update-aggregator" >nul 2>&1
)
goto :eof

:uninstall_companion
sc query update-detector-companion >nul 2>&1
if errorlevel 1 (
  echo install.bat: no update-detector-companion install found
  goto :eof
)
echo install.bat: removing update-detector-companion...
rem Companion is always native, needs real administrator rights to run
rem winget -- no Docker case to check here, same as install.sh's own
rem uninstall_companion.
sc stop update-detector-companion >nul 2>&1
timeout /t 2 /nobreak >nul
sc delete update-detector-companion >nul 2>&1
del /f /q "%BIN_DIR%\update-detector-companion.exe" >nul 2>&1
del /f /q "%CACHED_INSTALL_BAT%" >nul 2>&1
goto :eof
