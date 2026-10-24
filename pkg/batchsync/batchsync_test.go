package batchsync

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/jtway/go-tilt-exporter/pkg/brewfather"
	"github.com/jtway/go-tilt-exporter/pkg/contract"
	"go.uber.org/zap"
)

type fakeAPI struct {
	batches []brewfather.Batch
	err     error
	got     []brewfather.Status
}

func (f *fakeAPI) GetActiveBatches(_ context.Context, st []brewfather.Status) ([]brewfather.Batch, error) {
	f.got = st
	return f.batches, f.err
}

type msg struct {
	topic    string
	retained bool
	payload  []byte
}

type fakePub struct {
	mu   sync.Mutex
	msgs []msg
}

func (p *fakePub) Publish(topic string, retained bool, payload []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.msgs = append(p.msgs, msg{topic, retained, append([]byte(nil), payload...)})
	return nil
}

func (p *fakePub) take() []msg {
	p.mu.Lock()
	defer p.mu.Unlock()
	m := p.msgs
	p.msgs = nil
	return m
}

func batch(id, name string, status brewfather.Status, tilt string) brewfather.Batch {
	var b brewfather.Batch
	b.Id, b.Name, b.Status = id, name, status
	b.EstimatedFg = 1.012
	b.MeasuredOg = 1.061
	b.Devices.Tilt.Items = []brewfather.TiltDevice{{Name: tilt, Key: brewfather.TiltKey(tilt), Enabled: true}}
	b.Devices.Streams.Streams = []brewfather.Stream{{Name: "brewtracker"}}
	b.Recipe = &brewfather.BatchRecipe{}
	b.Recipe.Mash = &struct {
		Steps []brewfather.RecipeStep `json:"steps"`
	}{Steps: []brewfather.RecipeStep{{Name: "Sacch", StepTemp: 66, StepTime: 60}}}
	return b
}

func TestRefreshPublishesThenClears(t *testing.T) {
	api := &fakeAPI{batches: []brewfather.Batch{batch("b1", "Julius", brewfather.Fermenting, "BLUE")}}
	pub := &fakePub{}
	s := New(Config{}, api, pub, contract.Topics{}, zap.NewNop().Sugar())

	if err := s.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(api.got) != 1 || api.got[0] != brewfather.Fermenting {
		t.Fatalf("statuses = %v", api.got)
	}
	m := pub.take()
	if len(m) != 2 || m[0].topic != "brewery/batch/v1/b1" || !m[0].retained || m[1].topic != "brewery/batch/v1/index" {
		t.Fatalf("published %+v", m)
	}
	st, ok, err := contract.DecodeBatchState(m[0].payload)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if st.Devices.Tilt[0] != "Blue" || st.Devices.Streams[0] != "brewtracker" || st.Targets.EstimatedFG != 1.012 ||
		len(st.Targets.Mash) != 1 || st.Targets.Mash[0].TempC != 66 {
		t.Fatalf("state %+v", st)
	}

	// The last fermenting batch moved to Conditioning: an EMPTY active list
	// must clear it (the old code kept the stale list forever).
	api.batches = nil
	if err := s.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	m = pub.take()
	if len(m) != 2 || m[0].topic != "brewery/batch/v1/b1" || len(m[0].payload) != 0 || !m[0].retained {
		t.Fatalf("want tombstone then index, got %+v", m)
	}
	var idx contract.BatchIndexV1
	json.Unmarshal(m[1].payload, &idx)
	if len(idx.Batches) != 0 {
		t.Fatalf("index %+v", idx)
	}
}

func TestRefreshErrorPublishesNothing(t *testing.T) {
	api := &fakeAPI{err: errors.New("429")}
	pub := &fakePub{}
	s := New(Config{}, api, pub, contract.Topics{}, zap.NewNop().Sugar())
	s.Seed([]string{"b1"})
	if err := s.Refresh(context.Background()); err == nil {
		t.Fatal("want error")
	}
	if m := pub.take(); len(m) != 0 {
		t.Fatalf("published on error: %+v", m)
	}
}

func TestSeededStaleBatchIsCleared(t *testing.T) {
	api := &fakeAPI{batches: []brewfather.Batch{batch("b2", "Pils", brewfather.Fermenting, "RED")}}
	pub := &fakePub{}
	s := New(Config{}, api, pub, contract.Topics{}, zap.NewNop().Sugar())
	s.Seed([]string{"old"}) // retained before a restart; finished since
	if err := s.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	var cleared bool
	for _, m := range pub.take() {
		if m.topic == "brewery/batch/v1/old" && len(m.payload) == 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("stale retained batch not cleared")
	}
}

func TestActiveStatuses(t *testing.T) {
	got := Config{TrackConditioning: true}.ActiveStatuses()
	if len(got) != 2 || got[0] != brewfather.Fermenting || got[1] != brewfather.Conditioning {
		t.Fatalf("%v", got)
	}
	got = Config{Statuses: []string{"brewing", "Fermenting"}}.ActiveStatuses()
	if len(got) != 2 || got[0] != brewfather.Brewing {
		t.Fatalf("%v", got)
	}
}
