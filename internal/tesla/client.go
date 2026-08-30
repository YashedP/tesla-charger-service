package tesla

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
)

// ErrVehicleUnavailable is returned when the Tesla API responds with HTTP 408,
// indicating the vehicle is asleep or offline.
var ErrVehicleUnavailable = errors.New("vehicle unavailable")

const (
	defaultAcceptHeader = "application/json"
	defaultUserAgent    = "tesla-charger-service/1.0"
	retryCount          = 3
	retryWaitTime       = 100 * time.Millisecond
	retryMaxWaitTime    = 300 * time.Millisecond
)

type FleetClient struct {
	baseURL string
}

func NewFleetClient(baseURL string) *FleetClient {
	return &FleetClient{baseURL: strings.TrimRight(baseURL, "/")}
}

func (c *FleetClient) GetChargingState(ctx context.Context, httpClient *http.Client, vin string) (string, error) {
	endpoint := fmt.Sprintf("/api/1/vehicles/%s/vehicle_data", url.PathEscape(vin))
	payload := &vehicleDataPayload{}

	resp, err := c.newRequestClient(httpClient).
		R().
		SetContext(ctx).
		SetResult(payload).
		Get(endpoint)
	if err != nil {
		return "", fmt.Errorf("perform tesla request: %w", err)
	}

	if resp.StatusCode() == http.StatusRequestTimeout {
		return "", fmt.Errorf("tesla api status=408: %w", ErrVehicleUnavailable)
	}

	if resp.StatusCode() >= http.StatusMultipleChoices {
		return "", fmt.Errorf(
			"tesla api status=%d",
			resp.StatusCode(),
		)
	}

	if payload.Error != "" {
		return "", errors.New("tesla api error response")
	}

	state := strings.TrimSpace(payload.Response.ChargeState.ChargingState)
	if state == "" {
		return "", fmt.Errorf("missing charge_state.charging_state in response")
	}

	return state, nil
}

func (c *FleetClient) newRequestClient(httpClient *http.Client) *resty.Client {
	client := resty.NewWithClient(httpClient)
	// Retry diagnostics can contain the VIN URL. The checker emits safe events.
	client.SetLogger(discardLogger{})
	client.SetBaseURL(c.baseURL)
	client.SetHeader("Accept", defaultAcceptHeader)
	client.SetHeader("User-Agent", defaultUserAgent)
	client.SetRetryCount(retryCount)
	client.SetRetryWaitTime(retryWaitTime)
	client.SetRetryMaxWaitTime(retryMaxWaitTime)
	client.AddRetryCondition(func(response *resty.Response, err error) bool {
		if err != nil {
			return true
		}
		if response == nil {
			return false
		}
		status := response.StatusCode()
		return status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
	})
	return client
}

func (c *FleetClient) WakeUp(ctx context.Context, httpClient *http.Client, vin string) error {
	endpoint := fmt.Sprintf("/api/1/vehicles/%s/wake_up", url.PathEscape(vin))

	resp, err := c.newRequestClient(httpClient).
		R().
		SetContext(ctx).
		Post(endpoint)
	if err != nil {
		return fmt.Errorf("perform wake_up request: %w", err)
	}

	if resp.StatusCode() >= http.StatusMultipleChoices {
		return fmt.Errorf(
			"wake_up status=%d",
			resp.StatusCode(),
		)
	}

	return nil
}

func (c *FleetClient) GetVehicleState(ctx context.Context, httpClient *http.Client, vin string) (string, error) {
	endpoint := fmt.Sprintf("/api/1/vehicles/%s", url.PathEscape(vin))
	payload := &vehiclePayload{}

	resp, err := c.newRequestClient(httpClient).
		R().
		SetContext(ctx).
		SetResult(payload).
		Get(endpoint)
	if err != nil {
		return "", fmt.Errorf("perform vehicle state request: %w", err)
	}

	if resp.StatusCode() >= http.StatusMultipleChoices {
		return "", fmt.Errorf(
			"vehicle state status=%d",
			resp.StatusCode(),
		)
	}

	state := strings.TrimSpace(payload.Response.State)
	if state == "" {
		return "", fmt.Errorf("missing state in vehicle response")
	}

	return state, nil
}

type vehiclePayload struct {
	Response struct {
		State string `json:"state"`
	} `json:"response"`
}

type vehicleDataPayload struct {
	Response struct {
		ChargeState struct {
			ChargingState string `json:"charging_state"`
		} `json:"charge_state"`
	} `json:"response"`
	Error string `json:"error"`
}

// discardLogger prevents Resty from independently logging sensitive request URLs.
type discardLogger struct{}

func (discardLogger) Errorf(string, ...interface{}) {}
func (discardLogger) Warnf(string, ...interface{})  {}
func (discardLogger) Debugf(string, ...interface{}) {}
