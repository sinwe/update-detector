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

## Do first — small, high impact

- [ ] **1. Fix the `/dev/tty` guard** — `[ -r /dev/tty ]` passes with no
  controlling terminal, then `read < /dev/tty` fails with ENXIO and
  `set -e` aborts (exit 2). Affects all 7 prompt sites. Test with
  `[ -t 0 ]` or probe-open `/dev/tty` first. Also: no-TTY Linux default
  is "companion only" vs macOS "agent" — companion-only is useless on a
  fresh host with no agent.
- [x] **2. Add `--help` (and reject unknown flags)** — today only
  `--uninstall` is recognised; anything else, including `--help`, is
  silently ignored and the install starts. Done: `--help`/`-h` prints
  usage and exits 0 (works with and without root, since it runs before
  the root check); unknown args fail fast, exit 1. Verified live on this
  VM 8 Nov 2026; scripted install still works end to end.
- [ ] **3. Verify the install actually works before declaring success**
  — `install_unit` does enable + restart and never checks the service
  stayed up or that `/status` answers. Wait briefly, then `curl
  localhost:8080/status` and report the upgradable counts.
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
- [ ] **5. Offer Telegram setup instead of env-vars-only** —
  `TELEGRAM_BOT_TOKEN` / `TELEGRAM_CHAT_ID` are never prompted for; a new
  user gets no notifications and only learns why from a log line. One
  optional prompt ("want update alerts on Telegram?") would close it.

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
