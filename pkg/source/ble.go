package source

import (
	"context"
	"time"

	"github.com/jtway/go-tilt"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// BLE scans for Tilts with the local Bluetooth adapter (the original mode).
type BLE struct {
	scanner  *tilt.Scanner
	duration time.Duration
	receiver string
}

// NewBLE returns a BLE source that scans for `duration` per cycle.
func NewBLE(duration time.Duration, receiver string) *BLE {
	return &BLE{scanner: tilt.NewScanner(), duration: duration, receiver: receiver}
}

func (b *BLE) Name() string { return "ble" }

func (b *BLE) Close() {}

func (b *BLE) Collect(ctx context.Context) (Readings, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "ble.scan",
		trace.WithAttributes(attribute.String("ble.scan.duration", b.duration.String())))
	defer span.End()

	b.scanner.Scan(b.duration)
	now := time.Now()
	out := Readings{}
	for colour, t := range b.scanner.Tilts() {
		color := NormalizeColor(string(colour))
		out[color] = Reading{
			Color:       color,
			TempF:       float64(t.Fahrenheit()),
			Gravity:     t.Gravity(),
			Receiver:    b.receiver,
			Observed:    now,
			Received:    now,
			SpanContext: span.SpanContext(),
		}
	}
	span.SetAttributes(attribute.Int("tilt.readings", len(out)))
	return out, ctx.Err()
}
