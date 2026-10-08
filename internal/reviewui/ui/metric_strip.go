package ui

// Metric is one figure. A value that changed takes the tone of what changed
// (Tone); From renders as "from → value".
type Metric struct {
	Label string
	From  string
	Value string
	Tone  Tone
	Note  string  // small mono line underneath
	Meter float64 // 0–1; draws a bar under the value when > 0
}

// MetricStrip is a row of metrics divided by rules, e.g. latency on Review,
// outcome on Replay, the hand-off on a room change.
type MetricStrip struct {
	Label string
	Items []Metric
}
