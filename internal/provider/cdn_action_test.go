package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func cdnActionConfig(t *testing.T, a *cdnAction, values map[string]attr.Value) tfsdk.Config {
	t.Helper()
	var sch action.SchemaResponse
	a.Schema(context.Background(), action.SchemaRequest{}, &sch)
	ts := map[string]attr.Type{}
	all := map[string]attr.Value{}
	for k, v := range sch.Schema.Attributes {
		ts[k] = v.GetType()
		all[k], _ = networkValue(v.GetType(), nil)
	}
	for k, v := range values {
		all[k] = v
	}
	raw, err := types.ObjectValueMust(ts, all).ToTerraformValue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return tfsdk.Config{Schema: sch.Schema, Raw: raw}
}
func TestCDNActionsPurgeValidationAndConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name, mode, response string
		targets              []string
		field                string
		wantError            bool
		wantCalls            int
	}{
		{"all", "all", `{"success":true,"mode":"all"}`, nil, "", false, 1},
		{"paths", "url", `{"success":true,"mode":"url"}`, []string{"/a"}, "paths", false, 1},
		{"wrong-response-mode", "all", `{"success":true,"mode":"url"}`, nil, "", true, 1},
		{"false-success", "all", `{"success":false,"mode":"all"}`, nil, "", true, 1},
		{"missing-success", "all", `{"mode":"all"}`, nil, "", true, 1},
		{"missing-target", "url", `{}`, nil, "", true, 0},
		{"conflict", "all", `{}`, []string{"/a"}, "paths", true, 0},
		{"empty", "url", `{}`, []string{}, "paths", true, 0},
		{"blank", "url", `{}`, []string{" "}, "paths", true, 0},
		{"unknown-mode", "oops", `{}`, nil, "", true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			a := &cdnAction{client: storageTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "POST" || r.URL.Path != "/cdn/distributions/dist/purge" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				if body["mode"] != tc.mode {
					t.Errorf("mode=%v", body)
				}
				fmt.Fprint(w, tc.response)
			})}
			values := map[string]attr.Value{"distribution_id": types.StringValue("dist"), "mode": types.StringValue(tc.mode)}
			if tc.field != "" {
				v := []attr.Value{}
				for _, x := range tc.targets {
					v = append(v, types.StringValue(x))
				}
				values[tc.field] = types.ListValueMust(types.StringType, v)
			}
			var resp action.InvokeResponse
			a.Invoke(context.Background(), action.InvokeRequest{Config: cdnActionConfig(t, a, values)}, &resp)
			if resp.Diagnostics.HasError() != tc.wantError || calls != tc.wantCalls {
				t.Fatalf("calls=%d diagnostics=%v", calls, resp.Diagnostics)
			}
		})
	}
}
func TestCDNVerifyReportsPendingAndChecksIdentity(t *testing.T) {
	for _, tc := range []struct {
		status, domain string
		err, warning   bool
	}{{"active", "cdn.example.com", false, false}, {"pending_tls", "cdn.example.com", false, true}, {"failed", "cdn.example.com", true, false}, {"active", "other.example.com", true, false}, {"", "cdn.example.com", true, false}} {
		a := &cdnAction{verify: true, client: storageTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "POST" || r.URL.Path != "/cdn/distributions/dist/custom-domains/cdn.example.com/verify" {
				t.Errorf("unexpected path %s", r.URL.Path)
			}
			json.NewEncoder(w).Encode(map[string]any{"status": tc.status, "domain": tc.domain})
		})}
		var resp action.InvokeResponse
		a.Invoke(context.Background(), action.InvokeRequest{Config: cdnActionConfig(t, a, map[string]attr.Value{"distribution_id": types.StringValue("dist"), "domain": types.StringValue("cdn.example.com")})}, &resp)
		if resp.Diagnostics.HasError() != tc.err || resp.Diagnostics.WarningsCount() > 0 != tc.warning {
			t.Fatalf("%+v diagnostics=%v", tc, resp.Diagnostics)
		}
	}
}
