package contract

import (
	"encoding/json"
	"fmt"
)

// TiltReadingV1 is published retained on brewery/tilt/v1/<color> by
// tilt-receiver (ESPHome). See esphome/tilt-receiver.yaml.
type TiltReadingV1 struct {
	V        int     `json:"v"`
	Color    string  `json:"color"`
	TempF    float64 `json:"temp_f"`
	SG       float64 `json:"sg"`
	RSSI     int     `json:"rssi"`
	TxPower  int     `json:"tx_power"`
	Receiver string  `json:"receiver"`
	TS       int64   `json:"ts"`
	Trace
}

// DecodeTiltReading parses and validates a v1 reading.
func DecodeTiltReading(b []byte) (TiltReadingV1, error) {
	var p TiltReadingV1
	if err := json.Unmarshal(b, &p); err != nil {
		return p, fmt.Errorf("decode payload: %w", err)
	}
	if p.V != 1 {
		return p, fmt.Errorf("unsupported payload version %d", p.V)
	}
	// Sanity bounds: a Tilt v2 reports roughly 0.990–1.200.
	if p.SG < 0.9 || p.SG > 1.3 {
		return p, fmt.Errorf("gravity %.3f out of range", p.SG)
	}
	return p, nil
}
