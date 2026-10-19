package source

import (
	"encoding/json"
	"fmt"
	"time"
)

// PayloadV1 is the JSON published by tilt-receiver (ESPHome) on
// brewery/tilt/v1/<color>. Traceparent is optional: the ESP32 can't originate
// traces, but a future in-cluster publisher can, and the subscriber will then
// continue that trace.
type PayloadV1 struct {
	V           int     `json:"v"`
	Color       string  `json:"color"`
	TempF       float64 `json:"temp_f"`
	SG          float64 `json:"sg"`
	RSSI        int     `json:"rssi"`
	TxPower     int     `json:"tx_power"`
	Receiver    string  `json:"receiver"`
	TS          int64   `json:"ts"`
	Traceparent string  `json:"traceparent,omitempty"`
	Tracestate  string  `json:"tracestate,omitempty"`
}

// DecodePayload parses and validates a v1 payload. topicColor is the color
// from the topic, used when the payload omits it.
func DecodePayload(b []byte, topicColor string, received time.Time) (Reading, PayloadV1, error) {
	var p PayloadV1
	if err := json.Unmarshal(b, &p); err != nil {
		return Reading{}, p, fmt.Errorf("decode payload: %w", err)
	}
	if p.V != 1 {
		return Reading{}, p, fmt.Errorf("unsupported payload version %d", p.V)
	}
	color := NormalizeColor(p.Color)
	if color == "" {
		color = NormalizeColor(topicColor)
	}
	if color == "" {
		return Reading{}, p, fmt.Errorf("payload has no color")
	}
	// Sanity bounds: a Tilt v2 reports roughly 0.990–1.200 and whole °F.
	if p.SG < 0.9 || p.SG > 1.3 {
		return Reading{}, p, fmt.Errorf("gravity %.3f out of range", p.SG)
	}
	observed := received
	if p.TS > 0 {
		observed = time.Unix(p.TS, 0)
	}
	return Reading{
		Color:    color,
		TempF:    p.TempF,
		Gravity:  p.SG,
		RSSI:     p.RSSI,
		TxPower:  p.TxPower,
		Receiver: p.Receiver,
		Observed: observed,
		Received: received,
	}, p, nil
}
