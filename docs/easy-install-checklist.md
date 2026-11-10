# Easy-install checklist

Work items for making `install.sh` (and `install.bat`) friendlier, on
branch `feature/easy-install` (off `main` @ `a0de6f5`). Found by running
the installer on a real host and reading it end to end, 8 Nov 2026.
Check an item off when its change is committed on this branch.

## Done

- [x] **Tell the user the aggregator apply secret after an agent install**
  (`7970cac`) — new `print_aggregator_secret_note`, called at the end of
  all three agent install paths (native, Docker, macOS). Shows the
  `ADMIN_APPLY_SHARED_SECRET` value when discoverable on that host (env
  var, native `/etc/default/update-aggregator`, or a Docker aggregator's
  `.env`), otherwise says where to find it on the aggregator host.

- [x] **Fallback to wget when curl is unavailable** — added `fetch_body`,
  `download_to`, and `url_ok` helpers that prefer curl and fall back to
  wget. All download/reachability/verification call sites now use these
  helpers instead of calling curl directly. The hard requirement at the
  top now accepts either curl or wget. Verified with `bash -n`.

## Do first — small, high impact

- [x] **1. Fix the `/dev/tty` guard** — `[ -r /dev/tty ]` passes with no
  controlling terminal, then `read < /dev/tty` fails with ENXIO and
  `set -e` aborts (exit 2). Fixed: added `have_tty()` helper that
  probe-opens `/dev/tty` (not just `-r` test). All 7 prompt sites now
  use `have_tty`/`! have_tty`. Also: no-TTY Linux default changed from
  "companion only" to "agent" (companion-only is useless on a fresh host
  with no agent). Verified with `bash -n`.
- [x] **2. Add `--help` (and reject unknown flags)** — today only
  `--uninstall` is recognised; anything else, including `--help`, is
  silently ignored and the install starts. Done: `--help`/`-h` prints
  usage and exits 0 (works with and without root, since it runs before
  the root check); unknown args fail fast, exit 1. Verified live on this
  VM 8 Nov 2026; scripted install still works end to end.
- [x] **3. Verify the install actually works before declaring success**
  — Added `verify_agent` helper that waits up to 15s for `/status` (or
  `/healthz` for the aggregator) to answer, reporting the response.
  Called after every agent install path (native, macOS/launchd, Docker)
  and both aggregator installs (native + Docker). Never fatal — just
  warns if the service didn't come up. Uses `/status` first (agent
  upgradable counts) then falls back to `/healthz` (aggregator).
- [x] **4. Check the aggregator URL is reachable during agent install**
  — the companion install already does this (best-effort `curl` of
  `$agg_url/openapi.yaml` with a warning); the agent install doesn't,
  so the user only finds out from agent logs later. Done: new shared
  helper `warn_if_aggregator_unreachable`, called in all three agent
  install paths (native, Docker, macOS) right after the URL is resolved;
  same curl probe and non-fatal wording as the companion, naming the file
  holding the URL. Unreachable URL warns and the install continues
  (exit 0, service active); reachable URL stays silent. Verified live on
  this VM 8 Nov 2026.
- [x] **5. Offer Telegram setup instead of env-vars-only** — Added
  `prompt_telegram` helper that asks "Enable Telegram notifications?
  [y/N]" and prompts for bot token + chat ID if yes. Called in all
  three agent install paths (native, macOS/launchd, Docker). Skips
  silently when already configured via env or existing config, and when
  no TTY is available (non-interactive). No-TTY: env vars only.

- [x] **14. Install update-notifier-common on native hosts** — Native
  agent install now apt-installs `update-notifier-common` (best-effort,
  never fatal) when apt is available and apt-check is missing, so Ubuntu
  hosts get the authoritative security-pocket classification instead of
  falling back to parsing `apt-get -s dist-upgrade`. Found during live
  testing on Ubuntu 24.04 WSL (10 Nov 2026): apt-check was absent and
  the agent logged "apt-check: no such file or directory".

## Next — medium effort, real robustness

- [ ] **6. Generate an apply secret at aggregator-install time when none
  is set** — today an unset secret silently leaves apply disabled (501
  later). Generate one and print it, the same way the secret note now
  surfaces it.
- [ ] **7. Verify downloaded binaries** — `download_binary` curls the
  release asset and installs it with no checksum check. Needs the
  release workflow to publish checksums too.
- [ ] **8. Fix the misleading GitHub API error** — asset resolution hits
  `api.github.com` unauthenticated (60 req/hour limit), and any failure
  surfaces as "could not find asset X in release latest". Support
  `GH_TOKEN` and distinguish API failure from a genuinely missing asset.
- [ ] **9. Don't leave a partial install behind** — the `AGGREGATOR_URL`
  gate in the native/macOS agent path fires *after* the binary is
  downloaded, the system user created, and the state dir made. Validate
  the URL first, or clean up on failure.
- [ ] **10. Let uninstall offer to stop Docker deployments** — it
  deliberately never runs `docker compose down`, but it can already
  identify its own deployments via Compose labels; offer a prompted
  `down` for exactly those.

## Nice to have

- [ ] **11. A `--dry-run` mode** — print what it would do (components,
  Docker vs native, directory, URLs) without changing anything.
- [ ] **12. Show download progress** — `curl -fsSL` is fully silent; on
  a slow connection the installer looks hung after "downloading …".
- [ ] **13. Small polish** — the menu says "detector (agent) only" while
  everything else calls it "agent"; no cancel/quit choice; a re-run
  re-downloads the binary even when already on latest.

## Related findings (not install UX, noted while testing)

- Native agent install makes `AGGREGATOR_URL` fatal even though the
  agent runs standalone (aggregator push disabled).
- The whole install prints only 4 console lines, with no end summary
  (binary, service, port, env file, dashboard URL, next steps).
- The first apt check was still running after 4+ minutes on a cold VM
  and `/status` answered `{"error":"no check has completed yet"}` the
  whole time, which reads as a failure to a new user.

## Code review notes (added on rebase to main, 10 Nov 2026)

Assessment of the completed items and remaining work:

**Completed items — well implemented:**
- Items 2 and 4 are solid. `--help` runs before the root check (correct),
  unknown args fail fast, `warn_if_aggregator_unreachable` is non-fatal
  with consistent wording across all three agent paths. `print_aggregator_
  secret_note` has a thorough discovery chain (env → native env file →
  Docker compose label lookup) with a clear "where to find it" fallback.
- All three agent install paths (native, macOS/launchd, Docker) call both
  helpers consistently — good DRY.

**Potential issues in existing code:**
- ~~`warn_if_aggregator_unreachable` uses `curl`~~ — resolved: all HTTP
  helpers now fall back to wget when curl is unavailable.
- `env_value` and `docker_compose_dir_for` helpers are defined later in
  the file than their first use — works in bash (functions resolved at
  call time), but fragile for readers. Consider moving them up or
  documenting.

**Prioritization suggestions (unchanged from original review):**
1. **Item 1** (`/dev/tty` guard) — real bug, affects all 7 prompt sites,
   breaks non-interactive installs. Highest priority.
2. **Item 3** (verify install success) — `install_unit` never checks the
   service stayed up. A simple retry loop on `curl localhost:8080/status`
   would catch failures early.
3. **Item 6** (generate apply secret) — first-time aggregator installs
   silently leave apply disabled (501 later). Generate + print it.
4. **Item 7** (checksum verification) — security improvement worth doing
   before a final release.
5. **Item 9** (partial install cleanup) — AGGREGATOR_URL gate fires after
   binary download + user creation. Validate first or clean up on failure.
6. **Items 11-13** — polish, low effort, nice quality-of-life.

**Verdict:** The checklist is well-structured, the completed items are
implemented correctly, and the remaining items are legitimate follow-ups.
The branch is small and focused. Ready to merge when items 1-3 are done.
