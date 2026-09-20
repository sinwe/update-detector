# Roadmap

Status: `v0.15.3` — Ubuntu/Debian supported, Windows experimental,
macOS checker live on `feature/macos-checker` (Homebrew packages +
`softwareupdate` detection reporting; agent-only `install.sh` path
shipped in `v0.15.4-alpha1`). Companion apply proven live in
`v0.15.4-alpha2` (single-package apply clears the item fleet-wide;
`brew upgrade` pulls outdated deps exactly like apt). Self-update
proven live in `v0.15.4-alpha4`: the "Update agent" button carries
full config (port, URL, interval) across the update via sidecar
discovery, and the root-run companion self-updates through the same
`install.sh` path (brew itself always via `sudo -u`).

## New checker plugins (none started)

Each is a new subpackage under `internal/checker/<name>` + blank import in
the matching `cmd/update-detector/platforms_*.go` (see `AGENTS.md`).

- [ ] **Windows package-manager signal** — Windows Update covers the OS
  itself; a genuinely supported package-manager signal is still missing.
  Scoop and Chocolatey are the candidates (winget is not viable: no
  per-update severity signal, and it doesn't exist under `LocalSystem` —
  see `docs/reference.md#platform-limitations`).
- [ ] **macOS checker** — Homebrew-based (plus `softwareupdate` for OS
  updates, per the README platform table). Same reason as Windows: the
  container has no visibility into the real host, so this must be a native,
  non-containerized agent + checker implementation.
- [ ] **Docker image update detection on Linux** — tag/digest drift, a
  different kind of "update" than an OS package manager reports.

## Graduating Windows from experimental

Detection, `install.bat` install/uninstall, and companion apply/self-update
are confirmed against a real Windows host. Remaining:

- [ ] **Live KB install** — a real Windows Update install via "Apply
  selected" / "Upgrade all" is still only fixture-tested, not yet
  separately confirmed live. Start with a single low-stakes update and
  watch the live output pane.
- [ ] **Under-`LocalSystem` confirmation** — Windows Update detection is
  *expected* to work under `LocalSystem` (system-level service, unlike
  winget's per-user registration) but this is not yet separately confirmed
  live.

## Possible later (extension points, not committed)

- [ ] **More notifier channels** — Telegram is the only implementation;
  adding one (Slack, email, generic webhook, …) means implementing
  `Notifier` in `internal/notifier/notifier.go` and wiring it in
  `cmd/update-detector/main.go:run()` gated by env var.
