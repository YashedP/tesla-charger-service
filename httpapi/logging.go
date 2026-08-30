package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"log/slog"

	"golang.org/x/oauth2"
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

		if r.URL.Path != "/health" {
			s.logger.InfoContext(ctx, "request_received", "event", "request_received",
				"request_id", requestID, "method", r.Method, "route", r.URL.Path)
		}

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

// safeError deliberately excludes provider error strings and response bodies.
func safeError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var oauthErr *oauth2.RetrieveError
	if errors.As(err, &oauthErr) {
		return "oauth_failed"
	}
	return "operation_failed"
}
