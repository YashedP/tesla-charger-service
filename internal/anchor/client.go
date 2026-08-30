package anchor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Notification struct {
	IdempotencyKey      string `json:"idempotency_key"`
	Title               string `json:"title"`
	Message             string `json:"message"`
	InitialUrgency      string `json:"initial_urgency"`
	MaximumUrgency      string `json:"maximum_urgency"`
	ResponseRequirement string `json:"response_requirement"`
	MaximumPrompts      int    `json:"maximum_prompts"`
	DeadlineSeconds     int    `json:"deadline_seconds"`
	SkipEnabled         bool   `json:"skip_enabled"`
	SnoozeEnabled       bool   `json:"snooze_enabled"`
}

// SendResult contains only safe diagnostics, never response bodies or URLs.
type SendResult struct {
	Accepted   bool
	Retryable  bool
	RetryAt    time.Time
	StatusCode int
	Reason     string
}

type Client struct {
	endpoint string
	key      string
	http     *http.Client
}

func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return fmt.Errorf("ANCHOR_BASE_URL must be an absolute URL without credentials, query, or fragment")
	}
	if u.Scheme == "https" {
		return nil
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme == "http" && ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("ANCHOR_BASE_URL must use HTTPS (HTTP is allowed only for loopback IPs)")
}

func NewClient(baseURL, key string) (*Client, error) {
	if err := ValidateURL(baseURL); err != nil {
		return nil, err
	}
	if strings.TrimSpace(key) == "" {
		return nil, fmt.Errorf("ANCHOR_API_KEY is required")
	}
	return &Client{
		endpoint: strings.TrimRight(baseURL, "/") + "/v1/notifications",
		key:      key,
		http:     &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

func (c *Client) Send(ctx context.Context, payload []byte) SendResult {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return SendResult{Reason: "invalid_request"}
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return SendResult{Retryable: true, Reason: "transport_error"}
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	result := SendResult{StatusCode: resp.StatusCode}
	switch {
	case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated:
		result.Accepted = true
		result.Reason = "accepted"
	case resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		result.Retryable = true
		result.Reason = "temporary_http_error"
		result.RetryAt = retryAfter(resp.Header.Get("Retry-After"), time.Now())
	default:
		result.Reason = "rejected"
	}
	return result
}

func retryAfter(value string, now time.Time) time.Time {
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 {
		// Any delay beyond our delivery window expires the local run. Cap before
		// converting to Duration to prevent integer overflow from an untrusted header.
		if seconds > 86400 {
			seconds = 86400
		}
		return now.Add(time.Duration(seconds) * time.Second)
	}
	if at, err := http.ParseTime(value); err == nil && at.After(now) {
		return at
	}
	return time.Time{}
}
