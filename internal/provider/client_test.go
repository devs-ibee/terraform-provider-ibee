package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientScopeAndAuthentication(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/things" || r.URL.Query().Get("workspace_id") != "workspace-1" || r.URL.Query().Get("page") != "2" {
			t.Errorf("incorrect scoped URL: %s", r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("User-Agent") == "" {
			t.Error("missing authentication or user agent")
		}
		if r.Header.Get("X-Organization-ID") != "" {
			t.Error("client supplied trusted tenant header")
		}
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer s.Close()
	c := NewClient(s.URL+"/v1", "test-token", "workspace-1")
	var out map[string]bool
	if err := c.do(context.Background(), "GET", "/things?page=2&workspace_id=wrong", nil, &out); err != nil || !out["ok"] {
		t.Fatalf("request failed: %v", err)
	}
}
func TestClientRetriesOnlySafeRequests(t *testing.T) {
	for _, tc := range []struct {
		name, method string
		keyed        bool
		want         int
	}{{"read", "GET", false, 4}, {"mutation", "POST", false, 1}, {"keyed mutation", "POST", true, 4}} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(503)
				fmt.Fprint(w, `{"detail":"unavailable"}`)
			}))
			defer s.Close()
			c := NewClient(s.URL, "token", "workspace")
			c.retryDelay = 0
			headers := map[string]string{}
			if tc.keyed {
				headers["X-Idempotency-Key"] = "one-operation"
			}
			err := c.doH(context.Background(), tc.method, "/things", headers, nil, nil)
			if err == nil || calls != tc.want {
				t.Fatalf("calls=%d want=%d err=%v", calls, tc.want, err)
			}
		})
	}
}
func TestClientRejectsRedirectsAndInvalidPaths(t *testing.T) {
	destinationCalls := 0
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destinationCalls++ }))
	defer destination.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 307) }))
	defer s.Close()
	c := NewClient(s.URL, "token", "workspace")
	if err := c.do(context.Background(), "POST", "/things", map[string]string{"secret": "value"}, nil); err == nil {
		t.Fatal("redirect accepted")
	}
	if destinationCalls != 0 {
		t.Fatal("redirect followed")
	}
	for _, path := range []string{"https://example.com", "//example.com", "/../escape", "/%2e%2e/escape", "/things#fragment"} {
		if c.do(context.Background(), "GET", path, nil, nil) == nil {
			t.Errorf("invalid path accepted: %s", path)
		}
	}
}
func TestClientInvalidJSONAndWrappedNotFound(t *testing.T) {
	for _, body := range []string{"", "null", "{"} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
		var out map[string]any
		err := NewClient(s.URL, "token", "workspace").do(context.Background(), "GET", "/things", nil, &out)
		s.Close()
		if err == nil {
			t.Errorf("accepted invalid JSON %q", body)
		}
	}
	if !IsNotFound(fmt.Errorf("wrapped: %w", &apiError{Status: 404})) || IsNotFound(errors.New("404")) {
		t.Fatal("wrong not-found classification")
	}
}
func TestClientRedactsTokenAndCancelsRetry(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(503)
		fmt.Fprint(w, `{"detail":"test-token"}`)
	}))
	defer s.Close()
	c := NewClient(s.URL, "test-token", "workspace")
	err := c.do(context.Background(), "POST", "/things", nil, nil)
	if err == nil || strings.Contains(err.Error(), "test-token") {
		t.Fatalf("token exposed: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err = c.do(ctx, "GET", "/things", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("not cancelled: %v", err)
	}
}
