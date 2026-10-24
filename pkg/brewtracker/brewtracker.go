package brewtracker

import (
	"context"
	"os"
	"errors"

	"sort"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/jtway/go-tilt-exporter/pkg/brewfather"
	"github.com/jtway/go-tilt-exporter/pkg/contract"
	"github.com/jtway/go-tilt-exporter/pkg/mqttx"
	"github.com/jtway/go-tilt-exporter/pkg/source"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

const (
	tracerName = "github.com/jtway/go-tilt-exporter/pkg/brewtracker"
	// Component is the MQTT client suffix for Tilt readings; batch state uses
	// Component + "-batches".
	Component = "brewtracker"
)

// BrewTracker joins Tilt readings to active batches. It learns which batches
// are active ONLY from batch-sync's retained state on the broker, and posts
// readings to each batch's Brewfather custom stream.
type BrewTracker struct {
	Config  *Config
	metrics *metrics
	Logger  *zap.SugaredLogger
	tracer  trace.Tracer

	webhooks map[string]*brewfather.BrewTrackerWebhook

	// OnReading, when set, sees every collected reading (used to feed
	// device-metrics in BLE mode, where there's no broker subscription).
	OnReading func(source.Reading)

	mu      sync.Mutex
	batches map[string]contract.BatchStateV1
}

// NewBrewTracker builds the tracker from config.
func NewBrewTracker(cfg *Config, logger *zap.SugaredLogger) *BrewTracker {
	bt := &BrewTracker{
		Config:   cfg,
		metrics:  NewMetrics(),
		Logger:   logger,
		tracer:   otel.Tracer(tracerName),
		webhooks: map[string]*brewfather.BrewTrackerWebhook{},
		batches:  map[string]contract.BatchStateV1{},
	}
	httpClient := brewfather.NewHTTPClient()
	for i := range cfg.Brewfather.Webhooks {
		w := &cfg.Brewfather.Webhooks[i]
		// Allows `url: ${BREWFATHER_STREAM_URL}` so the logging id can come
		// from a secret instead of the config file.
		w.Url = os.ExpandEnv(w.Url)
		bt.webhooks[w.Name] = brewfather.NewBrewTrackerWebhook(w, httpClient)
		logger.Infof("webhook %s configured, enabled: %t", w.Name, w.Enabled)
	}
	return bt
}

func (bt *BrewTracker) newSource() (source.Source, error) {
	switch bt.Config.Source.Type {
	case "mqtt":
		return source.NewMQTT(bt.Config.MQTT, Component, bt.Config.Source.CycleInterval,
			bt.Config.Source.MaxReadingAge, bt.Logger)
	default:
		return source.NewBLE(bt.Config.Source.CycleInterval, bt.Config.Source.Receiver), nil
	}
}

// Run subscribes to batch state and processes readings every cycle until ctx
// is cancelled.
func (bt *BrewTracker) Run(ctx context.Context) error {
	defer bt.Logger.Sync()
	topics := contract.Topics{Root: bt.Config.MQTT.TopicRoot}
	batchClient, err := mqttx.Connect(mqttx.Config{
		Broker: bt.Config.MQTT.Broker, Username: bt.Config.MQTT.Username,
		Password: bt.Config.MQTT.Password, Component: Component + "-batches",
	}, []mqttx.Subscription{{Filter: topics.BatchFilter(), Handler: func(_ mqtt.Client, m mqtt.Message) {
		bt.HandleBatchMessage(m.Topic(), m.Payload())
	}}}, bt.Logger)
	if err != nil {
		return err
	}
	defer mqttx.Disconnect(batchClient, Component+"-batches")

	src, err := bt.newSource()
	if err != nil {
		return err
	}
	defer src.Close()
	bt.Logger.Infof("Reading Tilts from %s source, cycle %s", src.Name(), bt.Config.Source.CycleInterval)

	for {
		readings, err := src.Collect(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			bt.Logger.Errorf("collect from %s: %s", src.Name(), err)
			continue
		}
		if bt.OnReading != nil {
			for _, r := range readings {
				bt.OnReading(r)
			}
		}
		bt.Cycle(ctx, src.Name(), readings)
	}
}

// HandleBatchMessage applies one message from brewery/batch/v1/+.
func (bt *BrewTracker) HandleBatchMessage(topic string, payload []byte) {
	if contract.IsBatchIndex(topic) {
		return
	}
	id := contract.LastLevel(topic)
	state, ok, err := contract.DecodeBatchState(payload)

	ctx := mqttx.Extract(context.Background(), state.Trace.Carrier())
	_, span := bt.tracer.Start(ctx, "receive "+topic,
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			semconv.MessagingSystemKey.String("mqtt"),
			semconv.MessagingOperationTypeReceive,
			semconv.MessagingDestinationName(topic),
			semconv.MessagingMessageBodySize(len(payload)),
			attribute.String("brewfather.batch.id", id),
			attribute.Bool("batch.cleared", !ok && err == nil),
		))
	defer span.End()

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "decode failed")
		bt.Logger.Warnf("batch state %s: %s", topic, err)
		return
	}

	bt.mu.Lock()
	defer bt.mu.Unlock()
	prev, had := bt.batches[id]
	if !ok {
		if had {
			bt.metrics.deleteBatch(id)
			delete(bt.batches, id)
			bt.Logger.Infof("Batch %s (%s) is no longer active; stopped tracking", prev.Name, id)
		}
		return
	}
	// Name, status or bindings changed: drop old label sets before re-adding.
	if had && (prev.Name != state.Name || prev.Status != state.Status || !sameStrings(prev.Devices.Tilt, state.Devices.Tilt)) {
		bt.metrics.deleteBatch(id)
	}
	bt.batches[id] = state
	colors := state.Devices.Tilt
	if len(colors) == 0 {
		colors = []string{""}
	}
	for _, c := range colors {
		bt.metrics.batchInfo.WithLabelValues(state.ID, state.Name, state.Status, c).Set(1)
	}
	span.SetAttributes(attribute.String("brewfather.batch.name", state.Name), attribute.String("brewfather.batch.status", state.Status))
	if !had {
		bt.Logger.Infof("Tracking batch %s (%s, %s), Tilt %v", state.Name, id, state.Status, state.Devices.Tilt)
	}
}

// ActiveBatches returns a snapshot sorted by id.
func (bt *BrewTracker) ActiveBatches() []contract.BatchStateV1 {
	bt.mu.Lock()
	defer bt.mu.Unlock()
	out := make([]contract.BatchStateV1, 0, len(bt.batches))
	for _, b := range bt.batches {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Cycle matches readings to active batches and updates metrics + Brewfather.
func (bt *BrewTracker) Cycle(ctx context.Context, sourceName string, readings source.Readings) {
	batches := bt.ActiveBatches()
	ctx, span := bt.tracer.Start(ctx, "brewtracker.cycle", trace.WithAttributes(
		attribute.String("tilt.source", sourceName),
		attribute.Int("tilt.readings", len(readings)),
		attribute.Int("brewfather.active_batches", len(batches)),
	))
	defer span.End()

	for i := range batches {
		for _, color := range batches[i].Devices.Tilt {
			r, ok := readings[source.NormalizeColor(color)]
			if !ok {
				continue
			}
			bt.updateBatch(ctx, &batches[i], r)
		}
	}
}

func (bt *BrewTracker) updateBatch(ctx context.Context, batch *contract.BatchStateV1, r source.Reading) {
	opts := []trace.SpanStartOption{trace.WithAttributes(
		attribute.String("brewfather.batch.id", batch.ID),
		attribute.String("brewfather.batch.name", batch.Name),
		attribute.String("tilt.color", r.Color),
		attribute.Float64("tilt.gravity", r.Gravity),
		attribute.Float64("tilt.temp_f", r.TempF),
		attribute.Float64("tilt.reading_age_seconds", time.Since(r.Observed).Seconds()),
	)}
	if r.SpanContext.IsValid() {
		opts = append(opts, trace.WithLinks(trace.Link{SpanContext: r.SpanContext}))
	}
	ctx, span := bt.tracer.Start(ctx, "brewtracker.update_batch", opts...)
	defer span.End()

	// Hold the lock while writing series so a concurrent deactivation can't
	// delete them and have this cycle re-create them.
	bt.mu.Lock()
	if _, still := bt.batches[batch.ID]; !still {
		bt.mu.Unlock()
		return
	}
	m := bt.metrics
	m.beerReading.WithLabelValues(r.Color).Inc()
	m.beerMeasuredOriginalGravity.WithLabelValues(batch.ID, batch.Name).Set(batch.Targets.MeasuredOG)
	m.beerEstimatedFinalGravity.WithLabelValues(batch.ID, batch.Name).Set(batch.Targets.EstimatedFG)
	m.beerEstimatedIbu.WithLabelValues(batch.ID, batch.Name).Set(batch.Targets.EstimatedIBU)
	m.beerEstimatedSrm.WithLabelValues(batch.ID, batch.Name).Set(batch.Targets.EstimatedSRM)
	m.beerGravity.WithLabelValues(batch.ID, batch.Name, r.Color).Set(r.Gravity)
	m.beerTemperatureF.WithLabelValues(batch.ID, batch.Name, r.Color).Set(r.TempF)
	m.beerTemperatureC.WithLabelValues(batch.ID, batch.Name, r.Color).Set(r.Celsius())
	bt.mu.Unlock()

	for _, name := range batch.Devices.Streams {
		w, ok := bt.webhooks[name]
		if !ok || !w.Enabled() {
			continue
		}
		sent, err := w.Update(ctx, &brewfather.BrewTrackerStatus{
			Name:        "BrewTracker" + r.Color,
			Temperature: float32(r.TempF),
			TempUnit:    "F",
			Gravity:     r.Gravity,
			GravityUnit: "G",
		})
		if err != nil {
			bt.Logger.Errorf("Unable to update stream %s for %s: %s", name, batch.Name, err)
			span.RecordError(err)
			span.SetStatus(codes.Error, "webhook update failed")
			continue
		}
		if sent {
			bt.Logger.Infof("Updated Brewfather stream %s for %s (%s): sg=%.3f temp_f=%.0f",
				name, batch.Name, r.Color, r.Gravity, r.TempF)
		}
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ErrNoBroker is returned when no broker is configured: batch state now
// always comes from the broker.
var ErrNoBroker = errors.New("mqtt.broker is required: batch state is read from the broker")
