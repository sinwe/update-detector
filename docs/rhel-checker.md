# RHEL-family checker (feature/rhel-checker)

Branch: `feature/rhel-checker` (tag `v2.0.0-alpha1`)

## Scope

Add support for RHEL-family Linux hosts (RHEL, Rocky, AlmaLinux, CentOS
Stream, Fedora, Oracle Linux) to the agent and companion, mirroring the
existing Ubuntu/Debian path but using `dnf` instead of `apt`.

The aggregator is unchanged.

## Agent detection (`internal/checker/rhel`)

- Registered as `"rhel"` via `checker.Register`.
- `hostflavor` maps `fedora`, `rhel`, `centos`, `rocky`, `almalinux`, `ol`
  to `"rhel"` (default remains `ubuntu` for unknown IDs).
- `Check` runs `dnf check-update -q` (exit 100 = updates available) and
  parses `name.arch  version  repo` lines; arch suffix is stripped.
- Security classification via a parallel `dnf check-update -q --security`.
- OS version read from `/etc/os-release` (`VERSION_ID`).
- Wired into `platforms_unix.go` via blank import.

## Companion apply (`internal/companion/applier_dnf.go`)

- Implements `Applier` (`Packages`, `Upgrade`, `FullUpgrade`) using `dnf`.
- Registered at runtime only when `dnf` is on `PATH`; `applier_apt.go`
  skips registration in that case to avoid a duplicate-registration panic.

## Status

- [x] Checker scaffold + hostflavor mapping
- [x] dnf applier scaffold
- [x] OS version + security detection
- [x] Reboot detection (`needs-restarting`)
- [ ] CVE integration
- [x] Installer (`install.sh`) dnf-aware native path
- [ ] Fixture-based parsing tests
