# Fleet dashboard (Homepage) via update-aggregator

For a fleet of hosts, `update-aggregator` (a second, separate binary/image,
`Dockerfile.aggregator` / `docker-compose.aggregator.yml`) is a small central
service that agents push their status to, so [Homepage](https://gethomepage.dev)
can show one summary widget plus one card per host. It runs once, wherever
you like (e.g. next to Homepage) — unlike the agent, it's not per-host.

**Why push, not pull:** the aggregator never needs to reach into any agent's
network — each agent connects *out* to the aggregator, so this works even
across NAT/firewalls the aggregator couldn't otherwise reach through.

**Enrollment & approval:** on first run with `AGGREGATOR_URL` set, an agent
generates a random ID + token (`AGENT_IDENTITY_FILE`, persisted so restarts
don't re-enroll as a new agent) and announces itself with its claimed
hostname. The aggregator holds it as `pending` until you approve or reject
it on its `/admin` page — that's the actual trust decision; the token just
means one agent's credential leaking doesn't expose others. Pushing to the
aggregator is purely additive: local `/status`, Gatus, and Telegram all keep
working even if the aggregator is unreachable or the agent isn't approved
yet.

```sh
docker compose -f docker-compose.aggregator.yml up -d
# on each host's agent:
export AGGREGATOR_URL=http://aggregator-host:9090
docker compose up -d
# then open http://aggregator-host:9090/admin and approve the new host
```

Homepage "Custom API" widgets:

```yaml
- Fleet status:
    - Update Detector:
        widget:
          type: customapi
          url: http://aggregator-host:9090/widgets/summary
          mappings:
            - field: hosts_ok
              label: Hosts OK
            - field: packages_upgradable_security
              label: Security updates pending
- web01:
    - Update Detector:
        widget:
          type: customapi
          url: http://aggregator-host:9090/widgets/hosts/web01
          mappings:
            - field: packages.upgradable_total
              label: Upgradable
            - field: reboot_required
              label: Reboot required
```

The per-host widget URL (`/widgets/hosts/{hostname}`) returns the exact same
JSON shape as an agent's own `/status`, so the mapping is identical whether
Homepage points at the aggregator or straight at an agent.

For "what actually needs updating" across the whole fleet in one call, use
`GET /widgets/packages` — flattens every approved host's pending package
upgrades into one list (`[{hostname, name, current_version,
candidate_version, security}, ...]`); add `?security=true` to only list
security updates. `security` is best-effort per-package (derived from
`-security` appearing in the package's origin/pocket) — the authoritative
count per host is still `packages.upgradable_security`.

**Known limitations:** the `/admin` page and `/widgets/*` endpoints have no
authentication of their own — same trust model as the rest of this project
(agent `/status`, Gatus polling): keep the aggregator on a private network or
put it behind your own reverse-proxy auth if it's reachable beyond that.
`/widgets/hosts/{hostname}` picks the most-recently-seen approved agent if
two share a hostname — set a unique `HOSTNAME_OVERRIDE` per host to avoid
that ambiguity.

Back to [README](../../README.md).
