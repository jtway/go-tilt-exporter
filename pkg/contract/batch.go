package contract

import (
	"encoding/json"
	"fmt"
)

// BatchStateV1 is published retained on brewery/batch/v1/<id> by batch-sync
// for every batch in an active status. An EMPTY retained payload on that topic
// means the batch is no longer active: consumers must drop it and delete any
// series they export for it.
type BatchStateV1 struct {
	V       int          `json:"v"`
	ID      string       `json:"id"`
	Name    string       `json:"name"`
	BatchNo uint32       `json:"batchNo,omitempty"`
	Status  string       `json:"status"`
	Devices BatchDevices `json:"devices"`
	Targets BatchTargets `json:"targets"`
	// Updated is unix seconds when batch-sync last refreshed this batch.
	Updated int64 `json:"updated"`
	Trace
}

// BatchDevices are the bindings from the Brewfather batch.
type BatchDevices struct {
	// Tilt colors bound and enabled on the batch, title case ("Blue").
	Tilt []string `json:"tilt,omitempty"`
	// Custom stream names attached to the batch; brewtracker posts to the
	// webhook configured under the same name.
	Streams []string `json:"streams,omitempty"`
}

// BatchTargets are values from the recipe/batch used for comparisons.
type BatchTargets struct {
	EstimatedOG  float64 `json:"estimatedOg,omitempty"`
	EstimatedFG  float64 `json:"estimatedFg,omitempty"`
	MeasuredOG   float64 `json:"measuredOg,omitempty"`
	EstimatedIBU float64 `json:"estimatedIbu,omitempty"`
	EstimatedSRM float64 `json:"estimatedSrm,omitempty"`
	// Recipe steps when Brewfather returns them. Temperatures are °C (the API
	// returns metric units); Time is minutes for mash, days for fermentation.
	Mash         []Step `json:"mash,omitempty"`
	Fermentation []Step `json:"fermentation,omitempty"`
}

// Step is one mash or fermentation step.
type Step struct {
	Name  string  `json:"name,omitempty"`
	Type  string  `json:"type,omitempty"`
	TempC float64 `json:"tempC"`
	Time  float64 `json:"time,omitempty"`
}

// BatchIndexV1 is published retained on brewery/batch/v1/index.
type BatchIndexV1 struct {
	V        int      `json:"v"`
	Batches  []string `json:"batches"`
	Statuses []string `json:"statuses"`
	Updated  int64    `json:"updated"`
	Trace
}

// DecodeBatchState parses a batch topic payload. ok=false with a nil error
// means the payload was empty: the batch was deactivated.
func DecodeBatchState(b []byte) (s BatchStateV1, ok bool, err error) {
	if len(b) == 0 {
		return s, false, nil
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, false, fmt.Errorf("decode batch state: %w", err)
	}
	if s.V != 1 {
		return s, false, fmt.Errorf("unsupported batch state version %d", s.V)
	}
	if s.ID == "" {
		return s, false, fmt.Errorf("batch state has no id")
	}
	return s, true, nil
}
