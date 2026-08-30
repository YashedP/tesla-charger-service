package anchor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSendContractAndFailures(t *testing.T) {
	for _, status := range []int{200, 201, 400, 401, 403, 408, 409, 422, 429, 500, 503, 302} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/v1/notifications" || r.Header.Get("Authorization") != "Bearer fixture-key" || r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("incorrect request contract")
				}
				w.Header().Set("Retry-After", "120")
				w.WriteHeader(status)
				_, _ = w.Write([]byte("secret provider body"))
			}))
			defer server.Close()
			client, err := NewClient(server.URL, "fixture-key")
			if err != nil {
				t.Fatal(err)
			}
			result := client.Send(context.Background(), []byte(`{}`))
			wantRetry := status == 408 || status == 429 || status >= 500
			if result.Accepted != (status == 200 || status == 201) || result.Retryable != wantRetry || result.StatusCode != status {
				t.Fatalf("result=%+v", result)
			}
			if wantRetry && time.Until(result.RetryAt) < 115*time.Second {
				t.Fatal("Retry-After ignored")
			}
			if strings.Contains(result.Reason, "secret") {
				t.Fatal("provider body leaked")
			}
		})
	}
}

func TestRedirectDoesNotForwardCredentials(t *testing.T) {
	var followed atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed.Store(true) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	client, err := NewClient(source.URL, "fixture-key")
	if err != nil {
		t.Fatal(err)
	}
	result := client.Send(context.Background(), []byte(`{}`))
	if followed.Load() || result.Accepted || result.Retryable {
		t.Fatalf("redirect followed: %+v", result)
	}
}

func TestURLValidation(t *testing.T) {
	for _, url := range []string{"http://anchor.example", "http://localhost", "ftp://127.0.0.1", "https://user:pass@anchor.example", "https://anchor.example?token=secret", "https://anchor.example#fragment", "/relative"} {
		if err := ValidateURL(url); err == nil {
			t.Errorf("accepted unsafe URL %s", url)
		}
	}
	for _, url := range []string{"https://anchor.example", "http://127.0.0.1:9000", "http://[::1]:9000"} {
		if err := ValidateURL(url); err != nil {
			t.Errorf("rejected valid URL %s", url)
		}
	}
}

func TestNetworkAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "fixture-key")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	result := client.Send(ctx, []byte(`{}`))
	if !result.Retryable || result.Accepted {
		t.Fatalf("result=%+v", result)
	}
}

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	for _, value := range []string{"120", now.Add(2 * time.Minute).Format(http.TimeFormat)} {
		if got := retryAfter(value, now); !got.Equal(now.Add(2 * time.Minute)) {
			t.Errorf("%s: %s", value, got)
		}
	}
	if !retryAfter("invalid", now).IsZero() || !retryAfter("-1", now).IsZero() {
		t.Fatal("invalid retry delay accepted")
	}
	if !retryAfter("9223372036854775807", now).After(now) {
		t.Fatal("overflowed retry delay")
	}
}
