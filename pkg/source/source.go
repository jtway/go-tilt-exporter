// Package source provides Tilt readings to the brew tracker, independent of how
// they were captured: a local BLE scan, or an MQTT broker fed by remote
// receivers (ESP32).
package source

import (
	"context"
	"math"
	"strings"
	"time"

	"go.opentelemetry.io/otel/trace"
)

// Reading is one decoded Tilt observation.
type Reading struct {
	// Color is title case to match go-tilt ("Blue", "Red", ...).
	Color    string
	TempF    float64
	Gravity  float64
	RSSI     int
	TxPower  int
	Receiver string
	// Observed is when the receiver saw the advertisement (payload ts), or the
	// receipt time when the receiver had no clock yet.
	Observed time.Time
	// Received is when this process got the reading.
	Received time.Time
	// SpanContext of the span that received the reading. Processing spans link
	// to it so a batch update can be traced back to the message that fed it.
	SpanContext trace.SpanContext
}

// Celsius converts the Fahrenheit reading, rounded to 2 decimals like go-tilt.
func (r Reading) Celsius() float64 {
	return math.Round((r.TempF-32)/1.8*100) / 100
}

// Readings is the latest reading per color.
type Readings map[string]Reading

// Source yields readings once per processing cycle.
type Source interface {
	// Collect blocks for up to one cycle and returns the latest reading per
	// color. BLE scans for the cycle duration; MQTT waits out the cycle while
	// its subscription keeps the latest values.
	Collect(ctx context.Context) (Readings, error)
	// Close releases the underlying connection or device.
	Close()
	// Name is "ble" or "mqtt", used in logs and span attributes.
	Name() string
}

// NormalizeColor returns the title-case color go-tilt and this package use.
func NormalizeColor(c string) string {
	c = strings.TrimSpace(c)
	if c == "" {
		return ""
	}
	return strings.ToUpper(c[:1]) + strings.ToLower(c[1:])
}
