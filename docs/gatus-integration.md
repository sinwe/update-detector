# Gatus integration

```yaml
endpoints:
  - name: web01-updates
    url: "http://web01:8080/status"
    interval: 1h
    conditions:
      - "[STATUS] == 200"
      - "[BODY].ok == true"
```

`ok` is a convenience boolean: no security updates, no reboot pending, no OS
upgrade available. For finer-grained alerting, target the individual fields
instead, e.g.:

```yaml
    conditions:
      - "[BODY].packages.upgradable_security == 0"
      - "[BODY].reboot_required == false"
      - "[BODY].os.update_available == false"
```

Example `/status` response:

```json
{
  "hostname": "web01",
  "platform": "ubuntu",
  "checked_at": "2026-07-05T10:00:00Z",
  "reboot_required": false,
  "os": { "current_version": "22.04", "update_available": false },
  "packages": {
    "upgradable_total": 5,
    "upgradable_security": 2,
    "upgrades": [
      { "name": "curl", "current_version": "7.81.0-1ubuntu1.15", "candidate_version": "7.81.0-1ubuntu1.16" },
      { "name": "openssl", "current_version": "3.0.2-0ubuntu1.15", "candidate_version": "3.0.2-0ubuntu1.16" }
    ]
  },
  "ok": false
}
```

`GET /healthz` reports process liveness only (independent of update state),
so it won't get confused with "the host needs patching" — use it for
Docker's own health checking if desired.

Back to [README](../README.md).
