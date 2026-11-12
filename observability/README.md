# Local observability: Prometheus + Grafana

A local-dev stack that scrapes pulse-api's `/metrics`, evaluates alert rules,
and shows a provisioned Grafana dashboard. It is opt-in (a Compose profile),
so plain `docker compose up` still starts only pulse-api.

In staging and prod the architecture does not run this stack: an
OpenTelemetry Collector scrapes `/metrics` and exports to CloudWatch and
Cloud Monitoring (see section 9, *Observability*, in
`multi-cloud-iac-platform-architecture.md`). This stack is the
"self-hosted Grafana" option that the doc calls an optional single pane of
glass, scoped down to a laptop.

## Run it

```bash
docker compose --profile observability up --build -d
scripts/generate-traffic.sh            # 60 s of mixed traffic; DURATION=300 for longer
```

| What | URL | Notes |
| --- | --- | --- |
| Grafana dashboard | http://localhost:3000 | Opens on *pulse-api overview*. Anonymous read-only; admin login is `admin` / `admin` and forces a change. |
| Prometheus | http://localhost:9090 | Targets: `/targets`. Alerts: `/alerts`. |
| pulse-api | http://localhost:8080 | Unchanged. |

Stop with `docker compose --profile observability down` (add `-v` to drop the
stored metrics and Grafana state).

## What's in here

```
observability/
├── prometheus/
│   ├── prometheus.yml     scrape config (pulse-api every 5 s) + rule file
│   ├── alerts.yml         PulseApiDown, PulseApiHigh5xxRatio
│   └── alerts_test.yml    promtool unit tests for both rules
└── grafana/
    ├── provisioning/
    │   ├── datasources/   Prometheus datasource, fixed uid "prometheus"
    │   ├── dashboards/    file provider: loads dashboards/*.json read-only
    │   └── alerting/      intentionally empty (alerts live in Prometheus)
    └── dashboards/
        └── pulse-api.json the dashboard, as code
```

### Dashboard panels

All panels use only the three metrics pulse-api exports:
`pulse_http_requests_total{method,path,status}`,
`pulse_http_requests_in_flight` and `pulse_uptime_seconds`.

- **Scrape target**: `up`, shown as UP or DOWN.
- **API request rate**: requests per second to `/api/*`.
- **API 5xx ratio (5m)**: same expression as the 5xx alert, so the panel and the alert can't disagree.
- **Uptime**: falls back to near zero when the container restarts.
- **Requests per second by route** and **Responses by status code**: everything except `/metrics`. The scrapes would otherwise show up as a constant 0.2 req/s line.
- **API 5xx ratio** over time, with the 5% alert threshold drawn as a dashed line.
- **In-flight requests**: includes the scrape itself, so the idle floor is 1.

The dashboard has no latency panel because pulse-api has no duration
histogram yet. `internal/metrics` explains why it hand-rolls a counter and a
gauge instead of using `client_golang`. Adding a histogram means switching
to that library, and then a p95 panel is one query.

### Alerts

| Alert | Fires when | Why that shape |
| --- | --- | --- |
| `PulseApiDown` | `up == 0` for 1 minute | Covers a crash, a hang and a broken network path with one rule. |
| `PulseApiHigh5xxRatio` | 5xx share of `/api/*` > 5% for 5 minutes | Probes and scrapes are excluded so they can't dilute a real error spike, or cause one when `/readyz` returns 503. |

Run the rule tests (no running stack needed):

```bash
docker run --rm -v "$PWD/observability/prometheus:/rules:ro" -w /rules \
  --entrypoint promtool prom/prometheus:v3.5.0 test rules alerts_test.yml
```

The tests cover firing after the `for` window, staying quiet while pending,
and ignoring `/readyz` 503s. No Alertmanager is configured: alerts show in
Prometheus at `/alerts` but aren't routed anywhere. Routing belongs to the
cloud alerting in Milestone 19.

## Design choices

- **Dashboards and datasources are provisioned from git with UI edits refused.**
  The JSON in this folder is the only source of truth, the same rule the
  repo applies to infrastructure with Terraform.
- **Ports are bound to 127.0.0.1.** Prometheus has no authentication, and
  Grafana allows anonymous viewing here.
- **Images are pinned** (`prom/prometheus:v3.5.0`, `grafana/grafana:12.1.0`),
  matching how the rest of the repo pins its versions.
- **All capabilities are dropped and `no-new-privileges` is set**, the same
  hardening as pulse-api. Both images already run as non-root users.
