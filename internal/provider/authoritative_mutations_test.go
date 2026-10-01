package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestMutationSourcesCannotQueryEligibility(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") || file == "billing.go" || file == "billing_data_source.go" {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"checkBillingEligibility(", "requireBillingEligibility", "billing/resource-eligibility", "EstimatedCostMinor"} {
			if strings.Contains(string(data), forbidden) {
				t.Errorf("%s contains automatic billing logic: %s", file, forbidden)
			}
		}
	}
}

func TestExplicitLifecycleEligibilityDenialIsData(t *testing.T) {
	for _, operation := range []string{"REVOKE_CREDENTIAL", "SECURITY_RECOVERY"} {
		t.Run(operation, func(t *testing.T) {
			calls := 0
			d := &billingEligibilityDataSource{client: computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
				calls++
				if req.Method != "POST" || req.URL.Path != "/billing/resource-eligibility" {
					t.Error(req.URL)
				}
				var body billingEligibilityRequest
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body.Operation != operation || body.EstimatedCostMinor != nil {
					t.Fatalf("unexpected query: %+v", body)
				}
				computeEligibility(w, false, "")
			})}
			var schema datasource.SchemaResponse
			d.Schema(context.Background(), datasource.SchemaRequest{}, &schema)
			state := tfsdk.State{Schema: schema.Schema}
			computeNoErrors(t, state.Set(context.Background(), billingEligibilityModel{
				Operation: types.StringValue(operation),
			}))
			response := datasource.ReadResponse{State: state}
			d.Read(context.Background(), datasource.ReadRequest{Config: tfsdk.Config{Schema: schema.Schema, Raw: state.Raw}}, &response)
			computeNoErrors(t, response.Diagnostics)
			var got billingEligibilityModel
			computeNoErrors(t, response.State.Get(context.Background(), &got))
			if got.Allowed.ValueBool() || got.Allowed.IsNull() || calls != 1 {
				t.Fatal("denial did not remain data")
			}
		})
	}
}
