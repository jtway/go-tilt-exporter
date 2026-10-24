package source

import (
	"context"
	"os"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.uber.org/zap"
)

// Exactly what tilt-receiver.yaml publishes.
const espPayload = `{"v":1,"color":"Blue","temp_f":73,"sg":1.012,"rssi":-85,"tx_power":-59,"receiver":"tilt-receiver","ts":1760900000}`

func TestDecodePayload(t *testing.T) {
	now := time.Unix(1760900005, 0)
	r, _, err := DecodePayload([]byte(espPayload), "blue", now)
	if err != nil {
		t.Fatal(err)
	}
	if r.Color != "Blue" || r.Gravity != 1.012 || r.TempF != 73 || r.RSSI != -85 || r.Receiver != "tilt-receiver" {
		t.Fatalf("unexpected reading %+v", r)
	}
	if !r.Observed.Equal(time.Unix(1760900000, 0)) {
		t.Fatalf("observed = %v", r.Observed)
	}
	if r.Celsius() != 22.78 {
		t.Fatalf("celsius = %v", r.Celsius())
	}
}

func TestDecodePayloadFallbacks(t *testing.T) {
	now := time.Now()
	r, _, err := DecodePayload([]byte(`{"v":1,"temp_f":68,"sg":1.050,"ts":0}`), "red", now)
	if err != nil {
		t.Fatal(err)
	}
	if r.Color != "Red" {
		t.Fatalf("color from topic: %q", r.Color)
	}
	if !r.Observed.Equal(now) {
		t.Fatal("ts=0 should fall back to receipt time")
	}
}

func TestDecodePayloadRejects(t *testing.T) {
	for name, p := range map[string]string{
		"bad json": `{`,
		"version":  `{"v":2,"color":"Blue","sg":1.0}`,
		"no color": `{"v":1,"sg":1.0}`,
		"sg range": `{"v":1,"color":"Blue","sg":5.0}`,
		"sg zero":  `{"v":1,"color":"Blue","sg":0}`,
	} {
		if _, _, err := DecodePayload([]byte(p), "", time.Now()); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func newTestMQTT(maxAge time.Duration) *MQTT {
	return &MQTT{
		logger: zap.NewNop().Sugar(),
		tracer: otel.Tracer(tracerName),
		maxAge: maxAge,
		latest: Readings{},
	}
}

func TestIngestAndSnapshot(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	m := newTestMQTT(5 * time.Minute)
	m.tracer = otel.Tracer(tracerName)

	recv := time.Unix(1760900005, 0)
	m.ingest("brewery/tilt/v1/blue", []byte(espPayload), true, recv)
	m.ingest("brewery/tilt/v1/red", []byte(`not json`), false, recv)

	snap := m.snapshot(recv.Add(time.Minute))
	if len(snap) != 1 || snap["Blue"].Gravity != 1.012 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if !snap["Blue"].SpanContext.IsValid() {
		t.Fatal("reading should carry its receive span context")
	}
	// Older than max age → dropped.
	if got := m.snapshot(recv.Add(10 * time.Minute)); len(got) != 0 {
		t.Fatalf("stale reading kept: %+v", got)
	}

	spans := rec.Ended()
	if len(spans) != 2 {
		t.Fatalf("want 2 receive spans, got %d", len(spans))
	}
	if spans[0].Name() != "receive brewery/tilt/v1/blue" || spans[0].Status().Code != 0 {
		t.Fatalf("span 0: %s %v", spans[0].Name(), spans[0].Status())
	}
	if spans[1].Status().Description != "decode failed" {
		t.Fatalf("decode error not recorded: %v", spans[1].Status())
	}
}

func TestIngestContinuesPublisherTrace(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	otel.SetTextMapPropagator(propagation.TraceContext{})
	m := newTestMQTT(0)

	payload := `{"v":1,"color":"Blue","temp_f":73,"sg":1.012,"ts":0,` +
		`"traceparent":"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"}`
	m.ingest("brewery/tilt/v1/blue", []byte(payload), false, time.Now())

	s := rec.Ended()[0]
	if s.SpanContext().TraceID().String() != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("trace not continued: %s", s.SpanContext().TraceID())
	}
	if s.Parent().SpanID().String() != "00f067aa0ba902b7" {
		t.Fatalf("parent = %s", s.Parent().SpanID())
	}
}

// TestBrokerIntegration runs against a real broker when MQTT_TEST_BROKER is
// set, e.g. MQTT_TEST_BROKER=tcp://127.0.0.1:1883 MQTT_TEST_PASSWORD=...
func TestBrokerIntegration(t *testing.T) {
	broker := os.Getenv("MQTT_TEST_BROKER")
	if broker == "" {
		t.Skip("MQTT_TEST_BROKER not set")
	}
	m, err := NewMQTT(MQTTConfig{
		Broker:   broker,
		Username: "brewtracker",
		Password: os.Getenv("MQTT_TEST_PASSWORD"),
		Topic:    "brewery/tilt/v1/+",
	}, "test", 2*time.Second, 0, zap.NewNop().Sugar())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// The retained message published before we connected must arrive.
	r, err := m.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r["Blue"].Gravity != 1.012 {
		t.Fatalf("retained reading not received: %+v", r)
	}
}
