package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client uses public API authentication; trusted tenant headers belong to the gateway.
type Client struct {
	endpoint         string
	token            string
	workspaceID      string
	organizationID   string
	userAgent        string
	http             *http.Client
	operationTimeout time.Duration
	pollInterval     time.Duration
	retryDelay       time.Duration
}

func NewClient(endpoint, token, workspaceID string) *Client {
	return &Client{
		endpoint: strings.TrimRight(endpoint, "/"), token: token, workspaceID: workspaceID,
		userAgent:        "terraform-provider-ibee/dev",
		http:             &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		operationTimeout: 20 * time.Minute, pollInterval: 2 * time.Second, retryDelay: 250 * time.Millisecond,
	}
}

type apiError struct {
	Status             int
	Body               string
	Code               string
	Message            string
	Reason             string
	RequiredScope      string
	BillingSKUCode     string
	AdmissionContextID string
	RequestID          string
	billingReason      bool
	hasDetails         bool
}

// Error relays the server's answer: status, code, message and request id.
// The raw body is shown when there is no message or it carries more detail.
func (e *apiError) Error() string {
	message := fmt.Sprintf("IBEE API returned HTTP %d", e.Status)
	if e.Code != "" {
		message += " " + e.Code
	}
	switch {
	case e.Message != "" && e.hasDetails:
		message += ": " + e.Message + " Response: " + e.Body
	case e.Message != "":
		message += ": " + e.Message
	case e.Body != "":
		message += ": " + e.Body
	}
	if e.RequestID != "" {
		message += " (request " + e.RequestID + ")"
	}
	return message
}
func IsNotFound(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

// retryable allows safe reads to retry on 429, 502, 503 and 504. Writes,
// including keyed creates, retry only on 429 or 503 without a billing or
// admission answer: after a 502 or 504 a write may already have been applied.
func retryable(method string, e *apiError) bool {
	read := method == http.MethodGet || method == http.MethodHead
	switch e.Status {
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return read || !e.billingAnswer()
	case http.StatusBadGateway, http.StatusGatewayTimeout:
		return read
	}
	return false
}

// requestObserver is set only by tests: the contract route check records the
// method and path of every request the provider sends.
var requestObserver func(method, path string)

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	return c.doH(ctx, method, path, nil, body, out)
}
func (c *Client) doH(ctx context.Context, method, requestPath string, headers map[string]string, body any, out any) error {
	if !strings.HasPrefix(requestPath, "/") || strings.HasPrefix(requestPath, "//") {
		return fmt.Errorf("API path must be relative to the configured endpoint")
	}
	rel, err := url.Parse(requestPath)
	if err != nil || rel.IsAbs() || rel.Host != "" || rel.Fragment != "" {
		return fmt.Errorf("invalid API path")
	}
	for _, segment := range strings.Split(rel.Path, "/") {
		if segment == "." || segment == ".." {
			return fmt.Errorf("invalid API path segment")
		}
	}
	if requestObserver != nil {
		requestObserver(method, rel.EscapedPath())
	}
	u, err := url.Parse(c.endpoint + requestPath)
	if err != nil {
		return fmt.Errorf("invalid IBEE endpoint")
	}
	q := u.Query()
	q.Set("workspace_id", c.workspaceID)
	u.RawQuery = q.Encode()
	var payload []byte
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode API request: %w", err)
		}
	}
	var data []byte
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("build API request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", c.userAgent)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		res, err := c.http.Do(req)
		// A transport failure may occur after a mutation committed. Do not blindly replay it.
		if err != nil {
			return fmt.Errorf("IBEE request failed: %w", err)
		}
		const maxResponse = 16 << 20
		var readErr error
		data, readErr = io.ReadAll(io.LimitReader(res.Body, maxResponse+1))
		res.Body.Close()
		if readErr != nil {
			return fmt.Errorf("read API response: %w", readErr)
		}
		if len(data) > maxResponse {
			return fmt.Errorf("IBEE API response exceeds 16 MiB")
		}
		if res.StatusCode >= 200 && res.StatusCode <= 299 {
			break
		}
		apiErr := parseAPIError(res.StatusCode, data, res.Header.Get("X-Request-Id"), c.token)
		if attempt < 3 && retryable(method, apiErr) {
			delay := c.retryDelay * time.Duration(1<<attempt)
			if value := res.Header.Get("Retry-After"); value != "" {
				if seconds, e := strconv.Atoi(value); e == nil && seconds >= 0 {
					if seconds > 30 {
						seconds = 30
					}
					delay = time.Duration(seconds) * time.Second
				} else if date, e := http.ParseTime(value); e == nil {
					delay = time.Until(date)
				}
			}
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
			if delay < 0 {
				delay = 0
			}
			if err := waitContext(ctx, delay); err != nil {
				return err
			}
			continue
		}
		return apiErr
	}
	if out != nil {
		if len(bytes.TrimSpace(data)) == 0 {
			return fmt.Errorf("IBEE API returned an empty response where JSON was required")
		}
		if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
			return fmt.Errorf("IBEE API returned null where an object or array was required")
		}
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode IBEE API response: %w", err)
		}
	}
	return nil
}
func waitContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
