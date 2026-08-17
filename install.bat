@echo off
setlocal EnableDelayedExpansion

:: install.bat installs update-detector's pieces as native Windows
:: Services. Download it first, then run it from an elevated
:: (Administrator) Command Prompt -- unlike install.sh's `curl | sudo sh`,
:: cmd.exe has no way to run a script piped straight from stdin, so this
:: is two steps, not one:
::
::   curl -fsSL https://raw.githubusercontent.com/sinwe/update-detector/main/install.bat -o install.bat
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

set "GITHUB_API=https://api.github.com/repos/sinwe/update-detector"
set "INSTALL_BAT_RAW_URL=https://raw.githubusercontent.com/sinwe/update-detector/main/install.bat"
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
rem Track failures explicitly -- a bare `exit /b 0` here regardless of
rem outcome would report success even when a call below failed (e.g. a
rem download error), which matters most for the companion's own
rem self-update: its Go caller treats this script's exit code as the
rem entire result. Confirmed live: a failed download used to be
rem swallowed exactly this way.
set "install_failed=0"
echo !haystack!|find ",aggregator,">nul && (call :install_aggregator_native || set "install_failed=1")
echo !haystack!|find ",agent,">nul && (call :install_agent_native || set "install_failed=1")
echo !haystack!|find ",companion,">nul && (call :install_companion || set "install_failed=1")

exit /b %install_failed%

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
  set "release_url=%GITHUB_API%/releases/latest"
) else (
  set "release_url=%GITHUB_API%/releases/tags/%INSTALL_VERSION%"
)
rem GitHub's API rejects requests with no User-Agent at all (403).
for /f "delims=" %%U in ('powershell -NoProfile -Command "try { ((Invoke-RestMethod -Uri '%release_url%' -Headers @{ 'User-Agent' = 'update-detector-install.bat' }).assets | Where-Object { $_.name -eq '%~1' } | Select-Object -First 1).browser_download_url } catch { }"') do set "download_url=%%U"
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
rem move /y only suppresses the interactive overwrite prompt -- it does
rem NOT clear a read-only attribute on the destination, and fails with
rem this exact "Access is denied." if one is set (some antivirus/
rem deployment tools mark installed executables read-only). Clear it
rem unconditionally before attempting the swap; harmless no-op if the
rem file doesn't exist yet (fresh install) or isn't read-only.
if exist "%~2" attrib -R "%~2" >nul 2>&1
rem Retry the swap a few times, without any new label/goto inside
rem this subroutine -- confirmed live, adding a labeled backward-jump
rem loop here (even one that never actually loops, e.g. succeeding on
rem the very first attempt) corrupted label resolution for unrelated
rem calls made later in the same install_* run (create_or_update_service,
rem read_service_var). for /l avoids that entirely. Windows Defender's
rem real-time scan of a freshly-downloaded unsigned .exe can hold a
rem brief lock on it that makes an immediate `move` fail with "Access
rem is denied." even with the target service already fully stopped and
rem nothing else on the system holding it -- the lock clears itself
rem within a couple of seconds once the scan finishes.
set "move_ok=0"
for /l %%i in (1,1,5) do (
  if "!move_ok!"=="0" (
    move /y "%~2.new" "%~2" >nul 2>&1
    if not errorlevel 1 (
      set "move_ok=1"
    ) else (
      timeout /t 1 /nobreak >nul
    )
  )
)
if "!move_ok!"=="0" (
  echo install.bat: could not replace %~2 -- still locked after 5 retries ^(check for antivirus or other software holding it open^) >&2
  exit /b 1
)
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
  set "raw_url=https://raw.githubusercontent.com/sinwe/update-detector/%INSTALL_VERSION%/install.bat"
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
rem Disable auto-restart-on-failure BEFORE attempting a stop at all --
rem confirmed live: create_or_update_service's own failure policy
rem (restart every 5s) can relaunch a crash-looping service faster
rem than the caller's subsequent download+swap completes, leaving a
rem freshly-respawned process holding the binary file open even though
rem `sc query` reported STOPPED at the moment this function checked
rem it -- no amount of retrying the swap wins that race. Re-enabled
rem automatically when create_or_update_service reconfigures the
rem service again later in this same run.
sc failure "%~1" reset= 0 actions= "" >nul 2>&1
rem Checking for "not STOPPED" rather than "is RUNNING" is deliberate --
rem confirmed live, a service wedged in START_PENDING (never finishing
rem its transition, e.g. because the process itself is stuck) failed
rem the old "RUNNING" check and was silently skipped here entirely,
rem leaving its process alive to hold the port/file lock the reinstall
rem below then failed against with "Access is denied.". Any state that
rem isn't already STOPPED needs an actual stop attempt.
sc query "%~1" | find "STOPPED" >nul
if not errorlevel 1 goto :eof
sc stop "%~1" >nul 2>&1
set "tries=0"
:stop_wait_loop
timeout /t 1 /nobreak >nul
sc query "%~1" | find "STOPPED" >nul
if not errorlevel 1 goto :eof
set /a tries+=1
if !tries! LSS 30 goto stop_wait_loop
rem A service that never even reaches a state where it can accept a
rem Stop control request (e.g. permanently wedged in START_PENDING)
rem will never respond to `sc stop` at all -- confirmed live, this is
rem exactly what leaves a reinstall unable to replace the binary.
rem Force-kill its process directly as a last resort rather than just
rem warning and leaving it running.
echo install.bat: warning: %~1 did not stop within 30s -- force-killing its process instead >&2
set "stuck_pid="
for /f "tokens=3" %%P in ('sc query "%~1" ^| findstr /i "PID"') do set "stuck_pid=%%P"
if defined stuck_pid taskkill /f /pid !stuck_pid! >nul 2>&1
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

:: configure_winget_account SERVICE -> optionally reconfigures SERVICE to
:: run as a real user account instead of LocalSystem, so winget (a
:: per-user App Execution Alias -- SYSTEM has no such registration, see
:: README's platform-limitations section) actually works for it.
::
:: winget is OPTIONAL, not this checker's primary signal -- actual
:: Windows Update detection (and the registry-based reboot-pending
:: check) works fine under LocalSystem regardless, so this is always
:: skippable, and skipped by default. Set WINGET_ACCOUNT/WINGET_PASSWORD
:: for non-interactive use (e.g. from another install.bat run, or a
:: script); otherwise prompts once per service -- but only the first
:: time: if SERVICE is already configured to run as something other
:: than LocalSystem (confirmed live to matter -- every self-update/
:: manual re-run used to re-ask for a password even though the account
:: was already set), this is skipped entirely rather than re-asking.
:configure_winget_account
set "current_start_name="
for /f "delims=" %%A in ('powershell -NoProfile -Command "try { (Get-CimInstance Win32_Service | Where-Object { $_.Name -eq '%~1' }).StartName } catch { }"') do set "current_start_name=%%A"
if not defined WINGET_ACCOUNT if defined current_start_name if /i not "!current_start_name!"=="LocalSystem" (
  echo install.bat: %~1 is already configured to run as !current_start_name! -- leaving it as-is. >&2
  echo   Set WINGET_ACCOUNT/WINGET_PASSWORD to explicitly reconfigure it. >&2
  goto :eof
)
if defined WINGET_ACCOUNT (
  set "wa_user=%WINGET_ACCOUNT%"
  set "wa_pass=%WINGET_PASSWORD%"
) else (
  echo. >&2
  echo install.bat: winget-based package detection/apply is OPTIONAL for %~1 -- >&2
  echo   this checker's main signal is actual Windows Update, which already >&2
  echo   works fine under the default LocalSystem account. Only say yes here >&2
  echo   if you specifically also want winget-visible package upgrades. >&2
  set "wa_enable="
  set /p "wa_enable=Run %~1 as your own account so winget works too? [y/N]: "
  if /i not "!wa_enable!"=="y" goto :eof
  rem .\USERNAME, not %USERDOMAIN%\%USERNAME% -- on a workgroup,
  rem non-domain-joined, machine USERDOMAIN is often just the literal
  rem string "WORKGROUP", which Windows doesn't recognize as a valid
  rem account qualifier at all -- confirmed live: SC error 1057,
  rem "account name is invalid". ".\" always means "this local
  rem machine" regardless of workgroup/domain name. Must be a real
  rem local account, or a domain account if typed over this default --
  rem never a Microsoft/email-style account, which SC cannot log a
  rem service on as at all -- confirmed live: error 1355, "specified
  rem domain does not exist".
  set "wa_user=.\%USERNAME%"
  set /p "wa_user=Local or domain account, e.g. .\%USERNAME% [!wa_user!]: "
  for /f "delims=" %%P in ('powershell -NoProfile -Command "$p = Read-Host -AsSecureString 'Password for !wa_user!'; [Runtime.InteropServices.Marshal]::PtrToStringAuto([Runtime.InteropServices.Marshal]::SecureStringToBSTR($p))"') do set "wa_pass=%%P"
)
if not defined wa_pass (
  echo install.bat: no password given -- leaving %~1 on LocalSystem >&2
  goto :eof
)

call :grant_logon_as_service "!wa_user!"
echo install.bat: reconfiguring %~1 to run as !wa_user!...
sc config "%~1" obj= "!wa_user!" password= "!wa_pass!"
if errorlevel 1 (
  echo install.bat: warning: could not reconfigure %~1's logon account -- see the error above, leaving it on LocalSystem >&2
)
set "wa_pass="
goto :eof

:: grant_logon_as_service ACCOUNT -> grants ACCOUNT the "Log on as a
:: service" right via secedit (built in on every modern Windows, unlike
:: the deprecated ntrights.exe) -- exports the current policy, appends
:: ACCOUNT's SID to the SeServiceLogonRight line, reapplies just that
:: one area. Best-effort: sc config above still runs either way; if this
:: didn't actually take (or ACCOUNT already had the right, or something
:: about this host's policy shape wasn't what this expected), `sc start`
:: will surface Error 1069 ("logon failure") and README.md documents the
:: manual secpol.msc fallback for that case.
:grant_logon_as_service
set "sid="
for /f "delims=" %%S in ('powershell -NoProfile -Command "try { (New-Object System.Security.Principal.NTAccount('%~1')).Translate([System.Security.Principal.SecurityIdentifier]).Value } catch { }"') do set "sid=%%S"
if not defined sid (
  echo install.bat: warning: could not resolve a SID for %~1 -- skipping automatic "Log on as a service" grant >&2
  goto :eof
)
echo install.bat: granting "Log on as a service" to %~1...
set "secinf=%TEMP%\update-detector-secpol.inf"
set "secdb=%TEMP%\update-detector-secpol.sdb"
secedit /export /cfg "%secinf%" /areas USER_RIGHTS >nul
rem secedit's own INF files are UTF-16LE, not the system codepage --
rem every read/write below must say so explicitly (findstr /u, Get-/
rem Set-/Add-Content -Encoding Unicode), or the file ends up with mixed
rem encodings that secedit /configure then fails to parse correctly.
findstr /b /i /u /c:"SeServiceLogonRight" "%secinf%" >nul
if errorlevel 1 (
  powershell -NoProfile -Command "Add-Content -Encoding Unicode -Path '%secinf%' -Value 'SeServiceLogonRight = *%sid%'"
) else (
  powershell -NoProfile -Command "(Get-Content -Encoding Unicode '%secinf%') -replace '(?i)^SeServiceLogonRight\s*=\s*(.*)$', ('SeServiceLogonRight = $1,*' + '%sid%') | Set-Content -Encoding Unicode '%secinf%'"
)
secedit /configure /db "%secdb%" /cfg "%secinf%" /areas USER_RIGHTS >nul
if errorlevel 1 (
  echo install.bat: warning: secedit failed to apply the "Log on as a service" grant -- if the service fails to start with Error 1069, grant it manually via secpol.msc (see README.md) >&2
)
del /f /q "%secinf%" "%secdb%" >nul 2>&1
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

:: prompt_aggregator_url [EXISTING] -> sets "resolved_aggregator_url" to
:: %AGGREGATOR_URL% if already set (e.g. from install.bat's own
:: self-update reinvocation), otherwise prompts for one interactively --
:: pre-filled with EXISTING if the caller found one already configured
:: (e.g. re-running this script by hand on an already-installed agent),
:: so hitting Enter with no typed input keeps it unchanged: `set /p`
:: leaves its target variable's existing value alone when given a blank
:: response, which is exactly the mechanism this relies on. Unlike
:: install.sh, there's no /dev/tty trick needed here: this script can't
:: be invoked via a stdin pipe the way `curl | sh` works on Linux, so a
:: real console is always attached whenever a human runs this directly.
:: AGGREGATOR_URL is mandatory for both the agent and the companion --
:: without one, neither has any purpose.
:prompt_aggregator_url
if defined AGGREGATOR_URL (
  set "resolved_aggregator_url=%AGGREGATOR_URL%"
  goto :eof
)
set "resolved_aggregator_url=%~1"
if defined resolved_aggregator_url (
  set /p "resolved_aggregator_url=Aggregator URL [!resolved_aggregator_url!], Enter to keep: "
  goto :eof
)
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
set "download_binary_rc=%errorlevel%"
if not "%download_binary_rc%"=="0" exit /b 1

set "data_dir=%ProgramData%\update-detector"
if not exist "%data_dir%" mkdir "%data_dir%"

if defined AGGREGATOR_URL (
  set "resolved_aggregator_url=%AGGREGATOR_URL%"
) else (
  call :read_service_var update-detector AGGREGATOR_URL
  call :prompt_aggregator_url "!read_val!"
)
if not defined resolved_aggregator_url (
  echo install.bat: AGGREGATOR_URL is required -- set it and re-run. >&2
  exit /b 1
)

rem Every other field here is optional -- rather than silently resetting
rem any of them to a hardcoded default on a plain re-run (e.g. to pick
rem up a new version or a winget account change), fall back to this
rem service's own already-configured value first, the same
rem config-preservation self-update already gets via
rem internal/companion/selfupdate_windows.go's existingConfigEnv, just
rem reached here too since a manual re-run never goes through that Go
rem code at all.
if not defined LISTEN_ADDR (
  call :read_service_var update-detector LISTEN_ADDR
  set "LISTEN_ADDR=!read_val!"
)
if not defined LISTEN_ADDR set "LISTEN_ADDR=:8080"
if not defined CHECK_INTERVAL (
  call :read_service_var update-detector CHECK_INTERVAL
  set "CHECK_INTERVAL=!read_val!"
)
if not defined CHECK_INTERVAL set "CHECK_INTERVAL=6h"
if not defined HOSTNAME_OVERRIDE (
  call :read_service_var update-detector HOSTNAME_OVERRIDE
  set "HOSTNAME_OVERRIDE=!read_val!"
)
if not defined TELEGRAM_BOT_TOKEN (
  call :read_service_var update-detector TELEGRAM_BOT_TOKEN
  set "TELEGRAM_BOT_TOKEN=!read_val!"
)
if not defined TELEGRAM_CHAT_ID (
  call :read_service_var update-detector TELEGRAM_CHAT_ID
  set "TELEGRAM_CHAT_ID=!read_val!"
)

call :create_or_update_service update-detector "update-detector agent" "%bin_path%"
set "envval=LISTEN_ADDR=%LISTEN_ADDR%\0HOSTNAME_OVERRIDE=%HOSTNAME_OVERRIDE%\0CHECK_INTERVAL=%CHECK_INTERVAL%\0TELEGRAM_BOT_TOKEN=%TELEGRAM_BOT_TOKEN%\0TELEGRAM_CHAT_ID=%TELEGRAM_CHAT_ID%\0AGGREGATOR_URL=!resolved_aggregator_url!\0STATE_FILE=%data_dir%\state.json\0AGENT_IDENTITY_FILE=%data_dir%\agent-identity.json"
reg add "HKLM\SYSTEM\CurrentControlSet\Services\update-detector" /v Environment /t REG_MULTI_SZ /d "!envval!" /f >nul
call :configure_winget_account update-detector
call :start_service update-detector

echo install.bat: update-detector installed and started. Check: sc query update-detector
goto :eof

:install_aggregator_native
echo install.bat: installing update-aggregator...
call :stop_if_running update-aggregator

set "bin_path=%BIN_DIR%\update-aggregator.exe"
call :download_binary update-aggregator "%bin_path%"
set "download_binary_rc=%errorlevel%"
if not "%download_binary_rc%"=="0" exit /b 1

set "data_dir=%ProgramData%\update-aggregator"
if not exist "%data_dir%" mkdir "%data_dir%"

rem Prefixed AGGREGATOR_* input names (distinct from the agent's own
rem plain LISTEN_ADDR/TELEGRAM_* above), specifically so installing both
rem agent and aggregator in the same run can't accidentally share one
rem secret/token meant for only one of them -- same reasoning, and same
rem naming convention, as install.sh's own install_aggregator_native.
rem
rem Same re-run config-preservation reasoning as install_agent_native
rem above -- critically including ADMIN_APPLY_SHARED_SECRET: losing that
rem on a plain re-run would silently disable apply/self-update until
rem it's set again, not just reset a cosmetic default. The registry
rem itself stores these under their plain (unprefixed) names, since
rem that's what update-aggregator's own config.Load actually reads --
rem read_service_var is given that plain name, its result assigned to
rem the prefixed *input* variable name below, mirroring
rem existingConfigEnv's own translate() table on the Go side.
if not defined AGGREGATOR_LISTEN_ADDR (
  call :read_service_var update-aggregator LISTEN_ADDR
  set "AGGREGATOR_LISTEN_ADDR=!read_val!"
)
if not defined AGGREGATOR_LISTEN_ADDR set "AGGREGATOR_LISTEN_ADDR=:9090"
if not defined AGGREGATOR_TELEGRAM_BOT_TOKEN (
  call :read_service_var update-aggregator TELEGRAM_BOT_TOKEN
  set "AGGREGATOR_TELEGRAM_BOT_TOKEN=!read_val!"
)
if not defined AGGREGATOR_TELEGRAM_CHAT_ID (
  call :read_service_var update-aggregator TELEGRAM_CHAT_ID
  set "AGGREGATOR_TELEGRAM_CHAT_ID=!read_val!"
)
if not defined ADMIN_APPLY_SHARED_SECRET (
  call :read_service_var update-aggregator ADMIN_APPLY_SHARED_SECRET
  set "ADMIN_APPLY_SHARED_SECRET=!read_val!"
)

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
set "download_binary_rc=%errorlevel%"
if not "%download_binary_rc%"=="0" exit /b 1

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
call :configure_winget_account update-detector-companion
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
