// Package batchsync is the only component that talks to the Brewfather API.
// It polls for batches in the active statuses and publishes their state,
// retained, to brewery/batch/v1/<id>. When a batch leaves the active set it
// publishes an EMPTY retained payload there, which tells every consumer to
// drop the batch. Brewfather's status is the authority; this never writes it.
package batchsync

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/jtway/go-tilt-exporter/pkg/brewfather"
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

const (
	tracerName = "github.com/jtway/go-tilt-exporter/pkg/batchsync"
	Component  = "batch-sync"
)

// Config for batch-sync.
type Config struct {
	RefreshInterval time.Duration `mapstructure:"refresh_interval"`
	// Statuses that count as active. Default [Fermenting].
	Statuses []string `mapstructure:"statuses"`
	// TrackConditioning adds Conditioning to Statuses (cold crash tracking).
	TrackConditioning bool `mapstructure:"track_conditioning"`
	// MQTT credentials for the batch-sync user (needs write on batch topics).
	MQTTUsername string `mapstructure:"mqtt_username"`
	MQTTPassword string `mapstructure:"mqtt_password"`
}

// ActiveStatuses resolves the configured statuses.
func (c Config) ActiveStatuses() []brewfather.Status {
	seen := map[string]bool{}
	var out []brewfather.Status
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		s = strings.ToUpper(s[:1]) + strings.ToLower(s[1:])
		if !seen[s] {
			seen[s] = true
			out = append(out, brewfather.Status(s))
		}
	}
	for _, s := range c.Statuses {
		add(s)
	}
	if len(out) == 0 {
		add(string(brewfather.Fermenting))
	}
	if c.TrackConditioning {
		add(string(brewfather.Conditioning))
	}
	return out
}

// API is the part of the Brewfather client batch-sync uses.
type API interface {
	GetActiveBatches(ctx context.Context, statuses []brewfather.Status) ([]brewfather.Batch, error)
}

// Publisher is the part of an MQTT client batch-sync uses.
type Publisher interface {
	Publish(topic string, retained bool, payload []byte) error
}

var (
	lastSuccess = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "brewtracker", Subsystem: "batchsync", Name: "last_success_timestamp_seconds",
		Help: "Unix time of the last successful Brewfather refresh.",
	})
	refreshErrors = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "brewtracker", Subsystem: "batchsync", Name: "refresh_errors_total",
		Help: "Failed Brewfather refreshes. Published state is kept as-is on failure.",
	})
	activeBatches = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "brewtracker", Subsystem: "batchsync", Name: "active_batches",
		Help: "Batches in an active status as of the last successful refresh.",
	})
)

// Syncer polls Brewfather and publishes batch state.
type Syncer struct {
	cfg      Config
	statuses []brewfather.Status
	api      API
	pub      Publisher
	topics   contract.Topics
	logger   *zap.SugaredLogger
	tracer   trace.Tracer

	mu        sync.Mutex
	published map[string]bool // batch ids with a non-empty retained state
}

// New builds a Syncer. Use Run for the real broker; tests drive Refresh.
func New(cfg Config, api API, pub Publisher, topics contract.Topics, logger *zap.SugaredLogger) *Syncer {
	if cfg.RefreshInterval <= 0 {
		cfg.RefreshInterval = 5 * time.Minute
	}
	return &Syncer{
		cfg: cfg, statuses: cfg.ActiveStatuses(), api: api, pub: pub, topics: topics,
		logger: logger, tracer: otel.Tracer(tracerName), published: map[string]bool{},
	}
}

// Seed marks batch ids that already have retained state (from before a
// restart), so they get cleared if they're no longer active.
func (s *Syncer) Seed(ids []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		s.published[id] = true
	}
}

// Refresh polls once and publishes the result. On an API error nothing is
// published: consumers keep the last known state rather than dropping
// batches because Brewfather hiccuped.
func (s *Syncer) Refresh(ctx context.Context) (err error) {
	ctx, span := s.tracer.Start(ctx, "batchsync.refresh")
	defer func() {
		if err != nil {
			refreshErrors.Inc()
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}()
	statusNames := make([]string, len(s.statuses))
	for i, st := range s.statuses {
		statusNames[i] = string(st)
	}
	span.SetAttributes(attribute.StringSlice("brewfather.statuses", statusNames))

	batches, err := s.api.GetActiveBatches(ctx, s.statuses)
	if err != nil {
		return fmt.Errorf("refresh batches: %w", err)
	}

	now := time.Now()
	active := map[string]bool{}
	var publishErr error
	for i := range batches {
		b := &batches[i]
		state := toState(b, now)
		active[b.Id] = true
		if err := s.publishState(ctx, s.topics.Batch(b.Id), &state.Trace, state); err != nil {
			publishErr = err
			continue
		}
		s.mu.Lock()
		s.published[b.Id] = true
		s.mu.Unlock()
	}

	// Tombstone everything that was published but is no longer active.
	s.mu.Lock()
	var gone []string
	for id := range s.published {
		if !active[id] {
			gone = append(gone, id)
		}
	}
	s.mu.Unlock()
	sort.Strings(gone)
	for _, id := range gone {
		if err := s.clear(ctx, id); err != nil {
			publishErr = err
			continue
		}
		s.mu.Lock()
		delete(s.published, id)
		s.mu.Unlock()
		s.logger.Infof("batch-sync: batch %s no longer active; cleared", id)
	}

	ids := make([]string, 0, len(active))
	for id := range active {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	index := contract.BatchIndexV1{V: 1, Batches: ids, Statuses: statusNames, Updated: now.Unix()}
	if err := s.publishState(ctx, s.topics.BatchIndex(), &index.Trace, &index); err != nil {
		publishErr = err
	}

	span.SetAttributes(attribute.Int("brewfather.active_batches", len(ids)), attribute.Int("batchsync.cleared", len(gone)))
	if publishErr != nil {
		return fmt.Errorf("publish: %w", publishErr)
	}
	lastSuccess.Set(float64(now.Unix()))
	activeBatches.Set(float64(len(ids)))
	s.logger.Infof("batch-sync: %d active batch(es) in %v", len(ids), statusNames)
	return nil
}

// publishState injects the current trace into the payload's Trace field,
// marshals v and publishes it retained under a producer span.
func (s *Syncer) publishState(ctx context.Context, topic string, tr *contract.Trace, v any) error {
	ctx, span := s.tracer.Start(ctx, "publish "+topic, trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			semconv.MessagingSystemKey.String("mqtt"),
			semconv.MessagingOperationTypeSend,
			semconv.MessagingOperationName("publish"),
			semconv.MessagingDestinationName(topic),
			attribute.Bool("mqtt.retained", true),
		))
	defer span.End()
	*tr = contract.TraceFrom(mqttx.Inject(ctx))
	b, err := json.Marshal(v)
	if err != nil {
		span.RecordError(err)
		return err
	}
	span.SetAttributes(semconv.MessagingMessageBodySize(len(b)))
	if err := s.pub.Publish(topic, true, b); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "publish failed")
		return err
	}
	return nil
}

func (s *Syncer) clear(ctx context.Context, id string) error {
	topic := s.topics.Batch(id)
	_, span := s.tracer.Start(ctx, "publish "+topic, trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			semconv.MessagingSystemKey.String("mqtt"),
			semconv.MessagingDestinationName(topic),
			attribute.Bool("mqtt.retained", true),
			attribute.Bool("batch.cleared", true),
		))
	defer span.End()
	if err := s.pub.Publish(topic, true, []byte{}); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "publish failed")
		return err
	}
	return nil
}

func toState(b *brewfather.Batch, now time.Time) contract.BatchStateV1 {
	st := contract.BatchStateV1{
		V:       1,
		ID:      b.Id,
		Name:    b.Name,
		BatchNo: b.BatchNumber,
		Status:  string(b.Status),
		Devices: contract.BatchDevices{Tilt: b.TiltColors(), Streams: b.StreamNames()},
		Targets: contract.BatchTargets{
			EstimatedOG:  b.EstimatedOg,
			EstimatedFG:  b.EstimatedFg,
			MeasuredOG:   float64(b.MeasuredOg),
			EstimatedIBU: float64(b.EstimatedIbu),
			EstimatedSRM: float64(b.EstimatedColor),
		},
		Updated: now.Unix(),
	}
	if r := b.Recipe; r != nil {
		if r.Mash != nil {
			for _, x := range r.Mash.Steps {
				st.Targets.Mash = append(st.Targets.Mash, contract.Step{Name: x.Name, Type: x.Type, TempC: x.StepTemp, Time: x.StepTime})
			}
		}
		if r.Fermentation != nil {
			for _, x := range r.Fermentation.Steps {
				st.Targets.Fermentation = append(st.Targets.Fermentation, contract.Step{Name: x.Name, Type: x.Type, TempC: x.StepTemp, Time: x.StepTime})
			}
		}
	}
	return st
}

// mqttPublisher adapts a paho client.
type mqttPublisher struct{ c mqtt.Client }

func (p mqttPublisher) Publish(topic string, retained bool, payload []byte) error {
	tok := p.c.Publish(topic, 1, retained, payload)
	if !tok.WaitTimeout(10 * time.Second) {
		return fmt.Errorf("publish %s: timeout", topic)
	}
	return tok.Error()
}

// Run connects as the batch-sync user, learns which batch topics already hold
// retained state, then refreshes immediately and every RefreshInterval.
func Run(ctx context.Context, cfg Config, broker string, api API, topics contract.Topics, logger *zap.SugaredLogger) error {
	var mu sync.Mutex
	var existing []string
	learn := func(_ mqtt.Client, m mqtt.Message) {
		if contract.IsBatchIndex(m.Topic()) || len(m.Payload()) == 0 {
			return
		}
		mu.Lock()
		existing = append(existing, contract.LastLevel(m.Topic()))
		mu.Unlock()
	}
	client, err := mqttx.Connect(mqttx.Config{
		Broker: broker, Username: cfg.MQTTUsername, Password: cfg.MQTTPassword, Component: Component,
	}, nil, logger)
	if err != nil {
		return err
	}
	defer mqttx.Disconnect(client, Component)

	// Wait for the connection, then read retained batch topics briefly.
	for !client.IsConnectionOpen() {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
	}
	if tok := client.Subscribe(topics.BatchFilter(), 1, learn); tok.WaitTimeout(10*time.Second) && tok.Error() != nil {
		return fmt.Errorf("batch-sync: subscribe %s: %w", topics.BatchFilter(), tok.Error())
	}
	select {
	case <-ctx.Done():
		return nil
	case <-time.After(2 * time.Second):
	}
	client.Unsubscribe(topics.BatchFilter()).WaitTimeout(5 * time.Second)

	s := New(cfg, api, mqttPublisher{client}, topics, logger)
	mu.Lock()
	s.Seed(existing)
	mu.Unlock()
	logger.Infof("batch-sync: %d retained batch(es) found on startup; statuses %v; refresh %s",
		len(existing), s.statuses, s.cfg.RefreshInterval)

	ticker := time.NewTicker(s.cfg.RefreshInterval)
	defer ticker.Stop()
	for {
		if err := s.Refresh(ctx); err != nil {
			logger.Errorf("batch-sync: %s", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
