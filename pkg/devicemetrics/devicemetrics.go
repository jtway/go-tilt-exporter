// Package devicemetrics exports one series per Tilt, independent of any
// batch: a Tilt that's floating in water with no batch attached still shows
// up. Series for a color are deleted once no reading has arrived for
// max_reading_age.
package devicemetrics

import (
	"context"
	"sync"
	"time"

	"github.com/jtway/go-tilt-exporter/pkg/source"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.uber.org/zap"
)

// Component name (MQTT client id suffix, logs).
const Component = "device-metrics"

var (
	gravity = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "brewtracker", Subsystem: "tilt", Name: "gravity",
		Help: "Latest specific gravity per Tilt, regardless of batch.",
	}, []string{"color"})
	tempF = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "brewtracker", Subsystem: "tilt", Name: "temperature_f",
		Help: "Latest temperature (°F) per Tilt, regardless of batch.",
	}, []string{"color"})
	rssi = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "brewtracker", Subsystem: "tilt", Name: "rssi_dbm",
		Help: "Signal strength of the latest reading, as seen by the receiver.",
	}, []string{"color", "receiver"})
	lastReading = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "brewtracker", Subsystem: "tilt", Name: "last_reading_timestamp_seconds",
		Help: "Unix time the latest reading was observed; age = time() - this.",
	}, []string{"color"})
)

type seen struct {
	at       time.Time
	receiver string
}

// Exporter holds device series.
type Exporter struct {
	maxAge time.Duration
	logger *zap.SugaredLogger

	mu   sync.Mutex
	last map[string]seen
}

// New returns an Exporter that drops colors after maxAge without a reading.
func New(maxAge time.Duration, logger *zap.SugaredLogger) *Exporter {
	return &Exporter{maxAge: maxAge, logger: logger, last: map[string]seen{}}
}

// Observe records one reading.
func (e *Exporter) Observe(r source.Reading) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if prev, ok := e.last[r.Color]; ok && prev.receiver != r.Receiver {
		rssi.DeleteLabelValues(r.Color, prev.receiver)
	}
	e.last[r.Color] = seen{at: r.Observed, receiver: r.Receiver}
	gravity.WithLabelValues(r.Color).Set(r.Gravity)
	tempF.WithLabelValues(r.Color).Set(r.TempF)
	rssi.WithLabelValues(r.Color, r.Receiver).Set(float64(r.RSSI))
	lastReading.WithLabelValues(r.Color).Set(float64(r.Observed.Unix()))
}

// Sweep deletes series for colors not seen within maxAge of now.
func (e *Exporter) Sweep(now time.Time) {
	if e.maxAge <= 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for color, s := range e.last {
		if now.Sub(s.at) <= e.maxAge {
			continue
		}
		gravity.DeleteLabelValues(color)
		tempF.DeleteLabelValues(color)
		rssi.DeleteLabelValues(color, s.receiver)
		lastReading.DeleteLabelValues(color)
		delete(e.last, color)
		e.logger.Infof("device-metrics: no reading from %s Tilt for %s; series removed", color, e.maxAge)
	}
}

// RunSweeper sweeps every 30s until ctx is done.
func (e *Exporter) RunSweeper(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			e.Sweep(now)
		}
	}
}

// RunMQTT subscribes to Tilt readings with its own client and blocks until
// ctx is done.
func (e *Exporter) RunMQTT(ctx context.Context, cfg source.MQTTConfig) error {
	sub, err := source.NewMQTT(cfg, Component, 0, e.maxAge, e.logger, e.Observe)
	if err != nil {
		return err
	}
	defer sub.Close()
	e.RunSweeper(ctx)
	return nil
}
