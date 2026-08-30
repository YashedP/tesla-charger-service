package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"golang.org/x/oauth2"
	"tesla-charger-service/internal/config"
	"tesla-charger-service/internal/paths"
)

type testExchanger struct {
	err    error
	called bool
}

func (e *testExchanger) Exchange(context.Context, string, string) error {
	e.called = true
	return e.err
}

func TestRemovedEndpointAndRetainedRoutes(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll("secrets", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.FleetECPublicKeyPath, []byte("fixture-public-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &oauth2.Config{Endpoint: oauth2.Endpoint{AuthURL: "https://example.test/oauth"}}
	router := NewRouter(config.Config{}, cfg, &testExchanger{}, nil)
	for _, tc := range []struct {
		path   string
		status int
	}{
		{"/v1/is-charging", 404}, {"/health", 200}, {"/oauth/start", 302}, {"/oauth/callback", 400}, {"/docs/", 301}, {"/docs/index.html", 200}, {"/.well-known/appspecific/com.tesla.3p.public-key.pem", 200},
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest("GET", tc.path, nil))
		if recorder.Code != tc.status {
			t.Errorf("%s: status=%d", tc.path, recorder.Code)
		}
	}
}

func TestOAuthCallbackAndLogRedaction(t *testing.T) {
	for _, fail := range []bool{false, true} {
		var logs bytes.Buffer
		exchanger := &testExchanger{}
		if fail {
			exchanger.err = &oauth2.RetrieveError{Response: &http.Response{StatusCode: 400}, Body: []byte("private-provider-body"), ErrorCode: "private-provider-code"}
		}
		router := NewRouter(config.Config{}, &oauth2.Config{}, exchanger, slog.New(slog.NewJSONHandler(&logs, nil)))
		req := httptest.NewRequest("GET", "/oauth/callback?state=private-state&code=private-code", nil)
		req.AddCookie(&http.Cookie{Name: oauthStateCookieName, Value: "private-state"})
		req.Header.Set("Authorization", "Bearer private-bearer")
		req.Header.Set("X-Unusual-Header", "private-header")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		want := 200
		if fail {
			want = 500
		}
		if rec.Code != want || !exchanger.called {
			t.Fatalf("callback failed: status=%d", rec.Code)
		}
		if strings.Contains(logs.String(), "private-") || strings.Contains(rec.Body.String(), "private-") {
			t.Fatal("sensitive OAuth material leaked")
		}
		if !strings.Contains(logs.String(), "request_complete") || !strings.Contains(logs.String(), "request_id") {
			t.Fatal("request correlation missing")
		}
		if strings.Contains(rec.Body.String(), "is-charging") {
			t.Fatal("obsolete success message")
		}
	}
}

func TestOAuthRejectsWrongState(t *testing.T) {
	exchanger := &testExchanger{}
	router := NewRouter(config.Config{}, &oauth2.Config{}, exchanger, slog.New(slog.NewTextHandler(io.Discard, nil)))
	req := httptest.NewRequest("GET", "/oauth/callback?state=wrong&code=test", nil)
	req.AddCookie(&http.Cookie{Name: oauthStateCookieName, Value: "expected"})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != 400 || exchanger.called {
		t.Fatal("OAuth state check bypassed")
	}
	if safeError(errors.New("private-error")) != "operation_failed" {
		t.Fatal("unsafe diagnostic")
	}
}
