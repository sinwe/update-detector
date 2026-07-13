# Integrations

One file per integration, each self-contained:

- [gatus.md](gatus.md) — poll `/status` for uptime-style monitoring, `ok` vs. per-field conditions, sample response.
- [homepage.md](homepage.md) — fleet-wide dashboard via `update-aggregator`: enrollment/approval, Homepage "Custom API" widgets, the fleet-wide pending-packages endpoint.

Adding a new one? Follow the same pattern: a new `docs/integrations/<name>.md`, linked both from here and from the relevant section in the top-level [README](../../README.md).
