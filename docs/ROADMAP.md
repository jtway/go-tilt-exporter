# Brewery telemetry — architecture and roadmap

Status: **draft for review**, written 2026-10-20 after the MQTT + tracing phase shipped.
Owner: Josh. This file is the long-term plan; the README describes what exists today.

## Where we are (2026-10-20)

```
Tilt ──BLE──▶ ESP32 tilt-receiver ──MQTT──▶ Mosquitto (k3s, 192.168.1.241)
                                                │  brewery/tilt/v1/<color>   (retained)
                                                ▼
                         go-tilt-exporter on the Pi (systemd, source.type=mqtt)
                           ├─ Brewfather API: list batches, read Tilt bindings
                           ├─ Brewfather custom stream: POST every 15 min
                           ├─ Prometheus :9100  (scraped into VictoriaMetrics)
                           └─ OTLP traces ──▶ otlp.lab.jtway.com ──▶ Tempo
```

### Handoff — resume here

- **Branches:**
  - `go-tilt-exporter` `feature/mqtt-otel` (pushed): MQTT source, tracing, systemd unit, ESPHome configs.
  - `homelab-pulumi` `feature/mosquitto`: the broker. Merge it into `feature/in-cluster-observability`.
- **Deployed:**
  - Mosquitto in namespace `mqtt`, behind the 1Password item `Mosquitto` (fields `TILT_RECEIVER_PASSWORD` and `BREWTRACKER_PASSWORD`).
  - ESP32 `tilt-receiver`, still on the *test* build. Flash `esphome/tilt-receiver.yaml` to finish.
  - Pi: systemd unit `tilt-exporter`, config in `/etc/tilt-exporter/{config.yaml,env}`, `source.type: mqtt`.
- **Pi quirks:**
  - `otlp.lab.jtway.com` resolves through `/etc/hosts`, because the Pi's resolver goes to Google DNS, not AdGuard.
  - Go is at `/usr/local/go` (1.27.3).
- **Vault:** `Tway AI/memory/domain/brewery-panel.md`, the ESP32 item, has the dated history.

## Problems to solve next (confirmed in code)

1. **Finished batches keep reporting.** There are two causes, and both need fixing:
   - `refreshBatches` only replaces the batch list when the new list is non-empty
     (`len(batches) > 0`). When the *last* fermenting batch moves to Conditioning,
     Brewfather returns zero active batches and the stale one is kept forever.
     That guard was in the original code and I preserved it in the MQTT work.
   - Prometheus gauges are never deleted. Even after a batch drops out, its last
     `brewtracker_gravity_reading{id=…}` value is exported until the process restarts.
2. **Tilt values only exist as batch-scoped series.** Gravity and temperature are labelled with the
   batch id and name, so a Tilt that isn't attached to a batch has no gravity series at all. A
   batch rename also breaks series continuity.
3. **Batch polling is wasteful and slow to notice changes.** Every 15 minutes it lists *all* batches,
   paging 10 at a time, then fetches each fermenting batch. The API supports `?status=` and
   `include=` filters, which would replace most of those calls.

## Design principles

- **The broker is the contract.** Services only meet through versioned topics and payloads.
  Nothing calls another service directly, so splitting the code into processes later is a
  deployment change, not a rewrite.
- **Device data is batch-agnostic; batch context is joined on.** Receivers publish what a sensor
  read. Which batch the reading belongs to is separate state, owned by one component and
  published to the broker.
- **One owner per external system.** Exactly one component talks to the Brewfather API. It has
  a 500 calls/hour limit, and its batch state should have a single source of truth.
- **Modular monolith first, microservices when a seam earns it.** Build the components as
  separate packages with their own MQTT subscriptions inside one binary. Each can then be
  deployed as its own service with no code change, once it needs a different lifecycle,
  scaling or host.
- **Observability can be loose; control must be strict.** Read paths tolerate staleness. Anything
  that ever *actuates* (servos, PID setpoints) is a separate service with interlocks, and the
  hardware e-stop stays the final authority.

## Topic map (v1)

| Topic | Retained | Publisher | Payload |
|---|---|---|---|
| `brewery/tilt/v1/<color>` | yes | tilt-receiver (ESP32) | reading v1 (exists) |
| `brewery/receiver/v1/<name>/status` | yes | receivers | `online` / `offline` (exists) |
| `brewery/batch/v1/<batch_id>` | yes | batch-sync | batch state (below); **empty payload = batch no longer active** |
| `brewery/batch/v1/index` | yes | batch-sync | list of active batch ids + device bindings |
| `brewery/probe/v1/<id>` | yes | future hot-side receivers | temperature reading |
| `brewery/cmd/v1/…` | **no** | future controller only | commands; ACL-restricted |

Batch state payload (sketch):
```json
{"v":1,"id":"…","name":"Julius clone","status":"Fermenting","brewDate":…,
 "devices":{"tilt":["Blue"]},
 "targets":{"og":1.061,"fg":1.012,
            "fermentation":[{"step":"Primary","tempC":19,"days":7}],
            "mash":[{"step":"Sacch","tempC":66,"minutes":60}]},
 "updated":1760900000,"traceparent":"00-…"}
```

The empty retained payload is the key detail. It's MQTT's way of deleting a retained message, and it
gives every consumer an explicit "stop" signal, so no consumer needs its own rules about which
statuses count as active.

## Components

| Component | Role | Consumes | Produces | Notes |
|---|---|---|---|---|
| **tilt-receiver** (ESP32) | BLE → MQTT | Tilt adverts | `brewery/tilt/v1/*` | Exists. More receivers: just add boards. |
| **device-metrics** | Device telemetry | `brewery/+/v1/#` readings | Prometheus series per device (no batch labels) | Answers "always write metrics for found Tilts". |
| **batch-sync** | Brewfather → broker | Brewfather API (poll) | `brewery/batch/v1/*` | Sole API client. Owns status transitions and targets. |
| **brewtracker** | Batch telemetry | tilt readings + batch state | Brewfather custom stream; batch info/target series | What go-tilt-exporter becomes. |
| *hot-side monitor* (future) | Mash/boil vs targets | probe readings + batch state (status Brewing) | deviation metrics, alerts | Needs a hot-side temperature source first. |
| *controller* (future) | Automation | batch state + probes | `brewery/cmd/v1/*` | Separate, guarded, never via Brewfather. |
| *brew-day UI / flow diagram* (future) | Dashboard | retained state + VictoriaMetrics | web UI | Retained topics make a "current state" view cheap. |

### On "is a separate metrics service worth it?"

**Not as its own process yet.** The useful split is the *responsibility*: device series that
exist whether or not a batch is active or Brewfather is reachable, kept apart from batch-scoped
series. Build that as its own package with its own subscription inside the brewtracker binary.
It becomes a separate deployment when there's a reason, for example when hot-side probes and
PID data arrive and device-metrics grows past Tilts while brewtracker stays Brewfather-shaped.
Because it only talks to the broker, the split costs nothing then.

## Metrics model

Use device series plus an info metric, and join them in PromQL. Don't duplicate readings per batch.

```
brewery_tilt_gravity{color="Blue"}                      1.007
brewery_tilt_temperature_fahrenheit{color="Blue"}       67
brewery_tilt_rssi_dbm{color="Blue",receiver="…"}        -79
brewery_tilt_last_seen_timestamp_seconds{color="Blue"}  …

brewery_batch_info{batch_id="…",batch_name="…",status="Fermenting",tilt_color="Blue"} 1
brewery_batch_target_og{batch_id="…"}  1.061
brewery_batch_target_fg{batch_id="…"}  1.012
```

Query a batch's gravity:
```
brewery_tilt_gravity * on(color) group_left(batch_id, batch_name)
  label_replace(brewery_batch_info, "color", "$1", "tilt_color", "(.*)")
```

- When a batch stops being active, delete its `brewery_batch_info` and target series
  (`DeleteLabelValues`). Every joined panel stops on its own, which fixes problem 1 at the
  metrics layer.
- Device series go stale on their own: delete a color's series after `max_reading_age` with no
  reading.
- Renaming a batch only changes the info series. Gravity history stays continuous by color.
- Migration: keep emitting the current `brewtracker_*` names for one release next to the
  new ones, move the dashboards over, then drop the old names.

## Brewfather API usage (checked against docs.brewfather.app, 2026-10-20)

- **Limit:** 500 calls per hour per key; HTTP 429 with `Retry-After` when exceeded.
- **Listing batches:** `GET /v2/batches?status=Fermenting&include=recipe.fermentation,recipe.mash,devices`
  returns active batches in one call. Add `status=Brewing` for hot-side work.
- **Readings and status:** `GET /v2/batches/:id/readings/last` and `…/brewtracker` exist.
- **Writes:** `PATCH /v2/batches/:id` can set `status` and the measured values (`measuredOg`,
  `measuredPreBoilGravity`, `measuredPostBoilGravity`, `measuredMashPh`, …). This is a
  future path for logging hot-side samples from a UI instead of typing them into the app.
- **Custom stream:** at most one POST per 15 minutes per device name, and the response is
  `{"result":"OK"}`. The stream also accepts `temp_target`, `gravity_target`, `aux_temp`,
  `ext_temp`, `rssi`, `battery`, `ph` and `comment`, so receiver RSSI and the Tilt's battery-age
  byte could go there.

## Phases

### Phase A — correctness (small; next branch, before containerizing)
- [ ] Replace the batch list on *every* successful refresh, including an empty one. On a failed
      call, keep the old list but mark it stale.
- [ ] Delete batch-scoped series when a batch leaves the active set.
- [ ] Device series for every Tilt seen, whether or not it's bound to a batch, with stale deletion.
- [ ] Batch-scoped work only for Tilts bound to an active batch. Brewfather's
      `devices.tilt` binding stays the source of truth.
- [ ] Use `?status=Fermenting&include=…` and drop the page-everything loop.
- [ ] Refresh batches every ~5 minutes (12 calls/hour) instead of 15, so status changes show
      up quickly.
- [ ] Add `brewery_batch_info` and the target series, keeping the old `brewtracker_*` names for now.

### Phase B — contracts inside one binary
- [ ] `batch-sync` package: Brewfather → `brewery/batch/v1/*` (retained, empty payload = inactive).
- [ ] `brewtracker` reads batch state *from the broker*, not by calling the sync package directly.
      It also reads `brewery/tilt/v1/*`, as it does today. This is the seam that makes the later
      split free.
- [ ] `device-metrics` package with its own subscription.
- [ ] Mosquitto ACL: a `batch-sync` user that can write `brewery/batch/v1/#`.
- [ ] `traceparent` in every payload a service publishes. Receive spans already continue it.

### Phase C — run it in the cluster
- [ ] Container image (GHCR, `linux/amd64`), Pulumi component in `homelab-pulumi`, namespace
      `brewery`.
- [ ] In-cluster endpoints: `mosquitto.mqtt.svc:1883` and `otel-gateway.monitoring.svc:4318`.
      This also removes the Pi's `/etc/hosts` workaround.
- [ ] Secrets through ESO: Brewfather key, MQTT password and stream URL.
- [ ] Turn off the Pi service. The Pi goes back to being the panel computer; it's a candidate
      for a hot-side or PID adapter later.
- [ ] Decide then: one Deployment running all components, or one per component. The
      flag-per-component binary makes either a config choice.

### Phase D — hot side (when there's a temperature source)
- [ ] Decide how mash/HLT/boil temperatures reach the broker. This needs its own small
      investigation first; the current PIDs are standalone.
- [ ] hot-side monitor: during `Brewing`, compare probe readings to the mash step targets from
      batch state and export deviation, plus alerts through the planned Alertmanager → HA
      webhook path.
- [ ] Gravity during mash and boil is sample-based, not streamed. That probably means a small
      UI to log samples and `PATCH` the measured fields to Brewfather.

### Phase E — optional
- Brew-day dashboard: Grafana first. A custom page only if Grafana can't do what's wanted.
- Flow diagram (BrewSpark-like): a read-only UI over the retained `brewery/#` state.
- Automation: its own design doc before any code. Covers interlocks, the e-stop, failure modes,
  and the rule that no setpoints ever come from Brewfather unattended.

## Open decisions

1. Metric names: move to `brewery_*` (above) or keep `brewtracker_*`?
2. Which statuses count as active per component: Fermenting for cold side, Brewing for hot side?
   And what about Conditioning, for cold-crash temperature tracking?
3. Phase C: a single Deployment or one per component?
