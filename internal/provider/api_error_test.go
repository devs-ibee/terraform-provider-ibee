package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestAPIErrorEnvelopeParity(t *testing.T) {
	cases := []struct {
		name                      string
		status                    int
		body, code, reason, scope string
	}{
		{"restricted", 403, `{"error":"organization_restricted","billing_reason":"billing_limit_exhausted"}`, "organization_restricted", "billing_limit_exhausted", ""},
		{"suspended", 423, `{"error":"organization_suspended","billing_reason":null}`, "organization_suspended", "", ""},
		{"revoked", 403, `{"error":"key_revoked"}`, "key_revoked", "", ""},
		{"expired", 403, `{"error":"key_expired"}`, "key_expired", "", ""},
		{"workspace", 403, `{"error":"workspace_not_allowed"}`, "workspace_not_allowed", "", ""},
		{"scope", 403, `{"error":"insufficient_scope","required_scope":"billing.read"}`, "insufficient_scope", "", "billing.read"},
		{"nested", 403, `{"error":{"code":"FORBIDDEN","message":"Operation 'READ_RESOURCE' is not allowed while organization is suspended"}}`, "forbidden", "", ""},
		{"billing", 402, `{"error":"billing_denied","billing_reason":"initial_topup_required"}`, "billing_denied", "initial_topup_required", ""},
		{"detail", 403, `{"detail":{"code":"INSUFFICIENT_SCOPE","reason":"permission_denied","required_scope":"vm.edit"}}`, "insufficient_scope", "permission_denied", "vm.edit"},
		{"detail_error", 403, `{"detail":{"error_code":"workspace_not_allowed"}}`, "workspace_not_allowed", "", ""},
		{"top", 403, `{"code":" Key_Disabled ","reason":"token_disabled"}`, "key_disabled", "token_disabled", ""},
		{"precedence", 403, `{"error":"key_revoked","code":"not_found","detail":{"code":"not_found"}}`, "key_revoked", "", ""},
		{"malformed", 403, `{"error":`, "", "", ""},
		{"wrong_type", 403, `{"error":false,"code":42,"reason":[]}`, "", "", ""},
		{"plain", 403, `Forbidden`, "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("X-Request-Id", "request-123")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer s.Close()
			err := NewClient(s.URL, "fixture-token", "workspace").do(context.Background(), http.MethodGet, "/fixture", nil, nil)
			var ae *apiError
			if !errors.As(err, &ae) || calls != 1 || IsNotFound(err) {
				t.Fatalf("denial mishandled: %v, calls=%d", err, calls)
			}
			if ae.Status != tc.status || ae.Code != tc.code || ae.Reason != tc.reason || ae.RequiredScope != tc.scope || ae.RequestID != "request-123" {
				t.Fatalf("incorrect error: %+v", ae)
			}
		})
	}
}

func TestAPIErrorMetadataAndSecretSafeGuidance(t *testing.T) {
	secret := "fixture-secret-never-print"
	data := []byte(`{"error":"organization_restricted","billing_reason":"billing_limit_exhausted","billing_sku_code":"BACKUP-STD","admission_context_id":"ctx-123","message":"` + secret + `"}`)
	ae := parseAPIError(403, data, "request-123", secret)
	if ae.BillingSKUCode != "BACKUP-STD" || ae.AdmissionContextID != "ctx-123" {
		t.Fatalf("metadata missing: %+v", ae)
	}
	if strings.Contains(ae.Error(), secret) || strings.Contains(secretSafeError(ae), secret) || !strings.Contains(secretSafeError(ae), "organization is restricted") {
		t.Fatal("unsafe or unactionable secret diagnostic")
	}
	for _, code := range []string{"organization_suspended", "key_revoked", "key_expired", "insufficient_scope", "workspace_not_allowed", "organization_lifecycle_unavailable", "billing_denied"} {
		e := parseAPIError(403, []byte(`{"error":"`+code+`","billing_reason":"`+secret+`","required_scope":"`+secret+`"}`), secret, secret)
		if e.guidance() == "" || strings.Contains(secretSafeError(e), secret) {
			t.Fatalf("unsafe guidance for %s", code)
		}
	}
	malformed := parseAPIError(403, []byte(`{"error":"key_revoked\nforged","billing_reason":"bad\u001b[31m","required_scope":"wrong scope"}`), "request\nforged", "")
	if malformed.Code != "" || malformed.Reason != "" || malformed.RequiredScope != "" || malformed.RequestID != "" {
		t.Fatal("control/invalid metadata accepted")
	}
	if strings.Contains(secretSafeError(&apiError{Status: 403, Body: secret, Code: secret, Reason: secret, RequestID: secret}), secret) {
		t.Fatal("secret-safe diagnostics used untrusted metadata")
	}
}

func TestLifecycleUnavailableRetryBudget(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			calls := 0
			c := networkTestClient(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(503)
				fmt.Fprint(w, `{"error":"organization_lifecycle_unavailable"}`)
			})
			err := c.do(context.Background(), method, "/fixture", nil, nil)
			var ae *apiError
			want := 1
			if method == http.MethodGet {
				want = 4
			}
			if !errors.As(err, &ae) || ae.Code != "organization_lifecycle_unavailable" || calls != want {
				t.Fatalf("retry budget %d want %d: %v", calls, want, err)
			}
		})
	}
}

func TestLifecycleReadDenialPreservesTerraformState(t *testing.T) {
	for _, status := range []int{401, 403, 423, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r := NewFirewallGroupResource().(*firewallGroupResource)
			r.client = networkTestClient(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":"organization_restricted"}`)
			})
			s := networkTestState(t, networkTestSchema(r), firewallGroupModel{ID: types.StringValue("group-1"), Name: types.StringValue("existing"), Description: types.StringNull(), Status: types.StringValue("active")})
			resp := resource.ReadResponse{State: s}
			r.Read(context.Background(), resource.ReadRequest{State: s}, &resp)
			if !resp.Diagnostics.HasError() || resp.State.Raw.IsNull() || !resp.State.Raw.Equal(s.Raw) {
				t.Fatal("read denial removed or changed managed state")
			}
		})
	}
}
