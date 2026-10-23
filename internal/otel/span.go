// Package otel derives OpenTelemetry spans from the journal and exports them
// over OTLP/HTTP. The log stays the one record: a span is a reader's view of
// it, as a harvested pair is, so it cannot disagree with what was journalled
// (SPEC §8, ADR-0007, ADR-0104).
package otel

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"time"
)

// TraceID names one conversation's trace.
type TraceID [16]byte

// SpanID names one span within a trace.
type SpanID [8]byte

func (id TraceID) String() string { return hex.EncodeToString(id[:]) }
func (id SpanID) String() string  { return hex.EncodeToString(id[:]) }

// IsZero reports a parent that is no span: the span is a root.
func (id SpanID) IsZero() bool { return id == SpanID{} }

// traceOf is a conversation's trace id. Derived, not random, so a log read
// twice, live or later, yields the same trace (ADR-0104).
func traceOf(conversationID string) TraceID {
	sum := sha256.Sum256([]byte("chorus/trace\x00" + conversationID))
	var id TraceID
	copy(id[:], sum[:])
	return id
}

// spanOf names the span an event opens. role tells apart the spans one
// event opens: an utterance opens its turn and anchors its model ask.
func spanOf(conversationID string, seq uint64, role string) SpanID {
	h := sha256.New()
	h.Write([]byte("chorus/span\x00" + conversationID + "\x00"))
	_ = binary.Write(h, binary.BigEndian, seq)
	h.Write([]byte("\x00" + role))
	var id SpanID
	copy(id[:], h.Sum(nil))
	if id.IsZero() {
		// OTLP reads an all-zero id as no span at all.
		id[7] = 1
	}
	return id
}

// SpanKind is OTLP's span kind. Only the two Chorus uses are named.
type SpanKind int32

const (
	KindInternal SpanKind = 1
	KindClient   SpanKind = 3
)

// Span is one stretch of a conversation the journal recorded.
type Span struct {
	Trace  TraceID
	ID     SpanID
	Parent SpanID
	Name   string
	Kind   SpanKind
	Start  time.Time
	End    time.Time
	Attrs  []Attr
	Events []Event
	Status Status
}

// Attr returns the span's attribute named key, and whether it has one.
func (s Span) Attr(key string) (any, bool) {
	for _, a := range s.Attrs {
		if a.Key == key {
			return a.Value, true
		}
	}
	return nil, false
}

// Event is something that happened at one moment within a span.
type Event struct {
	Name  string
	At    time.Time
	Attrs []Attr
}

// Status is OTLP's span status. Failed is ERROR; otherwise it is UNSET.
type Status struct {
	Failed  bool
	Message string
}

// Attr is one attribute. Value is a string, int64, float64, bool or
// []string, as the constructors below make it.
type Attr struct {
	Key   string
	Value any
}

func String(k, v string) Attr            { return Attr{k, v} }
func Int(k string, v int64) Attr         { return Attr{k, v} }
func Float(k string, v float64) Attr     { return Attr{k, v} }
func Bool(k string, v bool) Attr         { return Attr{k, v} }
func Strings(k string, v ...string) Attr { return Attr{k, v} }
