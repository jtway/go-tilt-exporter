package source

import (
	"time"

	"github.com/jtway/go-tilt-exporter/pkg/contract"
)

// PayloadV1 is kept as an alias for the contract type.
type PayloadV1 = contract.TiltReadingV1

// DecodePayload parses and validates a v1 reading. topicColor is the color
// from the topic, used when the payload omits it.
func DecodePayload(b []byte, topicColor string, received time.Time) (Reading, PayloadV1, error) {
	p, err := contract.DecodeTiltReading(b)
	if err != nil {
		return Reading{}, p, err
	}
	color := NormalizeColor(p.Color)
	if color == "" {
		color = NormalizeColor(topicColor)
	}
	if color == "" {
		return Reading{}, p, errNoColor
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
