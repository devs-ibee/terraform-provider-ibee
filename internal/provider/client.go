package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Client is a minimal IBEE public API client. Every request carries the
// bearer token and the workspace_id query parameter the gateway requires.
type Client struct {
	endpoint    string
	token       string
	workspaceID string
	http        *http.Client
}

func NewClient(endpoint, token, workspaceID string) *Client {
	return &Client{
		endpoint:    endpoint,
		token:       token,
		workspaceID: workspaceID,
		http:        &http.Client{Timeout: 90 * time.Second},
	}
}

// apiError carries the HTTP status and response body for non-2xx replies.
type apiError struct {
	Status int
	Body   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("IBEE API error %d: %s", e.Status, e.Body)
}

func IsNotFound(err error) bool {
	if ae, ok := err.(*apiError); ok {
		return ae.Status == http.StatusNotFound
	}
	return false
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	return c.doH(ctx, method, path, nil, body, out)
}

func (c *Client) doH(ctx context.Context, method, path string, headers map[string]string, body any, out any) error {
	u, err := url.Parse(c.endpoint + path)
	if err != nil {
		return err
	}
	q := u.Query()
	q.Set("workspace_id", c.workspaceID)
	u.RawQuery = q.Encode()

	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, u.String(), rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := string(data)
		if len(msg) > 500 {
			msg = msg[:500]
		}
		return &apiError{Status: resp.StatusCode, Body: msg}
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}
