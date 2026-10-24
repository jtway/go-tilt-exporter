# Brewery telemetry — architecture and roadmap

Status: **phases A and B done 2026-10-24** (branch `feature/batch-sync`). Decisions recorded below. First written 2026-10-20 after the MQTT + tracing phase shipped.
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

### What A+B changed (2026-10-24, `feature/batch-sync`)

- **Components:** batch-sync, brewtracker and device-metrics run in one binary, each with its
  own MQTT client (ids `…-batch-sync`, `…-brewtracker`, `…-brewtracker-batches`,
  `…-device-metrics`). They're switched on and off under `components:`.
- **Batch topics:** batch-sync is the only Brewfather API client. It publishes retained
  `brewery/batch/v1/<id>` and `brewery/batch/v1/index`. On startup it reads the existing
  retained batch topics and clears any batch that's no longer active.
- **Deployment requirements:**
  - The new Mosquitto user `batch-sync` needs the field `BATCH_SYNC_PASSWORD` on the
    1Password item before `pulumi up`.
  - The Pi needs `TILT_EXPORTER_BATCH_SYNC_MQTT_PASSWORD` in `/etc/tilt-exporter/env`.
- **Defaults changed:**
  - `source.type` now defaults to `mqtt`.
  - `mqtt.broker` is required, because batch state comes from the broker even in BLE mode.
  - `brewfather.update_interval` is ignored; `batch_sync.refresh_interval` replaces it (5m).
- **Verified end to end** against a real mosquitto with the same ACLs, a stubbed Brewfather and
  a stubbed OTLP collector. A stale retained batch was cleared on startup, the Fermenting batch
  was tracked, a Tilt with no batch got device series only, and Fermenting → Completed removed
  every batch series and the retained topic while the device series stayed.
- **Not verified against the real Brewfather API:** the `status` filter, and decoding
  `recipe.mash/fermentation.steps` (`stepTemp`/`stepTime`). Check both with the first real
  Fermenting batch.

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
| `brewery/sample/v1/<batch_id>` | no | brew-day sample entry (UI, later BLE meters) | one manual measurement: kind, value, unit, sample temp, stage |
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
| *brew-day samples* (future) | Sample capture → Brewfather | UI entries, later BLE meters | `brewery/sample/v1/*`; batch-sync PATCHes measured fields | The no-new-hardware route to "automated" gravity/pH logging. |
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

**Decision (2026-10-24): keep the `brewtracker_*` prefix.** An existing dashboard depends on
those names. The series that exist today keep their names and labels. New series are added
under the same prefix.

Device series, one per Tilt and batch-agnostic. Some already exist; the rest are new:
```
brewtracker_tilt_gravity{color="Blue"}                       1.007   (new)
brewtracker_tilt_temperature_f{color="Blue"}                 67      (new)
brewtracker_tilt_rssi_dbm{color="Blue",receiver="…"}         -79     (exists)
brewtracker_tilt_last_reading_timestamp_seconds{color="Blue"} …      (exists)
```

Batch context, new:
```
brewtracker_batch_info{id="…",name="…",status="Fermenting",tilt_color="Blue"} 1
```

The existing batch series stay as they are, so the dashboard keeps working:
`brewtracker_gravity_reading{id,name,tilt_color}`, `brewtracker_temperature_reading_f/_c`,
`brewtracker_measured_og`, `brewtracker_estimated_fg`, and the rest. The difference is that
they are **deleted when the batch leaves the active set** (problem 1). New panels can use the
join instead:
```
brewtracker_tilt_gravity * on(color) group_left(id, name)
  label_replace(brewtracker_batch_info, "color", "$1", "tilt_color", "(.*)")
```

- Device series are deleted after `max_reading_age` with no reading.
- Renaming a batch only changes the info series and the existing batch-scoped ones. The
  device series keep their history by color.

## Batch lifecycle — Brewfather's status is the authority

**Decision (2026-10-24):** status is changed by hand in the Brewfather app, and telemetry never
changes it. Services only *respect* it. Today Fermenting → Conditioning/Completed happens at
kegging.

| Status | Cold side (Tilt) | Hot side (future) |
|---|---|---|
| Planning | ignored | ignored |
| Brewing | ignored | **active**: mash/boil monitoring, sample capture |
| Fermenting | **active**: batch series, Brewfather stream | ignored |
| Conditioning | configurable (`track_conditioning`, default off) | ignored |
| Completed / Archived | stopped: series deleted, no stream posts | ignored |

A possible workflow change, which Josh is open to: mark Conditioning when fermentation has
stalled for ~2 days, rather than at kegging. Telemetry can **suggest** that transition without
making it:
- An alert rule on the device series fires when gravity has been flat for 48h, for example
  `max_over_time(brewtracker_tilt_gravity[48h]) - min_over_time(brewtracker_tilt_gravity[48h]) < 0.002`,
  restricted to Tilts bound to a Fermenting batch.
- It notifies through the planned Alertmanager → HA mobile push: "Blue / Julius clone: flat
  for 48h at 1.012. Consider Conditioning."
- With `track_conditioning: true`, the Tilt keeps reporting temperature through conditioning
  and a cold crash.
- Pushing the status automatically (`PATCH status`) stays out of scope. The API allows it, but
  the decision stays with Josh.

Cold crashing today means turning a heater off. There's no chiller, so there's nothing to
automate yet beyond seeing the temperature curve.

## Brewfather API usage (checked against docs.brewfather.app, 2026-10-20)

- **Limit:** 500 calls per hour per key; HTTP 429 with `Retry-After` when exceeded.
- **Listing batches:** `GET /v2/batches?status=Fermenting&include=recipe.fermentation,recipe.mash,devices`
  returns active batches in one call. Add `status=Brewing` for hot-side work.
- **Readings and status:** `GET /v2/batches/:id/readings/last` and `…/brewtracker` exist.
- **Writes:** `PATCH /v2/batches/:id` can set `status` and the measured values
  `measuredFirstWortGravity`, `measuredPreBoilGravity`, `measuredPostBoilGravity`,
  `measuredMashPh`, `measuredOg`, `measuredFg`, plus volumes (`measuredBoilSize`,
  `measuredKettleSize`, `measuredBatchSize`, …).
  - **The only pH field is mash pH.** Boil or sparge pH has no batch field. It can go to the
    custom stream's `ph` field (graphed on the batch), or stay in our own metrics.
  - **No reading-history writes.** Repeated mash or sparge gravity readings can't be stored as
    measured fields. Keep them in VictoriaMetrics, and push only the milestone values
    (first wort, pre-boil, post-boil, OG).
- **Brew-day timer:** `GET /v2/batches/:id/brewtracker` returns the stored brew tracker status.
  If the in-app brew-day timer exposes the current mash step there, the hot-side monitor
  knows which step target applies without any extra input. **Unverified; check this before
  designing phase D around it.**
- **Custom stream:** at most one POST per 15 minutes per device name, and the response is
  `{"result":"OK"}`. The stream also accepts `temp_target`, `gravity_target`, `aux_temp`,
  `ext_temp`, `rssi`, `battery`, `ph` and `comment`, so receiver RSSI and the Tilt's battery-age
  byte could go there.

## Phases

### Phase A — correctness (done 2026-10-24)
- [x] Replace the batch list on *every* successful refresh, including an empty one. On a failed
      call, keep the old list but mark it stale.
- [x] Delete batch-scoped series when a batch leaves the active set.
- [x] Device series for every Tilt seen, whether or not it's bound to a batch, with stale deletion.
- [x] Batch-scoped work only for Tilts bound to an active batch. Brewfather's
      `devices.tilt` binding stays the source of truth.
- [x] Use `?status=<status>` and drop the page-everything loop. (`include=` isn't used: each active
      batch is fetched in full, one call each, which is the known-good path.)
- [x] Refresh batches every ~5 minutes (12 calls/hour) instead of 15, so status changes show
      up quickly.
- [x] Add `brewtracker_batch_info` and the device series `brewtracker_tilt_gravity` / `_temperature_f`.
      Existing `brewtracker_*` names and labels stay unchanged (the dashboard depends on them).
- [x] Status handling per the lifecycle table, with a `track_conditioning` option (default off).

### Phase A2 — fermentation-done nudge (needs the in-cluster alerting work)
- [ ] VMRule: 48h flat gravity on a Fermenting batch → Alertmanager → HA mobile push.
- [ ] Tune the threshold and window against the history of a past batch before turning it on.

### Phase B — contracts inside one binary (done 2026-10-24)
- [x] `batch-sync` package: Brewfather → `brewery/batch/v1/*` (retained, empty payload = inactive).
- [x] `brewtracker` reads batch state *from the broker*, not by calling the sync package directly.
      It also reads `brewery/tilt/v1/*`, as it does today. This is the seam that makes the later
      split free.
- [x] `device-metrics` package with its own subscription.
- [x] Mosquitto ACL: a `batch-sync` user that can write `brewery/batch/v1/#`.
- [x] `traceparent` in every payload a service publishes. Receive spans already continue it.

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

### Phase D — brew day (hot side)

Goal: mash temperature consistency, gravity and pH through mash and sparge, and pre/post-boil
gravity and pH in Brewfather without retyping. Two tracks; D1 needs no new hardware.

**D1 — sample capture (no new hardware).** Most brew-day numbers come from a pulled sample
anyway, so automate everything *except* taking the sample.
- [ ] A phone-friendly page with one form: kind (gravity / pH / Brix), value, sample temperature
      and stage (mash, first runnings, sparge, pre-boil, post-boil, OG). It's bound to the
      batch in `Brewing` automatically.
- [ ] Corrections are applied on entry: hydrometer temperature correction, refractometer
      Brix → SG with a wort correction factor, and the alcohol correction for refractometer
      readings once fermentation has started. The raw value is always stored too.
- [ ] Each entry is published to `brewery/sample/v1/<batch_id>` and becomes a metric and a
      span event, so a brew day is one trace with a timeline of samples.
- [ ] batch-sync PATCHes the milestone fields: first wort, pre-boil, post-boil, OG and mash pH.
      Anything without a field goes to the custom stream (`gravity` and `ph` with a `comment`)
      or stays in metrics.
- [ ] Later, a Bluetooth refractometer or pH meter publishes to the **same topic**. It replaces
      the typing; nothing downstream changes.

**D2 — mash temperature consistency (needs a temperature source).**
- [ ] The kettle PT100s go to the standalone ITC-106VH PIDs, which have no data output. A
      second path is needed: a probe the ESP32 or Pi can read, or a BLE thermometer the
      tilt-receiver pattern can decode. **Check first whether anything already owned
      fits**, before considering a purchase.
- [ ] Published as `brewery/probe/v1/<id>` (mash, HLT, RIMS out), with a probe → vessel mapping.
- [ ] The hot-side monitor compares readings to the mash step targets from batch state
      (`recipe.mash` steps, `stepTemp`/`stepTime`). It exports deviation, time-in-band % per
      step, and ramp time between steps, and alerts on a sustained deviation.
- [ ] Knowing which step is current: from the Brewfather brew-day timer if the API exposes it
      (see above), otherwise inferred from the temperature profile, or a "next step" button
      on the D1 page.

### Phase E — optional
- Brew-day dashboard: Grafana first. A custom page only if Grafana can't do what's wanted.
- Flow diagram (BrewSpark-like): a read-only UI over the retained `brewery/#` state.
- Automation: its own design doc before any code. Covers interlocks, the e-stop, failure modes,
  and the rule that no setpoints ever come from Brewfather unattended.

## Decisions

| Date | Decision |
|---|---|
| 2026-10-24 | Keep the `brewtracker_*` metric prefix: an existing dashboard depends on it. New series use the same prefix. |
| 2026-10-24 | Brewfather status is the authority and is set by hand. Telemetry may suggest a transition (48h flat gravity) but never PATCHes status. |
| 2026-10-24 | Active cold-side status: Fermenting. Conditioning is optional (`track_conditioning`). Completed/Archived: stop and delete series. |
| 2026-10-24 | Brew day: sample capture (D1) comes before any new sensor hardware. |

## Open decisions

1. Phase C: a single Deployment or one per component?
2. Whether to move the Conditioning transition earlier (at stalled fermentation, prompted by
   the A2 alert) instead of at kegging.
