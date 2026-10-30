package brewtracker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jtway/go-tilt-exporter/pkg/brewfather"
	"github.com/jtway/go-tilt-exporter/pkg/contract"
	"github.com/jtway/go-tilt-exporter/pkg/source"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
)

func stateJSON(t *testing.T, s contract.BatchStateV1) []byte {
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBatchLifecycle(t *testing.T) {
	var posts atomic.Int32
	stream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		posts.Add(1)
		w.Write([]byte(`{"result":"OK"}`))
	}))
	defer stream.Close()

	cfg := &Config{Brewfather: brewfather.Config{Webhooks: []brewfather.WebhookConfig{
		{Name: "brewtracker", Url: stream.URL, UpdateInterval: time.Hour, Enabled: true},
	}}}
	bt := NewBrewTracker(cfg, zap.NewNop().Sugar())
	m := bt.metrics

	st := contract.BatchStateV1{V: 1, ID: "t1", Name: "Julius", Status: "Fermenting",
		Devices: contract.BatchDevices{Tilt: []string{"Blue"}, Streams: []string{"brewtracker"}},
		Targets: contract.BatchTargets{MeasuredOG: 1.061, EstimatedFG: 1.012}}
	bt.HandleBatchMessage("brewery/batch/v1/t1", stateJSON(t, st))
	bt.HandleBatchMessage("brewery/batch/v1/index", []byte(`{"v":1}`)) // ignored

	if got := testutil.ToFloat64(m.batchInfo.WithLabelValues("t1", "Julius", "Fermenting", "Blue")); got != 1 {
		t.Fatalf("batch_info = %v", got)
	}

	readings := source.Readings{
		"Blue": {Color: "Blue", Gravity: 1.020, TempF: 66, Observed: time.Now()},
		"Red":  {Color: "Red", Gravity: 1.000, TempF: 70, Observed: time.Now()}, // unbound Tilt
	}
	bt.Cycle(context.Background(), "mqtt", readings)
	if got := testutil.ToFloat64(m.beerGravity.WithLabelValues("t1", "Julius", "Blue")); got != 1.020 {
		t.Fatalf("gravity = %v", got)
	}
	if n := testutil.CollectAndCount(m.beerGravity); n != 1 {
		t.Fatalf("only the bound Tilt should have a batch series, got %d", n)
	}
	if posts.Load() != 1 {
		t.Fatalf("stream posts = %d", posts.Load())
	}

	// Rename: old label set goes away.
	st.Name = "Julius v2"
	bt.HandleBatchMessage("brewery/batch/v1/t1", stateJSON(t, st))
	bt.Cycle(context.Background(), "mqtt", readings)
	if n := testutil.CollectAndCount(m.beerGravity); n != 1 {
		t.Fatalf("after rename want 1 gravity series, got %d", n)
	}

	// Batch leaves Fermenting: empty retained payload → everything removed,
	// and further cycles don't bring it back.
	bt.HandleBatchMessage("brewery/batch/v1/t1", nil)
	bt.Cycle(context.Background(), "mqtt", readings)
	for name, n := range map[string]int{
		"gravity": testutil.CollectAndCount(m.beerGravity),
		"temp_f":  testutil.CollectAndCount(m.beerTemperatureF),
		"og":      testutil.CollectAndCount(m.beerMeasuredOriginalGravity),
		"info":    testutil.CollectAndCount(m.batchInfo),
	} {
		if n != 0 {
			t.Fatalf("%s series left after deactivation: %d", name, n)
		}
	}
	if len(bt.ActiveBatches()) != 0 {
		t.Fatal("batch still tracked")
	}
}
