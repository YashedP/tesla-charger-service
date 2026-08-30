package tesla

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"tesla-charger-service/internal/crypto"
	"tesla-charger-service/internal/store"
)

func TestRefreshPersistsRotation(t *testing.T) {
	for _, refresh := range []string{"", "rotated-refresh"} {
		t.Run("refresh="+refresh, func(t *testing.T) {
			s := tokenTestStore(t)
			ctx := context.Background()
			if err := s.SaveToken(ctx, &oauth2.Token{AccessToken: "old-access", RefreshToken: "old-refresh", Expiry: time.Now().Add(-time.Hour)}); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"` + refresh + `","token_type":"Bearer","expires_in":3600}`))
			}))
			defer server.Close()
			tokens := NewTokens(s, &oauth2.Config{Endpoint: oauth2.Endpoint{TokenURL: server.URL}})
			if _, err := tokens.Fresh(ctx); err != nil {
				t.Fatal(err)
			}
			got, err := s.LoadToken(ctx)
			if err != nil {
				t.Fatal(err)
			}
			wantRefresh := refresh
			if wantRefresh == "" {
				wantRefresh = "old-refresh"
			}
			if got.AccessToken != "new-access" || got.RefreshToken != wantRefresh || !got.Valid() {
				t.Fatal("refresh not persisted")
			}
		})
	}
}

func TestOAuthReplacementSerializedWithRefresh(t *testing.T) {
	s := tokenTestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.SaveToken(ctx, &oauth2.Token{AccessToken: "old", RefreshToken: "old-refresh", Expiry: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Form.Get("grant_type") == "refresh_token" {
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"refreshed","refresh_token":"refreshed-key","expires_in":3600}`))
		} else {
			_, _ = w.Write([]byte(`{"access_token":"replacement","refresh_token":"replacement-key","expires_in":3600}`))
		}
	}))
	defer server.Close()
	tokens := NewTokens(s, &oauth2.Config{Endpoint: oauth2.Endpoint{TokenURL: server.URL, AuthStyle: oauth2.AuthStyleInParams}})
	refreshDone, exchangeDone := make(chan error, 1), make(chan error, 1)
	go func() { _, err := tokens.Fresh(ctx); refreshDone <- err }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("refresh did not start")
	}
	go func() { exchangeDone <- tokens.Exchange(ctx, "fake-code", "fake-audience") }()
	// A cancelled waiter must not block shutdown behind the refresh.
	canceled, stop := context.WithCancel(ctx)
	stop()
	if _, err := tokens.Fresh(canceled); err == nil {
		t.Fatal("canceled token request succeeded")
	}
	close(release)
	if err := <-refreshDone; err != nil {
		t.Fatal(err)
	}
	if err := <-exchangeDone; err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "replacement" || got.RefreshToken != "replacement-key" {
		t.Fatal("refresh overwrote replacement grant")
	}
}

func tokenTestStore(t *testing.T) *store.SQLiteStore {
	t.Helper()
	cipher, err := crypto.NewAESCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "test.sqlite"), cipher)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
