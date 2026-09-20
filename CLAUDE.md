# CLAUDE.md

This file is for Claude Code. `AGENTS.md` is the source of truth — this is a verbatim mirror for `claude.ai` / `CLAUDE.md` discovery.

<!-- BEGIN AGENTS.md -->
# AGENTS.md

## Commands

```sh
go test ./...   # unit tests only (parsing/diff/registry fixtures, no Docker/apt)
go vet ./...
go build ./...

# Windows: internal/companion tests use Linux fixtures (apt/systemd/Unix sockets),
# still excluded there — the package itself builds on both (apt vs Windows Update appliers)
go test $(go list ./... | grep -v '/internal/companion$') -v

# Docker images (both need Ubuntu base for apt-check)
docker build -t update-detector -f Dockerfile .
docker build -t update-aggregator -f Dockerfile.aggregator .

# OpenAPI lint
npx @redocly/cli lint openapi/update-detector.yaml openapi/update-aggregator.yaml

# Cross-compile — always inject version
go build -ldflags "-X update-detector/internal/version.Version=$TAG" -o bin/update-detector ./cmd/update-detector
GOOS=windows GOARCH=amd64 go build -ldflags "-X update-detector/internal/version.Version=$TAG" -o bin/update-detector.exe ./cmd/update-detector
# same pattern for ./cmd/update-aggregator and ./cmd/update-detector-companion (GOARCH=arm64 for Pi 4B)
```

CI: GitHub is primary — `.github/workflows/ci.yml` (`go build` → `go vet` → `go test` on `ubuntu-latest`/`windows-latest`, with `internal/companion` tests excluded on Windows). `.github/workflows/release.yml` on a `v*` tag builds multi-arch images + 9 binary assets, and pushes channel-specific tags per `internal/version`'s alpha < beta < rc < release convention (`:latest-alpha`/`:latest-beta`/`:latest-rc`, plus plain `:latest` only for a real release) so `docker compose pull` can track a channel without pinning a version; `retag-latest.yml` (manual `workflow_dispatch`) backfills those channel tags for older releases that predate this. No Makefile, no golangci-lint, no pre-commit.

> **Remotes:** `origin` is a legacy remote — ignore it. Push/pull and releases are on **GitHub only** (`github` → `github.com/sinwe/update-detector`; no `git push origin`).

## Architecture

Three binaries, three entrypoints:
- `cmd/update-detector` — agent daemon, polls host for updates, serves `GET /status`, pushes to aggregator
- `cmd/update-aggregator` — central dashboard/registry (`/admin`), holds SSE connections to companions
- `cmd/update-detector-companion` — host-native privileged process, receives `apply`/`recheck` over SSE, validates against `GET /status` before executing

Data flow: `agent --HTTP push--> aggregator <--SSE-- companion --GET /status--> agent`; companion streams stdout back via SSE. Trust-on-first-contact enrollment (Pending → Approved on `/admin`). Companion re-validates every `packages` action against pending upgrades in `internal/companion/execute.go:Apply` — never arbitrary exec (apt-get on Linux; Windows Update COM via PowerShell on Windows, winget code exists but is unsupported per `docs/reference.md`).

Key packages:
- `internal/checker` — `Checker` interface, `Fields map[string]string` registry, `Status`/`PackageInfo` types
- `internal/checker/{ubuntu,debian,windows}` — platform checkers (Windows: Windows Update primary, winget supplementary-but-unsupported)
- `internal/hostflavor` — detects `ID` from `/host/etc/os-release` to select checker
- `internal/companion` — `Applier` interface (`applier.go`), `apt` vs Windows Update appliers, self-update, output streaming
- `internal/config` / `internal/aggregatorconfig` — env-based config with host-mount defaults
- `internal/agentstream` — SSE client used by both agent and companion (exactly one connection/host, server-side arbitration in `internal/aggregator`'s CompanionHub)
- `internal/notifier` / `internal/state` / `internal/version` — Telegram fanning, diff/persistence, `Version` var via ldflags

## Platform / Build Tags

- `//go:build !windows` vs `//go:build windows` splits all OS-specific code. Keep platform files thin (only `exec.Command`); put parsing in tag-free files with fixture tests.
- Never import platform checker packages directly in `main.go`. Register via `init()` (`checker.Register`, `registerApplier`). Wiring is in `cmd/update-detector/platforms_unix.go` (blank-imports `ubuntu`+`debian`) and `platforms_windows.go` (blank-imports `windows`) — `checker.New()` selects at runtime.
- Config → checker bridge is `checker.Fields` (`map[string]string`), not typed structs, to avoid circular imports. `config.Config.CheckerFields()` populates all keys; unused keys are ignored.
- Companion token handoff is OS-split: Unix socket (`token_unix.go`) vs named pipe (`token_windows.go` via `go-winio`).

## Conventions

- Adding a checker: new subpackage under `internal/checker/<name>`, implement `Checker`, `checker.Register` in `init()`, add blank import to matching `platforms_*.go`.
- Adding a notifier: implement `Notifier` (`internal/notifier/notifier.go`), wire in `cmd/update-detector/main.go:run()` gated by env var.
- Tests are fixture-based; no Docker/apt/services required for `go test ./...`. E2E needs real Ubuntu host with bind-mounted `/host/etc/apt`, `/host/var/lib/dpkg/status`, etc.
- Version is `internal/version.Version` default `"dev"`; release workflow injects tag via `-ldflags -X`. Never hardcode versions.
- OpenAPI specs at `openapi/*.yaml` are the single source of truth (embedded via `openapi/openapi.go`, served live at `GET /openapi.yaml`) — keep them in sync with handlers.
- Go 1.22 (`go.mod:3`). All env config has defaults matching `docker-compose.yml` mounts (`/host/...` read-only, `/var/lib/update-detector/...` writable).
<!-- END AGENTS.md -->
