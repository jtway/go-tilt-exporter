# go-tilt-exporter
Tilt Hydrometer (Unofficial) Exporter written in Golang

Matches Tilt readings to fermenting Brewfather batches, updates the batch's
Brewfather custom stream, and exposes Prometheus metrics (`:9100/metrics`).

Long-term plan, handoff and known issues: [`docs/ROADMAP.md`](docs/ROADMAP.md).

## Components

One binary, three components. They only talk to each other through the MQTT broker, so each
can be turned off under `components:` and run as its own service later.

| Component | Reads | Writes | MQTT user |
|---|---|---|---|
| `batch-sync` | Brewfather API, every `refresh_interval` (5m) | `brewery/batch/v1/<id>` and `brewery/batch/v1/index`, retained | `batch-sync` (write) |
| `brewtracker` | `brewery/batch/v1/+` and Tilt readings | Batch metrics, Brewfather custom stream | `brewtracker` (read) |
| `device-metrics` | `brewery/tilt/v1/+` | One series per Tilt | `brewtracker` (read) |

**Brewfather's status is the authority.** batch-sync polls only the configured statuses
(default `Fermenting`; `track_conditioning: true` adds `Conditioning`). When a batch leaves
them, it publishes an empty retained payload on that batch's topic. brewtracker then deletes
every series for the batch and stops posting to its stream. If a refresh fails, nothing is
published, so a Brewfather outage doesn't make batches disappear.

## Reading sources

| `source.type` | Where readings come from |
|---|---|
| `mqtt` (default) | The broker. ESP32 receivers next to the fermenter (see `esphome/`) publish to `brewery/tilt/v1/<color>`. |
| `ble` | This machine's Bluetooth adapter scans for `cycle_interval` each cycle. Batch state still comes from the broker. |

### Payloads (v1)

Tilt reading, published retained on `brewery/tilt/v1/<color>`:
```json
{"v":1,"color":"Blue","temp_f":73,"sg":1.012,"rssi":-85,"tx_power":-59,
 "receiver":"tilt-receiver","ts":1760900000}
```

Batch state, published retained on `brewery/batch/v1/<id>`. An empty payload means the
batch is no longer active:
```json
{"v":1,"id":"…","name":"Julius clone","batchNo":7,"status":"Fermenting",
 "devices":{"tilt":["Blue"],"streams":["brewtracker"]},
 "targets":{"estimatedFg":1.012,"measuredOg":1.061,
            "mash":[{"name":"Sacch","tempC":66,"time":60}],
            "fermentation":[{"type":"Primary","tempC":19,"time":7}]},
 "updated":1760900000,"traceparent":"00-…"}
```

Every payload may carry `traceparent`/`tracestate`. batch-sync sets them, so a batch's
refresh, publish and receive spans form one trace.

## Metrics

| Series | Labels | Notes |
|---|---|---|
| `brewtracker_tilt_gravity`, `brewtracker_tilt_temperature_f` | `color` | Every Tilt heard. Removed after `max_reading_age` without a reading. |
| `brewtracker_tilt_rssi_dbm` | `color`, `receiver` | |
| `brewtracker_tilt_last_reading_timestamp_seconds` | `color` | |
| `brewtracker_batch_info` | `id`, `name`, `status`, `tilt_color` | 1 per active batch and Tilt. Join on `tilt_color`. |
| `brewtracker_gravity_reading`, `brewtracker_temperature_reading_f/_c`, `brewtracker_measured_og`, `brewtracker_estimated_fg/_ibu/_srm` | `id`, `name`, … | Unchanged names. **Deleted when the batch stops being active.** |
| `brewtracker_batchsync_last_success_timestamp_seconds`, `_refresh_errors_total`, `_active_batches` | | batch-sync health |
| `brewtracker_mqtt_component_connected` | `component` | |

## Tracing

OTLP/HTTP via `tracing.endpoint` (or the standard `OTEL_*` env vars).

| Span | When |
|---|---|
| `receive brewery/tilt/v1/<color>` | One per MQTT message (consumer). Attributes: color, gravity, temp, RSSI, receiver. Decode failures are recorded as errors. |
| `batchsync.refresh` | Root of each Brewfather poll: `brewfather.get_active_batches` (HTTP client spans), then a `publish brewery/batch/v1/…` producer span per batch. |
| `receive brewery/batch/v1/<id>` | brewtracker receiving batch state. Continues the batch-sync trace. |
| `brewtracker.cycle` | Root span for each processing pass. |
| `brewtracker.update_batch` | One per batch. It **links** to the receive span of the reading it used. |
| `brewfather.webhook.update` | The stream POST, with its HTTP client span. |

## Configuration

See `config-example.yaml`. Files are read from `/etc/tilt-exporter/config.yaml`,
`~/.tilt-exporter/config.yaml` or `./config.yaml`. Any key can be overridden by env as
`TILT_EXPORTER_<SECTION>_<KEY>`, e.g. `TILT_EXPORTER_MQTT_PASSWORD`,
`TILT_EXPORTER_BATCH_SYNC_MQTT_PASSWORD`. `brewfather.update_interval` is no longer used;
see `batch_sync.refresh_interval`.

## Build for the Raspberry Pi

```bash
GOOS=linux GOARCH=arm64 go build -o tilt-exporter .          # 64-bit Pi OS
GOOS=linux GOARCH=arm GOARM=7 go build -o tilt-exporter .    # 32-bit Pi OS
```

Requires Go 1.26+. With `GOTOOLCHAIN=auto` (the default), an older Go fetches 1.26
automatically.

## Tests

```bash
go test ./...
# against a live broker (reads the retained brewery/tilt/v1/blue):
MQTT_TEST_BROKER=tcp://192.168.1.241:1883 MQTT_TEST_PASSWORD=... go test ./pkg/source -run Integration -v
```
