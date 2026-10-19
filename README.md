# go-tilt-exporter
Tilt Hydrometer (Unofficial) Exporter written in Golang

Matches Tilt readings to fermenting Brewfather batches, updates the batch's
Brewfather custom stream, and exposes Prometheus metrics (`:9100/metrics`).

## Reading sources

| `source.type` | Where readings come from |
|---|---|
| `mqtt` | The broker. ESP32 receivers next to the fermenter (see `esphome/`) publish to `brewery/tilt/v1/<color>`. |
| `ble` | This machine's Bluetooth adapter scans for `cycle_interval` each cycle (the original behaviour). |

### MQTT payload (v1)

Topic `brewery/tilt/v1/<color>`, retained:

```json
{"v":1,"color":"Blue","temp_f":73,"sg":1.012,"rssi":-85,"tx_power":-59,
 "receiver":"tilt-receiver","ts":1760900000}
```

- `ts` is unix seconds from the receiver (0 until its clock syncs).
- Optional `traceparent` / `tracestate` fields are honoured. A publisher that sets them
  gets its trace continued by the receive span. The ESP32 doesn't; future in-cluster
  publishers will.

## Tracing

OTLP/HTTP via `tracing.endpoint` (or the standard `OTEL_*` env vars).

| Span | When |
|---|---|
| `receive brewery/tilt/v1/<color>` | One per MQTT message (consumer). Attributes: color, gravity, temp, RSSI, receiver. Decode failures are recorded as errors. |
| `brewtracker.cycle` | Root span for each processing pass. |
| `brewfather.get_active_batches` | Child of the cycle, with an HTTP client span per Brewfather call. |
| `brewtracker.update_batch` | One per batch. It **links** to the receive span of the reading it used. |
| `brewfather.webhook.update` | The stream POST, with its HTTP client span. |

## Configuration

See `config-example.yaml`. Files are read from `/etc/tilt-exporter/config.yaml`,
`~/.tilt-exporter/config.yaml` or `./config.yaml`. Any key can be overridden by env as
`TILT_EXPORTER_<SECTION>_<KEY>`, e.g. `TILT_EXPORTER_MQTT_PASSWORD`.

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
