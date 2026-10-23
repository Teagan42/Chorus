package otel

import (
	coltracepb "github.com/teagan42/chorus/internal/pb/opentelemetry/proto/collector/trace/v1"
	commonpb "github.com/teagan42/chorus/internal/pb/opentelemetry/proto/common/v1"
	resourcepb "github.com/teagan42/chorus/internal/pb/opentelemetry/proto/resource/v1"
	tracepb "github.com/teagan42/chorus/internal/pb/opentelemetry/proto/trace/v1"
)

// scope is the instrumentation scope every span is exported under: the
// reader that derived it from the journal.
const scope = "github.com/teagan42/chorus/internal/otel"

// Request is one OTLP export of spans, under the resource that produced
// them and the scope at version.
func Request(resource []Attr, version string, spans []Span) *coltracepb.ExportTraceServiceRequest {
	out := make([]*tracepb.Span, 0, len(spans))
	for _, s := range spans {
		out = append(out, encodeSpan(s))
	}
	return &coltracepb.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
		Resource: &resourcepb.Resource{Attributes: encodeAttrs(resource)},
		ScopeSpans: []*tracepb.ScopeSpans{{
			Scope: &commonpb.InstrumentationScope{Name: scope, Version: version},
			Spans: out,
		}},
	}}}
}

func encodeSpan(s Span) *tracepb.Span {
	p := &tracepb.Span{
		TraceId:           s.Trace[:],
		SpanId:            s.ID[:],
		Name:              s.Name,
		Kind:              tracepb.Span_SpanKind(s.Kind),
		StartTimeUnixNano: uint64(s.Start.UnixNano()),
		EndTimeUnixNano:   uint64(s.End.UnixNano()),
		Attributes:        encodeAttrs(s.Attrs),
	}
	if !s.Parent.IsZero() {
		p.ParentSpanId = s.Parent[:]
	}
	for _, e := range s.Events {
		p.Events = append(p.Events, &tracepb.Span_Event{
			TimeUnixNano: uint64(e.At.UnixNano()),
			Name:         e.Name,
			Attributes:   encodeAttrs(e.Attrs),
		})
	}
	if s.Status.Failed {
		p.Status = &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR, Message: s.Status.Message}
	}
	return p
}

func encodeAttrs(attrs []Attr) []*commonpb.KeyValue {
	out := make([]*commonpb.KeyValue, 0, len(attrs))
	for _, a := range attrs {
		out = append(out, &commonpb.KeyValue{Key: a.Key, Value: encodeValue(a.Value)})
	}
	return out
}

func encodeValue(v any) *commonpb.AnyValue {
	switch v := v.(type) {
	case string:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}
	case int64:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: v}}
	case float64:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: v}}
	case bool:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: v}}
	case []string:
		vals := make([]*commonpb.AnyValue, 0, len(v))
		for _, s := range v {
			vals = append(vals, encodeValue(s))
		}
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_ArrayValue{ArrayValue: &commonpb.ArrayValue{Values: vals}}}
	}
	return &commonpb.AnyValue{}
}
