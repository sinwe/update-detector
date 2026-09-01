# CLAUDE.md

This file is for Claude Code. `AGENTS.md` is the source of truth — this is a verbatim mirror for `claude.ai` / `CLAUDE.md` discovery.

<!-- BEGIN AGENTS.md -->
# AGENTS.md

## Commands

```sh
go test ./...   # unit tests only (parsing/diff/registry fixtures, no Docker/apt)
go vet ./...
go build ./...

# Windows: internal/companion is Linux-only (apt/systemd/Unix sockets), exclude it
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

CI: GitHub is primary — `.github/workflows/windows-test.yml` (`go build` → `go vet` → `go test` on `1.22`, with `internal/companion` excluded on Windows). `.forgejo/workflows/` is legacy, do not use. Release on `v*` tag builds multi-arch images + 9 binary assets. No Makefile, no golangci-lint, no pre-commit.

> **Remotes:** `origin` still points to Forgejo (`forgejo.winar.to`), `github` points to `github.com/sinwe/update-detector`. Push/pull and releases are on **GitHub only** — never push to `origin`/Forgejo (no `git push origin`, no Forgejo registry/API).

## Architecture

Three binaries, three entrypoints:
- `cmd/update-detector` — agent daemon, polls host for updates, serves `GET /status`, pushes to aggregator
- `cmd/update-aggregator` — central dashboard/registry (`/admin`), holds SSE connections to companions
- `cmd/update-detector-companion` — host-native privileged process, receives `apply`/`recheck` over SSE, validates against `GET /status` before executing

Data flow: `agent --HTTP push--> aggregator <--SSE-- companion --GET /status--> agent`; companion streams stdout back via SSE. Trust-on-first-contact enrollment (Pending → Approved on `/admin`). Companion validates every `packages` action against pending upgrades — never arbitrary exec (`apt-get`/`winget` only).

Key packages:
- `internal/checker` — `Checker` interface (`internal/checker/checker.go:14`), `Fields map[string]string` registry, `Status`/`PackageInfo` types
- `internal/checker/{ubuntu,debian,windows}` — platform checkers
- `internal/hostflavor` — detects `ID` from `/host/etc/os-release` to select checker
- `internal/companion` — `Applier` interface, `apt` vs Windows Update/winget, self-update, output streaming
- `internal/config` / `internal/aggregatorconfig` — env-based config with host-mount defaults
- `internal/agentstream` — SSE client used by both agent and companion (single connection/host, companion preempts agent)
- `internal/notifier` / `internal/state` / `internal/version` — Telegram fanning, diff/persistence, `Version` var via ldflags

## Platform / Build Tags

- `//go:build !windows` vs `//go:build windows` splits all OS-specific code. Keep platform files thin (only `exec.Command`); put parsing in tag-free files with fixture tests.
- Never import platform checker packages directly in `main.go`. Register via `init()`:
  ```go
  checker.Register("ubuntu", factory)        // internal/checker/registry.go:30
  registerApplier("apt", factory)            // internal/companion/applier.go:46
  ```
  Wiring is in `cmd/update-detector/platforms_unix.go` (blank-imports `ubuntu`+`debian`) and `platforms_windows.go` (blank-imports `windows`) — `checker.New()` selects at runtime.
- Config → checker bridge is `checker.Fields` (`map[string]string`), not typed structs, to avoid circular imports. `config.Config.CheckerFields()` populates all keys; unused keys are ignored.
- Companion token handoff is OS-split: Unix socket (`token_unix.go`) vs named pipe (`token_windows.go` via `go-winio`).

## Conventions

- Adding a checker: new subpackage under `internal/checker/<name>`, implement `Checker`, `checker.Register` in `init()`, add blank import to matching `platforms_*.go`.
- Adding a notifier: implement `Notifier` (`internal/notifier/notifier.go:23`), wire in `cmd/update-detector/main.go:run()` gated by env var.
- Tests are fixture-based; no Docker/apt/services required for `go test ./...`. E2E needs real Ubuntu host with bind-mounted `/host/etc/apt`, `/host/var/lib/dpkg/status`, etc.
- Version is `internal/version.Version` default `"dev"`; release workflow injects tag via `-ldflags -X`. Never hardcode versions.
- OpenAPI specs at `openapi/*.yaml` are served live at `GET /openapi.yaml` — keep them in sync with handlers.
- Go 1.22 (`go.mod:3`). All env config has defaults matching `docker-compose.yml` mounts (`/host/...` read-only, `/var/lib/update-detector/...` writable).
<!-- END AGENTS.md -->
