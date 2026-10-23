package telemetry_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"tesla-charger-service/internal/telemetry"
)

// A real HTTP round trip must preserve trace context and correlate JSON logs
// without exporting an OAuth code, cookie, authorization header, or URL path.
//
//nolint:paralleltest // This test temporarily replaces global OpenTelemetry providers.
func TestHTTPTraceAndLogCorrelation(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	previousProvider := otel.GetTracerProvider()
	previousPropagator := otel.GetTextMapPropagator()

	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		otel.SetTextMapPropagator(previousPropagator)

		_ = provider.Shutdown(context.Background())
	})

	var logs bytes.Buffer

	logger := slog.New(telemetry.Handler{Handler: slog.NewJSONHandler(&logs, nil)}).With("event", "test")

	downstream := httptest.NewServer(telemetry.HTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.InfoContext(r.Context(), "downstream")
		w.WriteHeader(http.StatusNoContent)
	})))
	defer downstream.Close()

	client := &http.Client{Transport: telemetry.Transport{}}

	upstream := httptest.NewServer(telemetry.HTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.InfoContext(r.Context(), "upstream")

		request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, downstream.URL+"/private-path?code=private-code", nil)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusInternalServerError)

			return
		}

		request.Header.Set("Authorization", "Bearer private-token")

		response, err := client.Do(request)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusInternalServerError)

			return
		}
		defer func() { _ = response.Body.Close() }()

		w.WriteHeader(response.StatusCode)
	})))
	defer upstream.Close()

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, upstream.URL+"/callback?code=private-code", nil)
	if err != nil {
		t.Fatal(err)
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", response.StatusCode)
	}

	spans := exporter.GetSpans()
	if len(spans) != 3 {
		t.Fatalf("span count = %d, want two server spans and one client span", len(spans))
	}

	traceID := spans[0].SpanContext.TraceID()
	for _, span := range spans {
		if span.SpanContext.TraceID() != traceID {
			t.Fatal("HTTP propagation split the request across traces")
		}

		encoded, marshalErr := json.Marshal(span)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}

		if strings.Contains(string(encoded), "private-") {
			t.Fatal("trace contains request secrets or raw URL")
		}
	}

	decoder := json.NewDecoder(&logs)

	for range 2 {
		var event map[string]any
		if decodeErr := decoder.Decode(&event); decodeErr != nil {
			t.Fatal(decodeErr)
		}

		if event["trace_id"] != traceID.String() || event["span_id"] == "" {
			t.Fatal("JSON log is missing its active trace context")
		}
	}

	beforeHealth := len(exporter.GetSpans())

	healthRequest, err := http.NewRequestWithContext(t.Context(), http.MethodGet, upstream.URL+"/health", nil)
	if err != nil {
		t.Fatal(err)
	}

	health, err := http.DefaultClient.Do(healthRequest)
	if err != nil {
		t.Fatal(err)
	}

	_, _ = io.Copy(io.Discard, health.Body)
	_ = health.Body.Close()

	// The test application calls downstream even on health; only that outbound
	// call and its downstream server should be traced, not the health request.
	if len(exporter.GetSpans()) != beforeHealth+2 {
		t.Fatal("health request created a server span")
	}
}
