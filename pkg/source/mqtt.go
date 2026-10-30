package source

import (
	"context"
	"errors"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/jtway/go-tilt-exporter/pkg/contract"
	"github.com/jtway/go-tilt-exporter/pkg/mqttx"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

const tracerName = "github.com/jtway/go-tilt-exporter/pkg/source"

var errNoColor = errors.New("payload has no color")

// MQTTConfig configures the broker connection shared by all components.
type MQTTConfig struct {
	// Broker URL, e.g. tcp://192.168.1.241:1883
	Broker   string `mapstructure:"broker"`
	Username string `mapstructure:"username"`
	// Password; prefer the TILT_EXPORTER_MQTT_PASSWORD env var over the file.
	Password string `mapstructure:"password"`
	// Topic filter for Tilt readings, e.g. brewery/tilt/v1/+
	Topic string `mapstructure:"topic"`
	// TopicRoot for batch topics (brewery/batch/v1/...).
	TopicRoot string `mapstructure:"topic_root"`
	// ClientID is ignored since v0.3: each component derives its own.
	ClientID string `mapstructure:"client_id"`
}

var mqttMessages = promauto.NewCounterVec(prometheus.CounterOpts{
	Namespace: "brewtracker",
	Subsystem: "mqtt",
	Name:      "messages_total",
	Help:      "Tilt reading messages received, by result (ok, decode_error).",
}, []string{"result"})

// Handler is called for every successfully decoded reading.
type Handler func(Reading)

// MQTT keeps the latest reading per color from a broker subscription.
type MQTT struct {
	client mqtt.Client
	logger *zap.SugaredLogger
	tracer trace.Tracer
	name   string

	ticker *time.Ticker
	maxAge time.Duration

	mu       sync.Mutex
	latest   Readings
	handlers []Handler
}

// NewMQTT subscribes to Tilt readings as `component` (its own client ID).
// cycle is how often Collect returns; readings older than maxAge are dropped
// from snapshots. Handlers, if any, see every decoded reading as it arrives.
func NewMQTT(cfg MQTTConfig, component string, cycle, maxAge time.Duration, logger *zap.SugaredLogger, handlers ...Handler) (*MQTT, error) {
	topic := cfg.Topic
	if topic == "" {
		topic = contract.Topics{Root: cfg.TopicRoot}.TiltFilter()
	}
	m := &MQTT{
		logger:   logger,
		tracer:   otel.Tracer(tracerName),
		name:     component,
		maxAge:   maxAge,
		latest:   Readings{},
		handlers: handlers,
	}
	if cycle > 0 {
		m.ticker = time.NewTicker(cycle)
	}
	client, err := mqttx.Connect(mqttx.Config{
		Broker: cfg.Broker, Username: cfg.Username, Password: cfg.Password, Component: component,
	}, []mqttx.Subscription{{Filter: topic, Handler: m.handle}}, logger)
	if err != nil {
		return nil, err
	}
	m.client = client
	return m, nil
}

func (m *MQTT) Name() string { return "mqtt" }

// Close disconnects from the broker.
func (m *MQTT) Close() {
	if m.ticker != nil {
		m.ticker.Stop()
	}
	mqttx.Disconnect(m.client, m.name)
}

// Collect waits for the next cycle tick, then returns fresh readings.
func (m *MQTT) Collect(ctx context.Context) (Readings, error) {
	if m.ticker == nil {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-m.ticker.C:
	}
	return m.snapshot(time.Now()), nil
}

func (m *MQTT) snapshot(now time.Time) Readings {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := Readings{}
	for color, r := range m.latest {
		if m.maxAge > 0 && now.Sub(r.Observed) > m.maxAge {
			continue
		}
		out[color] = r
	}
	return out
}

// handle runs on paho's goroutine for every message.
func (m *MQTT) handle(_ mqtt.Client, msg mqtt.Message) {
	m.ingest(msg.Topic(), msg.Payload(), msg.Retained(), time.Now())
}

func (m *MQTT) ingest(topic string, payload []byte, retained bool, now time.Time) {
	reading, p, err := DecodePayload(payload, contract.LastLevel(topic), now)

	// Continue the publisher's trace when it sent one; otherwise each receive
	// is its own root span.
	ctx := mqttx.Extract(context.Background(), p.Trace.Carrier())
	_, span := m.tracer.Start(ctx, "receive "+topic,
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithTimestamp(now),
		trace.WithAttributes(
			semconv.MessagingSystemKey.String("mqtt"),
			semconv.MessagingOperationTypeReceive,
			semconv.MessagingOperationName("receive"),
			semconv.MessagingDestinationName(topic),
			semconv.MessagingMessageBodySize(len(payload)),
			attribute.Bool("mqtt.retained", retained),
			attribute.String("component", m.name),
		))
	defer span.End()

	if err != nil {
		mqttMessages.WithLabelValues("decode_error").Inc()
		span.RecordError(err)
		span.SetStatus(codes.Error, "decode failed")
		m.logger.Warnf("mqtt %s: %s", topic, err)
		return
	}
	span.SetAttributes(
		attribute.String("tilt.color", reading.Color),
		attribute.Float64("tilt.gravity", reading.Gravity),
		attribute.Float64("tilt.temp_f", reading.TempF),
		attribute.Int("tilt.rssi", reading.RSSI),
		attribute.String("tilt.receiver", reading.Receiver),
		attribute.Int64("tilt.observed_unix", reading.Observed.Unix()),
	)
	reading.SpanContext = span.SpanContext()
	mqttMessages.WithLabelValues("ok").Inc()

	m.mu.Lock()
	m.latest[reading.Color] = reading
	m.mu.Unlock()
	for _, h := range m.handlers {
		h(reading)
	}
}
