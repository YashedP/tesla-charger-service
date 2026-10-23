package httpapi

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"tesla-charger-service/internal/config"
)

func TestHealth(t *testing.T) {
	var logs bytes.Buffer
	router := NewRouter(
		config.Config{},
		nil,
		nil,
		nil,
		slog.New(slog.NewJSONHandler(&logs, nil)),
	)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if logs.Len() != 0 {
		t.Fatalf("successful health check logged %d bytes", logs.Len())
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); got != "ok\n" {
		t.Fatalf("body = %q, want %q", got, "ok\n")
	}
}
