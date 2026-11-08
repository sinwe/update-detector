# CVE / vulnerability detection (planned)

This document tracks the design and rollout of OS-level CVE detection
for update-detector. It is a **separate concern** from the existing
"pending package updates" check: a host can be fully up-to-date on
packages and still carry a critical CVE in an installed library that has
no fix yet, or one whose fix hasn't been applied.

## Status

- Branch: `feature/cve-detection`
- Implemented: Debian/Ubuntu detector via `debsecan` (see
  `internal/cve/debian`), wired into both `ubuntu` and `debian` checkers,
  surfaced in `Status.Vulnerabilities`, and folded into `ComputeOK`.
- Not yet: macOS (Homebrew), Windows, aggregator/UI surfacing, Telegram
  alerting, companion apply.

## Architecture

CVE detection reuses the existing plugin registries:

- `internal/cve` defines `Detector` + `Register`/`New`, mirroring
  `internal/checker`. Each platform registers a detector in its own
  `init()`.
- `checker.Status` gained a `Vulnerabilities VulnerabilityInfo` field
  (counts by severity + detail items). `ComputeOK` now also requires
  zero critical/high CVEs.
- Each platform checker constructs its detector in `New()` (best-effort:
  a missing scanner or registration failure leaves the detector nil, and
  detection is skipped rather than reported as "clean") and calls it in
  `Check()`, preserving the previous cycle's value on failure.
- Config: `CVE_COMMAND` (default `debsecan`) and `CVE_SUITE` (default
  auto-detect). Set `CVE_COMMAND=` (empty) to disable.

## Why a scanner, not a feed

Parsing distro CVE feeds (Ubuntu USN, Debian security tracker) directly
would duplicate what `debsecan`/`osv-scanner` already do and would need
network + caching logic. Shelling out to a maintained scanner keeps this
project's "no services, fixture-tested" posture and lets each platform
pick the best available tool.

## Rollout plan

1. **Linux (Ubuntu/Debian)** — done (debsecan). Verify on a real host
   and tune severity parsing against actual debsecan output.
2. **macOS (Homebrew)** — use `brew audit` / `osv-scanner` against the
   installed formula list; register under `macos`. macOS has no native
   CVE feed, so osv-scanner (Google) is the pragmatic choice.
3. **Windows** — map Windows Update's "security" classification is
   already covered by the existing checker; CVE-level needs the MSRC API
   or `osv-scanner` on installed software. Start with osv-scanner for
   parity with macOS.
4. **Surfacing** — add `vulnerabilities.*` to the aggregator widgets and
   `/widgets/hosts/{host}` (already flows through since it mirrors
   `/status`), add a Telegram alert when critical/high CVEs appear or
   increase, and an `OK`-style convenience.
5. **Apply** — CVEs are fixed by upgrading packages, which the companion
   already does; no new apply path needed. Optionally auto-trigger a
   recheck after apply.

## Open questions

- debsecan requires network to refresh its cache. Should we cache the
  last successful result (already done via previous-preservation) or
  also persist to state.json? Current code preserves previous on failure,
  which matches the existing subsystem posture.
- Severity thresholds for alerting: alert on new critical/high only, or
  any change? Probably "new since last check" to avoid spam.
- Should `ComputeOK` include medium/low? Currently only critical/high
  fail OK, matching the user's "critical and high" requirement.
