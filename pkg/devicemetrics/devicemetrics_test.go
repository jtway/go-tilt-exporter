package devicemetrics

import (
	"testing"
	"time"

	"github.com/jtway/go-tilt-exporter/pkg/source"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
)

func TestObserveAndSweep(t *testing.T) {
	e := New(5*time.Minute, zap.NewNop().Sugar())
	now := time.Now()
	e.Observe(source.Reading{Color: "Blue", Gravity: 1.007, TempF: 67, RSSI: -79, Receiver: "rx1", Observed: now})
	e.Observe(source.Reading{Color: "Red", Gravity: 1.000, TempF: 70, RSSI: -60, Receiver: "rx1", Observed: now.Add(-10 * time.Minute)})

	if got := testutil.ToFloat64(gravity.WithLabelValues("Blue")); got != 1.007 {
		t.Fatalf("gravity = %v", got)
	}
	// Receiver change replaces the RSSI series rather than adding one.
	e.Observe(source.Reading{Color: "Blue", Gravity: 1.007, TempF: 67, RSSI: -70, Receiver: "rx2", Observed: now})
	if n := testutil.CollectAndCount(rssi); n != 2 {
		t.Fatalf("rssi series = %d, want 2 (Blue/rx2, Red/rx1)", n)
	}

	e.Sweep(now)
	if n := testutil.CollectAndCount(gravity); n != 1 {
		t.Fatalf("stale Red should be swept; gravity series = %d", n)
	}
	if n := testutil.CollectAndCount(rssi); n != 1 {
		t.Fatalf("rssi series after sweep = %d", n)
	}
}
