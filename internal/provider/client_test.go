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

// Reads retry on 429, 502, 503 and 504; writes only on a 429 that carries no
// billing or admission answer. A write that failed with 502, 503 or 504 may
// already have been applied (a replayed create could leave a second VM or use
// up a registry name), so it is never replayed, keyed or not. The identity
// access read mints a credential on every call and is never retried.
func TestClientRetriesOnlySafeRequests(t *testing.T) {
	const access = "/secret-store/identities/id-1/access"
	for _, tc := range []struct {
		name, method, path string
		keyed              bool
		status             int
		body               string
		want               int
	}{
		{"read 429", "GET", "/things", false, 429, `{"detail":"slow down"}`, 4},
		{"read 502", "GET", "/things", false, 502, `{"detail":"bad gateway"}`, 4},
		{"read 503", "GET", "/things", false, 503, `{"detail":"unavailable"}`, 4},
		{"read 504", "GET", "/things", false, 504, `{"detail":"timeout"}`, 4},
		{"identity read 503", "GET", "/secret-store/identities/id-1", false, 503, `{"detail":"unavailable"}`, 4},
		{"identity access read 429", "GET", access, false, 429, `{"detail":"slow down"}`, 1},
		{"identity access read 503", "GET", access, false, 503, `{"detail":"unavailable"}`, 1},
		{"identity access read 503 versioned", "GET", "/v1" + access, false, 503, `{"detail":"unavailable"}`, 1},
		{"identity access read 503 trailing slash", "GET", access + "/", false, 503, `{"detail":"unavailable"}`, 1},
		{"write 429", "POST", "/things", false, 429, `{"detail":"slow down"}`, 1},
		{"keyed write 429", "POST", "/things", true, 429, `{"detail":"slow down"}`, 1},
		{"patch 429", "PATCH", "/things/1", false, 429, `{"detail":"slow down"}`, 1},
		{"write 502", "POST", "/things", false, 502, `{"detail":"bad gateway"}`, 1},
		{"write 503", "POST", "/things", false, 503, `{"detail":"unavailable"}`, 1},
		{"write 504", "POST", "/things", false, 504, `{"detail":"timeout"}`, 1},
		{"keyed create 502", "POST", "/compute/cloud-vms", true, 502, `{"detail":"bad gateway"}`, 1},
		{"keyed create 503", "POST", "/compute/cloud-vms", true, 503, `{"detail":"unavailable"}`, 1},
		{"keyed create 504", "POST", "/compute/cloud-vms", true, 504, `{"detail":"timeout"}`, 1},
		{"keyed registry create 503", "POST", "/container-registry/registries", true, 503, `{"detail":"unavailable"}`, 1},
		{"keyed delete 503", "DELETE", "/compute/cloud-vms/vm-1", true, 503, `{"detail":"unavailable"}`, 1},
		{"keyed create 503 admission", "POST", "/compute/cloud-vms", true, 503, `{"error":"billing_admission_timeout","admission_context_id":"ctx-1"}`, 1},
		{"write 429 billing", "POST", "/things", false, 429, `{"error":"rate_limited","billing_reason":"credit_check"}`, 1},
		{"keyed write 429 admission", "POST", "/compute/cloud-vms/vm-1/actions/start", true, 429, `{"error":"billing_admission_rate_limited","admission_context_id":"ctx-1"}`, 1},
		{"write 409", "POST", "/things", true, 409, `{"detail":"conflict"}`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer s.Close()
			c := NewClient(s.URL, "token", "workspace")
			c.retryDelay = 0
			headers := map[string]string{}
			if tc.keyed {
				headers["X-Idempotency-Key"] = "one-operation"
			}
			err := c.doH(context.Background(), tc.method, tc.path, headers, nil, nil)
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
		if r.Method == http.MethodPost {
			w.WriteHeader(400)
		} else {
			w.WriteHeader(503)
		}
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
