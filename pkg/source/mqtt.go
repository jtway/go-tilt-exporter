package source

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

const tracerName = "github.com/jtway/go-tilt-exporter/pkg/source"

// MQTTConfig configures the broker subscription.
type MQTTConfig struct {
	// Broker URL, e.g. tcp://192.168.1.241:1883
	Broker   string `mapstructure:"broker"`
	Username string `mapstructure:"username"`
	// Password; prefer the TILT_EXPORTER_MQTT_PASSWORD env var over the file.
	Password string `mapstructure:"password"`
	// Topic filter, e.g. brewery/tilt/v1/+
	Topic string `mapstructure:"topic"`
	// ClientID; defaults to go-tilt-exporter-<hostname>.
	ClientID string `mapstructure:"client_id"`
}

var (
	mqttMessages = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "brewtracker",
		Subsystem: "mqtt",
		Name:      "messages_total",
		Help:      "MQTT messages received, by result (ok, decode_error).",
	}, []string{"result"})
	mqttConnected = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "brewtracker",
		Subsystem: "mqtt",
		Name:      "connected",
		Help:      "1 while connected to the MQTT broker.",
	})
)

// MQTT keeps the latest reading per color from a broker subscription.
type MQTT struct {
	cfg    MQTTConfig
	client mqtt.Client
	logger *zap.SugaredLogger
	tracer trace.Tracer

	ticker *time.Ticker
	maxAge time.Duration

	mu     sync.Mutex
	latest Readings
}

// NewMQTT connects (retrying in the background) and subscribes on every
// (re)connect. cycle is how often Collect returns; readings older than maxAge
// are dropped from snapshots.
func NewMQTT(cfg MQTTConfig, cycle, maxAge time.Duration, logger *zap.SugaredLogger) (*MQTT, error) {
	if cfg.Broker == "" || cfg.Topic == "" {
		return nil, fmt.Errorf("mqtt.broker and mqtt.topic are required")
	}
	if cfg.ClientID == "" {
		host, _ := os.Hostname()
		cfg.ClientID = "go-tilt-exporter-" + host
	}
	m := &MQTT{
		cfg:    cfg,
		logger: logger,
		tracer: otel.Tracer(tracerName),
		ticker: time.NewTicker(cycle),
		maxAge: maxAge,
		latest: Readings{},
	}

	opts := mqtt.NewClientOptions().
		AddBroker(cfg.Broker).
		SetClientID(cfg.ClientID).
		SetUsername(cfg.Username).
		SetPassword(cfg.Password).
		SetCleanSession(true).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(5 * time.Second).
		SetMaxReconnectInterval(30 * time.Second).
		SetOrderMatters(false).
		SetOnConnectHandler(func(c mqtt.Client) {
			mqttConnected.Set(1)
			logger.Infof("mqtt connected to %s; subscribing to %s", cfg.Broker, cfg.Topic)
			// Subscribe on every connect: clean sessions don't keep subscriptions.
			tok := c.Subscribe(cfg.Topic, 0, m.handle)
			if tok.WaitTimeout(10*time.Second) && tok.Error() != nil {
				logger.Errorf("mqtt subscribe %s failed: %s", cfg.Topic, tok.Error())
			}
		}).
		SetConnectionLostHandler(func(_ mqtt.Client, err error) {
			mqttConnected.Set(0)
			logger.Warnf("mqtt connection lost: %s", err)
		}).
		SetReconnectingHandler(func(_ mqtt.Client, _ *mqtt.ClientOptions) {
			logger.Infof("mqtt reconnecting to %s", cfg.Broker)
		})

	m.client = mqtt.NewClient(opts)
	tok := m.client.Connect()
	// With ConnectRetry the token completes only once connected; don't block
	// startup on the broker, the retry loop keeps going in the background.
	if !tok.WaitTimeout(10 * time.Second) {
		logger.Warnf("mqtt not connected to %s yet; retrying in background", cfg.Broker)
	} else if tok.Error() != nil {
		return nil, fmt.Errorf("mqtt connect: %w", tok.Error())
	}
	return m, nil
}

func (m *MQTT) Name() string { return "mqtt" }

// Close disconnects from the broker.
func (m *MQTT) Close() {
	m.ticker.Stop()
	m.client.Disconnect(250)
	mqttConnected.Set(0)
}

// Collect waits for the next cycle tick, then returns fresh readings.
func (m *MQTT) Collect(ctx context.Context) (Readings, error) {
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
	reading, p, err := DecodePayload(payload, topicColor(topic), now)

	// Continue the publisher's trace when it sent one (future in-cluster
	// publishers); otherwise each receive is its own root span.
	ctx := context.Background()
	if p.Traceparent != "" {
		ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier{
			"traceparent": p.Traceparent,
			"tracestate":  p.Tracestate,
		})
	}
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
}

// topicColor returns the last topic level (brewery/tilt/v1/<color>).
func topicColor(topic string) string {
	if i := strings.LastIndex(topic, "/"); i >= 0 {
		return topic[i+1:]
	}
	return topic
}
