package brewtracker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jtway/go-tilt-exporter/pkg/brewfather"
	"github.com/jtway/go-tilt-exporter/pkg/source"
	"github.com/spf13/viper"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

const tracerName = "github.com/jtway/go-tilt-exporter/pkg/brewtracker"

type BrewTracker struct {
	Config  *Config
	metrics *metrics
	Logger  *zap.SugaredLogger

	BrewfatherClient     *brewfather.BrewfatherClient
	brewFatherLastUpdate time.Time
	batches              []brewfather.Batch

	tracer trace.Tracer
}

func NewBrewTracker() (*BrewTracker, error) {
	bt := &BrewTracker{
		metrics: NewMetrics(),
		Logger:  zap.NewExample().Sugar(),
		tracer:  otel.Tracer(tracerName),
	}

	config, err := ReadInConfig()
	bt.Logger.Infof("Read Config File: %s", viper.ConfigFileUsed())
	if err != nil {
		return nil, fmt.Errorf("failed to read and set config: %w", err)
	}
	bt.Config = config
	bt.BrewfatherClient = brewfather.NewBrewfatherClient(&config.Brewfather, bt.Logger)
	return bt, nil
}

func (bt *BrewTracker) newSource() (source.Source, error) {
	switch bt.Config.Source.Type {
	case "mqtt":
		return source.NewMQTT(bt.Config.MQTT, bt.Config.Source.CycleInterval,
			bt.Config.Source.MaxReadingAge, bt.Logger)
	default:
		return source.NewBLE(bt.Config.Source.CycleInterval, bt.Config.Source.Receiver), nil
	}
}

// Run processes readings every cycle until ctx is cancelled.
func (bt *BrewTracker) Run(ctx context.Context) error {
	defer bt.Logger.Sync()

	bt.Logger.Infof("Fetching initial batches")
	if err := bt.refreshBatches(ctx); err != nil {
		return fmt.Errorf("unable to retrieve batches: %w", err)
	}

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
		bt.cycle(ctx, src.Name(), readings)
	}
}

func (bt *BrewTracker) refreshBatches(ctx context.Context) error {
	batches, err := bt.BrewfatherClient.GetActiveBatches(ctx)
	bt.brewFatherLastUpdate = time.Now()
	if err != nil {
		return err
	}
	// Only swap on a non-empty response, matching the original behavior.
	if len(batches) > 0 || bt.batches == nil {
		bt.batches = batches
	}
	bt.Logger.Infof("Working with %d active batches", len(bt.batches))
	return nil
}

// cycle is one processing pass: refresh batches if due, then match readings
// to batches and update metrics + Brewfather.
func (bt *BrewTracker) cycle(ctx context.Context, sourceName string, readings source.Readings) {
	ctx, span := bt.tracer.Start(ctx, "brewtracker.cycle", trace.WithAttributes(
		attribute.String("tilt.source", sourceName),
		attribute.Int("tilt.readings", len(readings)),
	))
	defer span.End()

	if time.Since(bt.brewFatherLastUpdate) >= bt.Config.Brewfather.UpdateInterval {
		bt.Logger.Infof("Fetching updated active batches.")
		if err := bt.refreshBatches(ctx); err != nil {
			bt.Logger.Errorf("Unable to retrieve batches, %s", err)
			span.RecordError(err)
		}
	}
	span.SetAttributes(attribute.Int("brewfather.active_batches", len(bt.batches)))

	for _, r := range readings {
		bt.metrics.tiltRssi.WithLabelValues(r.Color, r.Receiver).Set(float64(r.RSSI))
		bt.metrics.tiltLastReading.WithLabelValues(r.Color).Set(float64(r.Observed.Unix()))
	}

	for i := range bt.batches {
		bt.updateBatch(ctx, &bt.batches[i], readings)
	}
}

func (bt *BrewTracker) updateBatch(ctx context.Context, batch *brewfather.Batch, readings source.Readings) {
	batchTilt := batch.GetTilt()
	if batchTilt == nil {
		bt.Logger.Infof("No Tilt found for batch %s", batch.Name)
		return
	}
	tiltName := batchTilt.Name
	if tiltName == "" {
		tiltName = string(batchTilt.Key)
	}
	color := source.NormalizeColor(tiltName)
	r, ok := readings[color]
	if !ok {
		bt.Logger.Infof("Requested tilt %s not found for batch %s", tiltName, batch.Name)
		return
	}

	opts := []trace.SpanStartOption{trace.WithAttributes(
		attribute.String("brewfather.batch.id", batch.Id),
		attribute.String("brewfather.batch.name", batch.Name),
		attribute.String("tilt.color", color),
		attribute.Float64("tilt.gravity", r.Gravity),
		attribute.Float64("tilt.temp_f", r.TempF),
		attribute.Float64("tilt.reading_age_seconds", time.Since(r.Observed).Seconds()),
	)}
	// Link back to the span that received this reading (MQTT message / BLE scan).
	if r.SpanContext.IsValid() {
		opts = append(opts, trace.WithLinks(trace.Link{SpanContext: r.SpanContext}))
	}
	ctx, span := bt.tracer.Start(ctx, "brewtracker.update_batch", opts...)
	defer span.End()

	bt.metrics.beerReading.WithLabelValues(color).Inc()
	bt.metrics.beerMeasuredOriginalGravity.WithLabelValues(batch.Id, batch.Name).Set(float64(batch.MeasuredOg))
	bt.metrics.beerEstimatedFinalGravity.WithLabelValues(batch.Id, batch.Name).Set(batch.EstimatedFg)
	bt.metrics.beerEstimatedIbu.WithLabelValues(batch.Id, batch.Name).Set(float64(batch.EstimatedIbu))
	bt.metrics.beerEstimatedSrm.WithLabelValues(batch.Id, batch.Name).Set(float64(batch.EstimatedColor))
	bt.metrics.beerGravity.WithLabelValues(batch.Id, batch.Name, color).Set(r.Gravity)
	bt.metrics.beerTemperatureF.WithLabelValues(batch.Id, batch.Name, color).Set(r.TempF)
	bt.metrics.beerTemperatureC.WithLabelValues(batch.Id, batch.Name, color).Set(r.Celsius())

	sent, err := batch.UpdateWebhook(ctx, &brewfather.BrewTrackerStatus{
		Name:        "BrewTracker" + color,
		Temperature: float32(r.TempF),
		TempUnit:    "F",
		Gravity:     r.Gravity,
		GravityUnit: "G",
	})
	if err != nil {
		// A batch without a stream attached isn't a failure of this cycle.
		if errors.Is(err, brewfather.ErrNoWebhook) {
			return
		}
		bt.Logger.Errorf("Unable to update via webhook: %s", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, "webhook update failed")
		return
	}
	if sent {
		bt.Logger.Infof("Updated Brewfather stream for %s (%s): sg=%.3f temp_f=%.0f",
			batch.Name, color, r.Gravity, r.TempF)
	}
}
