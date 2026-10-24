// Package contract holds the broker contract: topic names and versioned
// payloads. Components meet only through these, which is what lets them run in
// one process today and as separate services later.
package contract

import (
	"strings"
)

// DefaultTopicRoot is the first topic level for everything brewery-related.
const DefaultTopicRoot = "brewery"

// Topics builds topic names under a root (default "brewery").
type Topics struct{ Root string }

func (t Topics) root() string {
	if t.Root == "" {
		return DefaultTopicRoot
	}
	return strings.TrimSuffix(t.Root, "/")
}

// TiltFilter matches every Tilt reading: brewery/tilt/v1/+
func (t Topics) TiltFilter() string { return t.root() + "/tilt/v1/+" }

// BatchFilter matches every batch state topic and the index: brewery/batch/v1/+
func (t Topics) BatchFilter() string { return t.root() + "/batch/v1/+" }

// Batch is the retained state topic for one batch.
func (t Topics) Batch(id string) string { return t.root() + "/batch/v1/" + id }

// BatchIndex lists the active batches.
func (t Topics) BatchIndex() string { return t.root() + "/batch/v1/index" }

// LastLevel returns the final topic level (the color or batch id).
func LastLevel(topic string) string {
	if i := strings.LastIndex(topic, "/"); i >= 0 {
		return topic[i+1:]
	}
	return topic
}

// IsBatchIndex reports whether a topic from BatchFilter is the index.
func IsBatchIndex(topic string) bool { return LastLevel(topic) == "index" }

// Trace carries W3C trace context inside a payload. MQTT 3.1.1 has no
// headers, so publishers that can trace put it here and receivers continue it.
type Trace struct {
	Traceparent string `json:"traceparent,omitempty"`
	Tracestate  string `json:"tracestate,omitempty"`
}

// Carrier exposes the fields as a propagation.TextMapCarrier map.
func (t Trace) Carrier() map[string]string {
	return map[string]string{"traceparent": t.Traceparent, "tracestate": t.Tracestate}
}

// TraceFrom builds a Trace from an injected carrier map.
func TraceFrom(m map[string]string) Trace {
	return Trace{Traceparent: m["traceparent"], Tracestate: m["tracestate"]}
}
