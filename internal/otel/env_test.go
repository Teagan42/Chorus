package otel_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/otel"
)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

// The household's collector is set up from its own instructions: the same
// OTEL_* variables every SDK reads. With none set nothing is exported, and
// the base endpoint gets the traces path while the traces endpoint is used
// as written (SPEC §13).
//
// verifies SPEC §13
func TestTheStandardVariablesSayWhereSpansGo(t *testing.T) {
	for _, c := range []struct {
		name string
		vars map[string]string
		want string
	}{
		{"nothing set", nil, ""},
		{"a collector on the compose network", map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://otel-collector:4318"}, "http://otel-collector:4318/v1/traces"},
		{"a trailing slash", map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://jaeger.lan:4318/"}, "http://jaeger.lan:4318/v1/traces"},
		{"Langfuse's traces endpoint, as written", map[string]string{
			"OTEL_EXPORTER_OTLP_ENDPOINT":        "http://otel-collector:4318",
			"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "https://langfuse.lan/api/public/otel/v1/traces",
		}, "https://langfuse.lan/api/public/otel/v1/traces"},
		{"the SDK switched off", map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://otel-collector:4318", "OTEL_SDK_DISABLED": "true"}, ""},
		{"traces switched off", map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://otel-collector:4318", "OTEL_TRACES_EXPORTER": "none"}, ""},
	} {
		s, err := otel.SettingsFromEnv(env(c.vars))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if s.Endpoint != c.want || s.Enabled() != (c.want != "") {
			t.Errorf("%s: endpoint %q (enabled %v), want %q", c.name, s.Endpoint, s.Enabled(), c.want)
		}
	}
}

// Langfuse wants a Basic credential, which the specification's list
// percent-encodes; the traces-only list overrides the general one. The
// timeout is milliseconds, and the resource is chorusd unless named.
//
// verifies SPEC §13
func TestHeadersTimeoutAndResourceReadAsTheSpecificationWrites(t *testing.T) {
	s, err := otel.SettingsFromEnv(env(map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT":       "https://langfuse.lan/api/public/otel",
		"OTEL_EXPORTER_OTLP_HEADERS":        "Authorization=Basic%20cGstbGYtdGVhZ2FuOnNrLWxmLWhvdXNl,x-household=glenn",
		"OTEL_EXPORTER_OTLP_TRACES_HEADERS": "x-household=glenn-kitchen",
		"OTEL_EXPORTER_OTLP_TRACES_TIMEOUT": "2500",
		"OTEL_RESOURCE_ATTRIBUTES":          "deployment.environment=home,host.name=nuc",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if s.Headers["Authorization"] != "Basic cGstbGYtdGVhZ2FuOnNrLWxmLWhvdXNl" || s.Headers["x-household"] != "glenn-kitchen" {
		t.Errorf("headers %v", s.Headers)
	}
	if s.Timeout != 2500*time.Millisecond {
		t.Errorf("timeout %v, want 2.5s", s.Timeout)
	}
	want := []otel.Attr{
		otel.String("service.name", "chorusd"),
		otel.String("deployment.environment", "home"),
		otel.String("host.name", "nuc"),
	}
	if !slices.Equal(s.Resource, want) {
		t.Errorf("resource %v, want %v", s.Resource, want)
	}
	if s.Words {
		t.Error("words are on with nobody asking")
	}

	named, err := otel.SettingsFromEnv(env(map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT": "http://otel-collector:4318",
		"OTEL_RESOURCE_ATTRIBUTES":    "service.name=chorus-upstairs",
		otel.WordsEnv:                 "on",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if named.Resource[0] != otel.String("service.name", "chorus-upstairs") || !named.Words {
		t.Errorf("resource %v words %v, want the attribute's service name and words on", named.Resource, named.Words)
	}
}

// Anything set but unusable stops startup naming its variable, rather than
// a house whose traces silently go nowhere. gRPC is refused with where to
// point instead.
//
// verifies SPEC §13
func TestAnUnusableSettingNamesItsVariable(t *testing.T) {
	base := map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://otel-collector:4318"}
	for k, v := range map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT":        "otel-collector:4318",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "ftp://otel-collector/v1/traces",
		"OTEL_EXPORTER_OTLP_PROTOCOL":        "grpc",
		"OTEL_EXPORTER_OTLP_TRACES_PROTOCOL": "http/json",
		"OTEL_EXPORTER_OTLP_TIMEOUT":         "10s",
		"OTEL_EXPORTER_OTLP_HEADERS":         "Authorization",
		"OTEL_TRACES_EXPORTER":               "zipkin",
		"OTEL_SDK_DISABLED":                  "yes",
		otel.WordsEnv:                        "please",
	} {
		vars := map[string]string{k: v}
		if !strings.Contains(k, "ENDPOINT") {
			vars["OTEL_EXPORTER_OTLP_ENDPOINT"] = base["OTEL_EXPORTER_OTLP_ENDPOINT"]
		}
		_, err := otel.SettingsFromEnv(env(vars))
		if err == nil || !strings.Contains(err.Error(), k) {
			t.Errorf("%s=%q: %v, want an error naming it", k, v, err)
		}
	}
	_, err := otel.SettingsFromEnv(env(map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://otel-collector:4317", "OTEL_EXPORTER_OTLP_PROTOCOL": "grpc"}))
	if err == nil || !strings.Contains(err.Error(), "4318") {
		t.Errorf("gRPC: %v, want pointed at the HTTP port", err)
	}
}
