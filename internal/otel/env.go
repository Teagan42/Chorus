package otel

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ProtocolProtobuf is the one protocol chorusd speaks: OTLP over HTTP as
// protobuf, every collector's default. gRPC's client would be the module's
// largest dependency for a port collectors also serve over HTTP, and OTLP's
// JSON is not protobuf's JSON mapping (ADR-0101).
const ProtocolProtobuf = "http/protobuf"

// WordsEnv puts what was said on the spans: "on", or "off" and unset.
const WordsEnv = "CHORUS_OTEL_WORDS"

// DefaultTimeout is OTLP's own default for one export.
const DefaultTimeout = 10 * time.Second

// Settings is where spans are exported and what they carry, read from the
// variables every OpenTelemetry SDK reads, so a collector's own setup
// instructions apply as written.
type Settings struct {
	// Endpoint is the full URL spans are posted to. Empty exports nothing.
	Endpoint string
	Headers  map[string]string
	Timeout  time.Duration
	// Resource names the process: service.name first.
	Resource []Attr
	Words    bool
}

// Enabled reports whether there is anywhere to export to.
func (s Settings) Enabled() bool { return s.Endpoint != "" }

// SettingsFromEnv reads the OTEL_* variables for traces, as the
// OpenTelemetry specification defines them, and WordsEnv. With no endpoint,
// export is off; anything set but unusable is an error naming its variable.
func SettingsFromEnv(getenv func(string) string) (Settings, error) {
	s := Settings{Timeout: DefaultTimeout}
	switch v := strings.ToLower(getenv("OTEL_SDK_DISABLED")); v {
	case "", "false":
	case "true":
		return Settings{}, nil
	default:
		return Settings{}, fmt.Errorf("OTEL_SDK_DISABLED=%q is not true or false", v)
	}
	switch v := getenv("OTEL_TRACES_EXPORTER"); v {
	case "", "otlp":
	case "none":
		return Settings{}, nil
	default:
		return Settings{}, fmt.Errorf("OTEL_TRACES_EXPORTER=%q: chorusd exports otlp or none", v)
	}

	endpoint, name := getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"), "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"
	if endpoint == "" {
		// The base endpoint names the collector; traces have their own path.
		name = "OTEL_EXPORTER_OTLP_ENDPOINT"
		if base := getenv(name); base != "" {
			endpoint = strings.TrimSuffix(base, "/") + "/v1/traces"
		}
	}
	if endpoint == "" {
		return Settings{}, nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Settings{}, fmt.Errorf("%s=%q is not an http or https URL", name, endpoint)
	}
	s.Endpoint = endpoint

	if s.Headers, err = headers(getenv, "OTEL_EXPORTER_OTLP_HEADERS", "OTEL_EXPORTER_OTLP_TRACES_HEADERS"); err != nil {
		return Settings{}, err
	}
	for _, k := range []string{"OTEL_EXPORTER_OTLP_PROTOCOL", "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL"} {
		if v := getenv(k); v != "" && v != ProtocolProtobuf {
			return Settings{}, fmt.Errorf("%s=%q: chorusd speaks %s only; a collector serves it on port 4318", k, v, ProtocolProtobuf)
		}
	}
	for _, k := range []string{"OTEL_EXPORTER_OTLP_TIMEOUT", "OTEL_EXPORTER_OTLP_TRACES_TIMEOUT"} {
		if v := getenv(k); v != "" {
			ms, err := strconv.Atoi(v)
			if err != nil || ms <= 0 {
				return Settings{}, fmt.Errorf("%s=%q is not a positive number of milliseconds", k, v)
			}
			s.Timeout = time.Duration(ms) * time.Millisecond
		}
	}

	if s.Resource, err = resource(getenv); err != nil {
		return Settings{}, err
	}
	switch v := getenv(WordsEnv); v {
	case "", "off":
	case "on":
		s.Words = true
	default:
		return Settings{}, fmt.Errorf("%s=%q is not on or off", WordsEnv, v)
	}
	return s, nil
}

// headers reads the general then the traces-only list, the second winning.
func headers(getenv func(string) string, names ...string) (map[string]string, error) {
	out := map[string]string{}
	for _, name := range names {
		pairs, err := pairs(name, getenv(name))
		if err != nil {
			return nil, err
		}
		for _, p := range pairs {
			out[p[0]] = p[1]
		}
	}
	return out, nil
}

// pairs reads the specification's key=value list: comma-separated, each
// value percent-encoded, so a Basic credential's spaces and padding survive.
func pairs(name, list string) ([][2]string, error) {
	var out [][2]string
	for item := range strings.SplitSeq(list, ",") {
		if strings.TrimSpace(item) == "" {
			continue
		}
		k, v, ok := strings.Cut(item, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, fmt.Errorf("%s: %q is not key=value", name, item)
		}
		dec, err := url.PathUnescape(strings.TrimSpace(v))
		if err != nil {
			return nil, fmt.Errorf("%s: %s: %w", name, k, err)
		}
		out = append(out, [2]string{k, dec})
	}
	return out, nil
}

// resource is service.name, chorusd unless OTEL_SERVICE_NAME says, and
// whatever OTEL_RESOURCE_ATTRIBUTES adds.
func resource(getenv func(string) string) ([]Attr, error) {
	extra, err := pairs("OTEL_RESOURCE_ATTRIBUTES", getenv("OTEL_RESOURCE_ATTRIBUTES"))
	if err != nil {
		return nil, err
	}
	service := getenv("OTEL_SERVICE_NAME")
	var attrs []Attr
	for _, p := range extra {
		if p[0] == "service.name" {
			if service == "" {
				service = p[1]
			}
			continue
		}
		attrs = append(attrs, String(p[0], p[1]))
	}
	if service == "" {
		service = "chorusd"
	}
	return append([]Attr{String("service.name", service)}, attrs...), nil
}
