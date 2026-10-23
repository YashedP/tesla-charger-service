// Package telemetry connects request traces and existing JSON logs without
// recording request URLs, headers, bodies, SQL, or provider error messages.
package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/felixge/httpsnoop"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const shutdownTimeout = 5 * time.Second

// Init enables OTLP/HTTP tracing only when an endpoint is configured.
// Standard OTEL_SERVICE_NAME, OTEL_RESOURCE_ATTRIBUTES, sampling, TLS, and
// exporter environment variables are interpreted by the OpenTelemetry SDK.
func Init(ctx context.Context, serviceName string) (func(), error) {
	if os.Getenv("OTEL_SDK_DISABLED") == "true" ||
		(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "") {
		return func() {}, nil
	}

	if configured := os.Getenv("OTEL_SERVICE_NAME"); configured != "" {
		serviceName = configured
	}

	res, err := resource.New(ctx, resource.WithFromEnv(),
		resource.WithAttributes(attribute.String("service.name", serviceName)))
	if err != nil {
		return nil, fmt.Errorf("configure telemetry resource: %w", err)
	}

	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("configure trace exporter: %w", err)
	}

	provider := sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithBatcher(exporter))
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	return func() {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer cancel()

		if err := provider.Shutdown(shutdownCtx); err != nil {
			slog.Warn("trace export shutdown failed")
		}
	}, nil
}

// HTTP starts a server span before the application's logging middleware.
// httpsnoop preserves optional ResponseWriter interfaces, including streaming.
func HTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)

			return
		}

		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))

		ctx, span := otel.Tracer("http").Start(ctx, r.Method,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(attribute.String("http.request.method", r.Method)))
		defer span.End()

		request := r.WithContext(ctx)
		metrics := httpsnoop.CaptureMetrics(next, w, request)
		span.SetAttributes(attribute.Int("http.response.status_code", metrics.Code))

		if request.Pattern != "" {
			span.SetName(request.Pattern)
			span.SetAttributes(attribute.String("http.route", request.Pattern))
		}

		if metrics.Code >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, "HTTP server error")
		}
	})
}

// Handler enriches context-aware slog calls with the active span identifiers.
// Keep stdout as the single log export path to avoid duplicate ingestion.
type Handler struct {
	slog.Handler
}

func (h Handler) Handle(ctx context.Context, record slog.Record) error {
	spanContext := trace.SpanContextFromContext(ctx)
	if spanContext.IsValid() {
		record = record.Clone()
		record.AddAttrs(slog.String("trace_id", spanContext.TraceID().String()),
			slog.String("span_id", spanContext.SpanID().String()))
	}

	if err := h.Handler.Handle(ctx, record); err != nil {
		return fmt.Errorf("handle trace log: %w", err)
	}

	return nil
}

func (h Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return Handler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h Handler) WithGroup(name string) slog.Handler {
	return Handler{Handler: h.Handler.WithGroup(name)}
}

// Transport traces outbound calls using only method, hostname, and status.
// Clone the request before propagation so caller-owned headers stay untouched.
type Transport struct {
	Base http.RoundTripper
}

func (t Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	ctx, span := otel.Tracer("http").Start(r.Context(), r.Method,
		trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(
			attribute.String("http.request.method", r.Method),
			attribute.String("server.address", r.URL.Hostname())))
	defer span.End()

	request := r.Clone(ctx)
	if request.Header == nil {
		request.Header = make(http.Header)
	}
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(request.Header))

	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}

	response, err := base.RoundTrip(request)
	if err != nil {
		span.SetStatus(codes.Error, "HTTP transport error")

		return nil, fmt.Errorf("traced HTTP request: %w", err)
	}

	span.SetAttributes(attribute.Int("http.response.status_code", response.StatusCode))

	if response.StatusCode >= http.StatusBadRequest {
		span.SetStatus(codes.Error, "HTTP client error")
	}

	return response, nil
}
