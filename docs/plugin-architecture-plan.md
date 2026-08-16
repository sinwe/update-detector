# Plugin architecture for OS-flavor checkers/appliers, with Windows as the first new platform

## Context

update-detector currently supports three "flavours" — Ubuntu, Debian/Raspberry Pi OS,
and WSL2 (which just reuses the Debian/Ubuntu native install path, not a separate
checker) — via a hardcoded switch in `cmd/update-detector/main.go` on
`internal/hostflavor.Detect`'s result. The README already commits, in writing, to
Windows/macOS being "planned," and specifically says: *"the checker is designed as an
interface so that's a new implementation, not a rewrite."* This plan makes that
statement concretely true — turning flavor selection into a real plugin/registry
mechanism — and proves it out end-to-end by adding Windows as the first plugin added
under the new architecture, covering detection (`internal/checker`) and, per this
plan's confirmed scope, the companion's apply/self-update path too (`internal/companion`).

Confirmed scope decisions (do not re-litigate):
1. **Windows checker depth**: real, best-effort detection (winget for packages, registry
   keys for reboot-pending) — not a stub.
2. **Apply/self-update**: in scope. Windows should eventually be able to apply updates
   too, not just detect them.
3. **Build/ship**: same binary/module, build-tag gated (`//go:build windows` /
   `//go:build !windows`), so `cmd/update-detector` cross-compiles to `windows/amd64`
   with the Windows plugin linked in and the Linux ones excluded — no separate module,
   matching the README's "new implementation, not a rewrite."

## Architecture today (for reference — no code changes described in this section)

- `internal/checker/checker.go`: `Checker interface { Platform() string; Check(ctx, *Status) (Status, error) }` — already platform-neutral, no changes needed.
- `internal/checker/types.go`: `Status`/`OSInfo`/`PackageInfo`/`PackageUpgrade` — already free of apt-specific fields.
- `internal/checker/ubuntu`, `internal/checker/debian`: two full implementations sharing `internal/aptutil` (apt.conf + `apt-get update`) and `internal/checker/reboot` (`/var/run/reboot-required`). Ubuntu additionally uses `apt-check` + `changelogs.ubuntu.com/meta-release` for OS-upgrade detection; Debian has neither (parses simulated `apt-get -s dist-upgrade` instead, and never reports an OS upgrade).
- `internal/hostflavor.Detect(osReleaseFile string) string`: reads the host's `/etc/os-release` `ID=`; defaults to `"ubuntu"` on anything unrecognized/missing — wrong default for a platform that will never have that file.
- `cmd/update-detector/main.go` (~line 50-79): flat hardcoded `switch flavor { case "debian": ...; default: ubuntu... }`, hand-picking `ubuntu.Config`/`debian.Config` fields from the single `config.Config`.
- `internal/companion/deploykind.go`: `DeployKind{None,Native,Docker}` — Native = systemd unit file presence; Docker = `docker ps`/`inspect` (gracefully no-ops if `docker` missing).
- `internal/companion/execute.go` (`Apply`): unconditionally shells to `apt-get install/upgrade/dist-upgrade/autoremove` for every apply-type action — 100% apt-hardcoded, no platform dispatch at all today.
- `internal/companion/selfupdate.go` (`SelfUpdate`): re-invokes `install.sh` (POSIX shell, assumes systemd restart bundled inside it) for native installs, or `docker compose up -d` + retag for Docker — no platform abstraction.
- `internal/aggregator/companion.go`: `Action{Type, Packages, Component, TargetVersion}` — **already platform-agnostic at the wire level**; the aggregator doesn't know or care what OS a companion's host runs. No changes needed here.
- `.github/workflows/release.yml`: cross-compiles all three binaries for `linux/amd64,arm64` only; no Windows target.
- `install.sh`: POSIX shell, hard-requires `systemctl` before any branching — cannot run on native Windows at all.

## Design

### 1. Checker registry (`internal/checker`)

New `internal/checker/registry.go`:

```go
type Fields map[string]string
type Factory func(Fields) (Checker, error)

func Register(name string, f Factory)   // panics on duplicate; called from each platform package's init()
func New(name string, fields Fields) (Checker, error)
```

`Fields` (a plain `map[string]string`) is the handoff shape, not each platform's own
typed `Config` struct — this is the key trick that lets `main.go` stay ignorant of
which platform packages even exist. A typed-`Config`-per-platform approach was
considered and rejected: `main.go` would still need to import `ubuntu`/`debian`/`windows`
unconditionally to construct their literals, defeating the point of build-tag exclusion.

Each platform's own package keeps its existing typed `Config` internally, and adds an
`init()`:

```go
// internal/checker/ubuntu/ubuntu.go
func init() {
    checker.Register("ubuntu", func(f checker.Fields) (checker.Checker, error) {
        return New(Config{Hostname: f["hostname"], AptSourcesList: f["apt_sources_list"], ...})
    })
}
```

`internal/config/config.go` gets one new method, `CheckerFields() checker.Fields`,
translating the existing flat `Config` struct into the string-keyed bag — no
restructuring of `Config` itself; it stays flat (this is a 3-4 platform project, not a
plugin ecosystem, so a nested per-plugin config scheme would be over-engineering).

### 2. Build-tag split (the mechanism that makes registry-based selection actually work)

- `internal/checker/ubuntu/*.go`, `internal/checker/debian/*.go`: add `//go:build !windows` (makes the existing implicit Linux-only assumption explicit).
- `internal/checker/windows/*.go` (new package): `//go:build windows`.
- `internal/hostflavor/hostflavor.go` → renamed/split into `hostflavor.go` (`//go:build !windows`, today's exact os-release-sniffing body) + new `hostflavor_windows.go` (`//go:build windows`, unconditionally returns `"windows"` — no os-release file to sniff, and no other flavor could be correct since only the windows checker package is even linked in).
- `cmd/update-detector/main.go` loses its per-platform imports/switch entirely; new `cmd/update-detector/platforms_unix.go` (`//go:build !windows`, blank-imports `ubuntu`+`debian` for their `init()` registration) and `platforms_windows.go` (`//go:build windows`, blank-imports `windows`). `main.go` itself only calls `checker.New(flavor, cfg.CheckerFields())` — compiles identically regardless of `GOOS`.

### 3. Windows checker (`internal/checker/windows`, `//go:build windows`)

Mirrors ubuntu/debian's file layout: `windows.go` (Checker+Config+Check), `packages.go`, `reboot.go`. No `release.go` — no OS-upgrade detection in v1, same posture as Debian (`OSInfo.UpdateAvailable` stays `false`; `CurrentVersion`/`CurrentCodename` populated informationally from the `CurrentVersion` registry key's `DisplayVersion`/`ProductName`).

- **Packages** (`packages.go`): shell `winget upgrade --include-unknown --accept-source-agreements --disable-interactivity`. Winget has no reliably-present JSON output across versions in the wild, so parse its table output the same "best-effort scrape" way Debian parses `apt-get -s dist-upgrade` — locate the header row, slice each data row at the header's own column-start offsets (not naive whitespace split, since names/Ids contain spaces/dots). **Put the parsing function itself in a platform-*un*tagged file taking a plain `string`** (only the `exec.Command` call needs the `windows` tag) — this is what makes it unit-testable on the Linux CI runner via fixture text, mirroring how Debian's own regex-parsing is already separated from its exec call.
  - No security/severity signal exists in winget output at all (unlike apt's `-security` pocket) — `UpgradableSecurity`/`PackageUpgrade.Security` are always `0`/`false` for v1. Call this out prominently in the README's Windows section.
  - **Risk to flag, not gloss over**: winget's table format/column set has changed across App Installer versions, and winget itself may be absent entirely on locked-down/enterprise or Server Windows machines. Detect absence via the exec error and surface as a `Status.Errors` entry (same fallback-to-previous-value posture every other subsystem here already uses); if the header row doesn't contain the expected column names, fail that cycle with a descriptive error rather than parsing garbage.
- **Reboot-required** (`reboot.go`): `golang.org/x/sys/windows/registry` (new dependency), check in order: `HKLM\...\Component Based Servicing\RebootPending` (key existence alone = pending), `HKLM\...\WindowsUpdate\Auto Update\RebootRequired` (same), `HKLM\SYSTEM\CurrentControlSet\Control\Session Manager` value `PendingFileRenameOperations` (non-empty `REG_MULTI_SZ` = pending). All are world-readable HKLM reads, no admin privilege or exec needed — the most reliable part of this whole plugin. `RebootRequiredPackages` stays empty (no Windows analog), same as Debian's own gap there.
- `Checker.Platform()` returns `"windows"`.

### 4. Companion apply seam (`internal/companion`)

New `internal/companion/applier.go`:

```go
type Applier interface {
    Packages(ctx context.Context, names []string) (output string, err error)
    Upgrade(ctx context.Context) (output string, err error)
    FullUpgrade(ctx context.Context) (output string, err error)
}
var defaultApplier Applier
func registerApplier(a Applier) { /* panics on duplicate */ }
```

`internal/companion/applier_apt.go` (`!windows`): extracts today's exact apt-get logic out of `execute.go` verbatim — including moving the `apt-get update` prologue and `apt-get autoremove` epilogue *into* this Applier (they're apt-specific in shape, not just reason; winget has no refresh-sources or orphan-cleanup equivalent, so they must be opt-in per-Applier, not forced shared code in `Apply`).

`internal/companion/applier_winget.go` (`windows`): `Packages` loops `winget upgrade --id <name> --silent ...` once per package (winget has no batch form); `Upgrade` runs `winget upgrade --all --silent ...`; `FullUpgrade` calls the same as `Upgrade` — **winget has no dist-upgrade/upgrade distinction**, flagged explicitly as a real semantic gap (`ActionFullUpgrade` behaves identically to `ActionUpgrade` on Windows), not silently hidden.

`execute.go`'s `Apply` loses its apt-specific switch, replaced with a 3-way dispatch to `defaultApplier.Packages/Upgrade/FullUpgrade`. The package-validation step (`missingFromPending`, comparing requested names against the agent's own last-known `/status`) stays exactly where it is — genuinely platform-agnostic already.

**Trade-off rejected**: an `Applier` method taking the whole `aggregator.Action` (letting each Applier interpret type strings itself) — rejected because three explicit methods make the winget upgrade/full-upgrade gap a visible one-line fact in the implementation, and keeps `missingFromPending` (which only applies to `ActionPackages`) in shared code rather than duplicated per Applier.

### 5. Companion self-update seam

- `deploykind.go`: add `DeployWindowsService` to the `DeployKind` enum (harmless everywhere, it's just an `int`). Split the native-detection body: `deploykind_unix.go` (`!windows`, today's `nativeUnitPresent` systemd-file-stat body) / `deploykind_windows.go` (`windows`, `golang.org/x/sys/windows/svc/mgr.OpenService` — service names match the existing systemd-unit-name convention 1:1, so `componentUnitName` needs zero change). `Detect`/`Detection`/`Kind()`/`Ambiguous()` stay shared/untagged.
- `selfupdate.go`: split `installNative`'s body — `selfupdate_unix.go` (today's exact `sh install.sh` reinvocation + `/etc/default/<name>` env-file read/write, verbatim) / `selfupdate_windows.go` (`cmd /c install.bat`, passing `INSTALL_COMPONENTS`/`INSTALL_VERSION` as env vars, same convention as the Unix side's own `INSTALL_COMPONENTS`/`INSTALL_VERSION` — **`.bat`, not `.ps1` as originally planned below**: `cmd.exe` runs a `.bat` directly, while a `.ps1` run via `powershell -File` is blocked outright on any host still at PowerShell's default Restricted ExecutionPolicy unless every invocation also passes `-ExecutionPolicy Bypass`, and this gets re-invoked non-interactively with no operator present to grant an exception). A Windows Service restart equivalent to `systemctl restart`, invoked *inside* `install.bat` the same indirection the Linux path already uses. `SelfUpdate`'s shared dispatch routes both `DeployNative` and `DeployWindowsService` through the (now platform-split) `installNative`.
  - **Resolved** (was an open question here): a Windows Service self-update preserves existing config across reinstall via the service's own registry `Environment` (`REG_MULTI_SZ`) value — the exact mechanism recommended below, just read directly from Go via `golang.org/x/sys/windows/registry` (already a dependency) rather than through a separate flat file. `selfupdate_windows.go` gained its own `existingConfigEnv(component)`, mirroring `selfupdate_unix.go`'s function of the same name/signature exactly (same translation tables for the aggregator's `AGGREGATOR_*`-prefixed inputs vs. its plain-named registry values) — `passThrough`/`translate` were hoisted into the shared `selfupdate.go` so both platform files reuse the identical logic. The companion itself has no preserved config either way (its own Environment is rebuilt fresh from rediscovered agent info on every `install.bat` run, matching the Unix side's `default: return nil` case).
- `internal/companiontoken`: build-tag split for the identity-handoff transport — `!windows` keeps `net.Listen("unix", path)`, `windows` uses `github.com/Microsoft/go-winio`'s named pipes (`winio.ListenPipe`/`DialPipe` — same library Docker/containerd use for this exact purpose on Windows). Same `Server`/`Serve`/`Close` surface either way; callers in both `main.go`s need no change.

### 6. Build/ship

- `.github/workflows/release.yml`: added one more `GOOS=windows GOARCH=amd64` iteration to the existing native-binary cross-compile step (Go cross-compiles natively, no QEMU needed, same as the existing arm64 comment already notes) — output `update-detector.exe`/`update-aggregator.exe`/`update-detector-companion.exe` → published as `update-detector-windows-amd64.exe` / `update-aggregator-windows-amd64.exe` / `update-detector-companion-windows-amd64.exe`. **Deviation from this plan's original recommendation to skip `update-aggregator` for Windows**: published anyway, for full parity with `install.bat` supporting all three components (per the `INSTALL_COMPONENTS=aggregator,agent,companion` design actually implemented) rather than a companion-only, detection-focused scope — the extra cross-compile is one more loop iteration, negligible cost, even though running the aggregator on Windows is expected to be rare. No Windows Docker image (Windows containers need a Windows container host, categorically out of scope — Docker Desktop's Windows mode is still a Linux VM, per the README's existing platform-limitations note).
- `install.bat` (repo root, alongside `install.sh` — **not `install.ps1`** as originally planned above, see selfupdate_windows.go's own note on why): Administrator-elevation check (`net session`), amd64-only assertion, download+atomic-replace of the `.exe` from the same GitHub releases API `install.sh` already uses (a `.new` + `move`, same atomicity idiom), install as a Windows Service (`sc create`/`sc config`, `start= auto`, `sc failure ... actions= restart/5000` as the closest native equivalent of `Restart=on-failure`/`RestartSec=5` — `LocalSystem` by default, including for the companion, which needs real elevated rights for `winget upgrade` the way the Linux companion needs root for `apt-get`; **known unresolved limitation**: `winget` is widely reported to not work correctly when run as `SYSTEM` at all, see README's platform-limitations section for the workaround), write the service's own registry `Environment` value (`reg add ... /v Environment /t REG_MULTI_SZ`, resolved above), cache itself for later self-update reinvocation, and an uninstall path (`install.bat --uninstall`) mirroring `install.sh --uninstall`. JSON parsing (resolving a release asset's download URL) and one-off registry reads use ad-hoc `powershell -Command` one-liners internally — never a `-File` load of an actual `.ps1`, so this doesn't reintroduce the ExecutionPolicy problem the `.bat` choice was meant to avoid. No Docker-discovery logic at all (unlike `install.sh`'s companion install, which checks for either a native or a containerized agent) — `winget` applies to the Windows host itself, so a Windows companion only ever makes sense paired with a host-native Windows agent.

## Sequencing (each phase independently mergeable, working state)

**Phase 1 — Registry + refactor ubuntu/debian onto it, zero behavior change.**
`internal/checker/registry.go`, `init()` registration + `!windows` tags on ubuntu/debian, `config.Config.CheckerFields()`, `main.go` split into `platforms_unix.go`/`platforms_windows.go` (windows side can be a no-op stub for now), `hostflavor` split. Tests: existing ubuntu/debian tests pass unchanged; add `checker.New("ubuntu", fields)`/`checker.New("bogus", ...)` registry tests; table-driven test asserting every `CheckerFields()` key round-trips correctly (mitigates the real risk here: a copy-paste field-name mismatch silently breaking a config field like `AptSourcesListD`).

**Phase 2 — Windows checker, no companion changes. Done.**
`internal/checker/windows/*`, `golang.org/x/sys/windows/registry` dependency, wired into `platforms_windows.go`. Tests: winget-table-parsing logic is fixture-testable cross-platform (parsing func untagged, per design above) and covered; registry-key reboot detection and real `winget` invocation are exercised by an actual `windows-latest` GitHub Actions runner (`.github/workflows/windows-test.yml`, added alongside this phase) rather than being an accepted gap as originally planned here -- **a Windows CI runner is now available** (this repo's CI/release pipeline lives on GitHub Actions, which has hosted Windows runners). That said, the GitHub runner's own registry state is whatever a fresh Windows Server image happens to have, so it only ever exercises the "nothing pending" branch of `checkRebootRequired` for real, and it's unknown whether `winget` is even present/functional on that image until actually confirmed -- genuine end-to-end verification against a real user's Windows machine (with real pending updates, real winget config) is still a manual, unverified gap.

**Phase 3 — Companion apply seam + Windows applier + Windows Service self-update. Done, with real regressions found and fixed after the fact.**
`applier.go`+`applier_apt.go`+`applier_winget.go`, `execute.go` refactored to dispatch through `defaultApplier`, `deploykind.go`/`selfupdate.go` splits, `companiontoken` named-pipe transport. The initial commit for this phase claimed "zero behavior change... extracted verbatim" but actually introduced 4 real regressions on the existing Linux behavior (a from-scratch `existingConfigEnv` rewrite that silently dropped/mistranslated preserved config fields, a recheck-on-failure regression when `apt-get update` itself fails, a missing `!windows` build tag on an unrelated streaming test that broke `go vet` on Windows, and a `Kind()` that could never actually return the just-added `DeployWindowsService` value) — caught only by independently re-running the full test suite rather than trusting the commit message or a "CI passed" report that turned out to only cover a workflow leg that excludes `internal/companion` entirely. All four fixed and verified (full Linux suite green, `GOOS=windows GOARCH=amd64 go build && go vet` green) in a follow-up commit. Nothing here can be end-to-end verified against a real Windows Service without a Windows environment; the Go-level dispatch logic is unit-testable, the actual `winget`/`sc.exe` invocations are not.

**Phase 4 — Build/ship. Done, `install.bat` not `install.ps1` (see section 6).**
`release.yml` windows/amd64 legs (low risk, additive — now publishing all three binaries, not just two, see section 6's noted deviation), `install.bat` (can't be validated in this repo's CI at all — static review only, plus manual verification on a real/VM Windows host still needed before calling this genuinely done, same category of gap as Phase 2/3's own Windows runtime behavior). Sequenced last deliberately, so any config-persistence/named-pipe design revisions surface after the Go-side plugin seam is already stable, not entangled with it.

## Verification

- Phases 1-3: `go build ./...`, `go vet ./...`, `go test ./...` on the existing Linux runner after each phase — the bar is "ubuntu/debian/companion-apt behavior is provably unchanged" via the table-driven/command-sequence tests called out above, plus new registry/plugin-dispatch unit tests.
- Windows-specific logic (winget table parsing, registry key format assumptions) is verified via fixture-based unit tests where the parsing is separated from the exec/registry call (per the file-layout choice above) — this is the only Windows-specific coverage possible without a Windows CI runner.
- Phase 2/3's actual Windows runtime behavior (real winget output, real registry reads, real Windows Service install/restart) requires manual verification on a real or VM-hosted Windows machine — flagged as a known gap, not silently skipped, given this repo currently has no Windows CI runner.

## Phase 5 — real Windows Update as the primary signal. Done for the Windows Update piece; multi-package-manager plugins still not started

Findings from the first real Windows install (Phases 1-4 above only ever ran against a hosted CI runner or fixture tests before this): the binaries never implemented the Windows Service Control Protocol at all (fixed -- see `internal/winsvc`, `cmd/*/start_windows.go`), and `winget` genuinely doesn't exist for `LocalSystem` (fixed as an opt-in account reconfiguration in `install.bat`, not by making winget mandatory).

Reprioritization from that same live install: winget was never meant to be this checker's primary signal, actual Windows Update (OS-level updates, with real MSRC severity ratings, unlike winget's total lack of one) is. **Implemented**: `internal/checker/windows/windowsupdate.go` (`//go:build windows`) shells a `powershell -Command` one-liner driving the Windows Update Agent API (`Microsoft.Update.Session`/`CreateUpdateSearcher().Search("IsInstalled=0 and IsHidden=0")`) and emits compact JSON via `ConvertTo-Json -InputObject` (never piped -- piping a 0-or-1-element collection to `ConvertTo-Json` silently collapses it to `null`/a bare object instead of an array); `windowsupdate_parse.go` (untagged, fixture-testable) parses it into the same `packageResult` shape `packages_parse.go`'s winget parsing already produces, counting anything with a non-empty `MsrcSeverity` as security. `windows.go`'s `Check` now queries both Windows Update (required -- a failure is a real error) and winget (still optional -- `ErrWingetNotFound` stays silent) and merges whichever succeeded via a new `mergePackageResult`, falling back to the previous cycle's value only if *both* failed. Not yet confirmed live: whether this actually works under `LocalSystem` without the account workaround winget needs (expected to, since the Windows Update service is system-level rather than tied to a per-user package registration the way winget.exe's App Execution Alias is, but unconfirmed).

**Apply side, also implemented**: `internal/companion/applier_windows.go` extends the same COM API to actually install updates via `IUpdateDownloader.Download()`/`IUpdateInstaller.Install()`, not just detect them -- `windowsApplier` is now the sole registered Applier on this platform (`wingetApplier`'s own `init()`/registration was removed; `windowsApplier` delegates to it by value for names that don't carry a `(KBnnnnnnn)` marker). `splitPackageNames` (untagged, fixture-tested) does the KB-vs-winget name split; `runWindowsUpdateInstall`, `installWindowsUpdatesByKB`, `installAllPendingWindowsUpdates` (all `//go:build windows`) are not fixture-testable at all, since there's no way to exercise the actual COM download/install flow without a real Windows host. This is the least-verified code in the entire Windows implementation -- every other piece is read-only detection; this one actually modifies the system and can require a reboot to take effect.

Still not started: Scoop/Chocolatey as alternative Windows package sources, a Homebrew-based macOS checker, and Docker image update (tag/digest drift) detection on Linux.
