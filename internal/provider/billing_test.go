package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBillingAdmissionFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{"allowed", func(d map[string]any) {}, ""},
		{"denied", func(d map[string]any) { d["allowed"] = false; d["reason"] = "initial_topup_required" }, "Add Credits"},
		{"missing-allowed", func(d map[string]any) { delete(d, "allowed") }, "incomplete"},
		{"missing-reason", func(d map[string]any) { delete(d, "reason") }, "incomplete"},
		{"wrong-organization", func(d map[string]any) { d["organization_id"] = "other" }, "organization_id"},
		{"wrong-sku", func(d map[string]any) { d["sku_code"] = "different" }, "requested SKU"},
		{"wrong-estimate", func(d map[string]any) { d["estimated_cost_minor"] = 0 }, "cost estimate"},
		{"timestamp", func(d map[string]any) { d["evaluated_at"] = "yesterday" }, "timestamp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "POST" || r.URL.Path != "/billing/resource-eligibility" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
				}
				var request billingEligibilityRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.SKUCode != "plan-1" || request.EstimatedCostMinor == nil || *request.EstimatedCostMinor != 100 {
					t.Errorf("bad billing request: %+v %v", request, err)
				}
				d := map[string]any{"organization_id": "org-1", "allowed": true, "reason": "eligible", "billing_mode": "PREPAID", "billing_state": "CURRENT", "currency": "INR", "sku_code": "plan-1", "evaluated_at": "2026-09-27T00:00:00Z"}
				tc.mutate(d)
				json.NewEncoder(w).Encode(d)
			}))
			defer s.Close()
			c := NewClient(s.URL, "token", "workspace-1")
			c.organizationID = "org-1"
			cost := int64(100)
			for i := 0; i < 2; i++ {
				err := c.requireBillingEligibility(context.Background(), "plan-1", &cost)
				if tc.want == "" {
					if err != nil {
						t.Fatal(err)
					}
				} else if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("wanted %q, got %v", tc.want, err)
				}
			}
			if calls != 2 {
				t.Fatal("billing result was cached")
			}
		})
	}
}
func TestBillingUnavailableDoesNotAuthorize(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }))
	defer s.Close()
	c := NewClient(s.URL, "token", "workspace")
	if err := c.requireBillingEligibility(context.Background(), "", nil); err == nil || !strings.Contains(err.Error(), "billing.read") {
		t.Fatalf("expected actionable denial: %v", err)
	}
	cost := int64(-1)
	if _, err := c.checkBillingEligibility(context.Background(), billingEligibilityRequest{EstimatedCostMinor: &cost}); err == nil {
		t.Fatal("negative estimate accepted")
	}
}

func TestBillingRejectsCatalogCurrencyMismatch(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"organization_id": "org-1", "allowed": true, "reason": "eligible", "billing_mode": "PREPAID", "billing_state": "CURRENT", "currency": "USD", "sku_code": "plan-1", "estimated_cost_minor": 100, "evaluated_at": "2026-09-27T00:00:00Z"})
	}))
	defer s.Close()
	c := NewClient(s.URL, "token", "workspace")
	cost := int64(100)
	if err := c.requireBillingEligibilityForCurrency(context.Background(), "plan-1", &cost, "INR"); err == nil || !strings.Contains(err.Error(), "currency") {
		t.Fatalf("wrong currency accepted: %v", err)
	}
	if err := c.requireBillingEligibilityForCurrency(context.Background(), "plan-1", &cost, "USD"); err != nil {
		t.Fatal(err)
	}
}
