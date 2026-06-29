package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"log/slog"

	"tesla-charger-service/internal/tesla"
)

type requestIDContextKey struct{}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(p)
}

func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := newRequestID()
		ctx := context.WithValue(r.Context(), requestIDContextKey{}, requestID)
		r = r.WithContext(ctx)

		s.logger.InfoContext(
			ctx,
			"request_received",
			slog.String("event", "request_received"),
			slog.String("request_id", requestID),
			slog.String("method", r.Method),
			slog.String("route", r.URL.Path),
			slog.String("host", r.Host),
			slog.String("remote_addr", r.RemoteAddr),
			slog.String("request_uri", r.RequestURI),
			slog.String("proto", r.Proto),
			slog.Bool("tls", r.TLS != nil),
			slog.Any("headers", sanitizeHeaders(r.Header)),
		)

		rec := &statusRecorder{ResponseWriter: w}
		start := time.Now()
		next.ServeHTTP(rec, r)

		if r.URL.Path == "/health" {
			return
		}
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}

		s.logger.Info(
			"request_complete",
			slog.String("event", "request_complete"),
			slog.String("request_id", requestID),
			slog.String("method", r.Method),
			slog.String("route", r.URL.Path),
			slog.Int("status", status),
			slog.Int64("duration_ms", time.Since(start).Milliseconds()),
		)
	})
}

func sanitizeHeaders(headers http.Header) map[string][]string {
	sanitized := make(map[string][]string, len(headers))
	for key, values := range headers {
		if isSensitiveHeader(key) {
			sanitized[key] = []string{"REDACTED"}
			continue
		}

		copied := make([]string, len(values))
		copy(copied, values)
		sanitized[key] = copied
	}
	return sanitized
}

func isSensitiveHeader(key string) bool {
	normalized := strings.ToLower(key)
	if normalized == "authorization" || normalized == "cookie" || normalized == "set-cookie" {
		return true
	}

	return strings.Contains(normalized, "token") ||
		strings.Contains(normalized, "secret") ||
		strings.Contains(normalized, "key") ||
		strings.Contains(normalized, "credential")
}

func requestID(r *http.Request) string {
	if r == nil {
		return ""
	}
	id, _ := r.Context().Value(requestIDContextKey{}).(string)
	return id
}

func newRequestID() string {
	id, err := randomState(9)
	if err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return id
}

func (s *Server) log(r *http.Request, level slog.Level, event string, attrs ...slog.Attr) {
	ctx := context.Background()
	args := make([]any, 0, 4+len(attrs))
	args = append(args, slog.String("event", event))
	if id := requestID(r); id != "" {
		args = append(args, slog.String("request_id", id))
	}
	if r != nil {
		ctx = r.Context()
		args = append(args, slog.String("method", r.Method), slog.String("route", r.URL.Path))
	}
	for _, attr := range attrs {
		args = append(args, attr)
	}
	s.logger.Log(ctx, level, event, args...)
}

func (s *Server) wakeObserver(r *http.Request) tesla.WakeObserver {
	return func(event tesla.WakeEvent) {
		attrs := []slog.Attr{}
		if event.Attempt > 0 {
			attrs = append(attrs, slog.Int("attempt", event.Attempt))
		}
		if event.State != "" {
			attrs = append(attrs, slog.String("vehicle_state", event.State))
		}
		if event.Err != nil {
			attrs = append(attrs, slog.String("error", safeError(event.Err)))
		}

		level := slog.LevelInfo
		if event.Err != nil {
			level = slog.LevelWarn
		}
		s.log(r, level, event.Event, attrs...)
	}
}

func safeError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	for _, marker := range []string{" body=", " body:"} {
		if idx := strings.Index(msg, marker); idx >= 0 {
			return strings.TrimSpace(msg[:idx])
		}
	}
	return msg
}
