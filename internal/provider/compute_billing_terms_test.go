package provider

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestComputeBillingTermSelection(t *testing.T) {
	for _, interval := range []string{"HOURLY", "MONTHLY"} {
		t.Run(interval, func(t *testing.T) {
			p := computePlan{PlanID: "plan", BillingCatalog: computeTestBillingCatalog("SKU", 20)}
			original := p.BillingCatalog
			if err := p.selectBillingTerm(interval); err != nil {
				t.Fatal(err)
			}
			if p.BillingCatalog["billing_interval"] != interval || p.BillingCatalog["sku_code"] != "SKU" {
				t.Fatal(p)
			}
			if _, ok := original["billing_interval"]; ok {
				t.Fatal("selection mutated shared catalog")
			}
			if p.BillingCatalog["committed"] != (interval == "MONTHLY") {
				t.Fatal("wrong commitment")
			}
		})
	}
}
func TestComputeBillingTermPassesOptionThrough(t *testing.T) {
	missing := computeTestBillingCatalog("SKU", 20)
	delete(missing, "billing_options")
	p := computePlan{PlanID: "plan", BillingCatalog: missing}
	if p.selectBillingTerm("HOURLY") == nil {
		t.Fatal("a term with no listed option was accepted")
	}
	// The API, not the provider, decides whether a listed option can be bought.
	for _, scenario := range []string{"negative", "monthly three months", "hourly committed", "missing period"} {
		t.Run(scenario, func(t *testing.T) {
			cat := computeTestBillingCatalog("SKU", 20)
			opts := cat["billing_options"].([]any)
			hourly, monthly := opts[0].(map[string]any), opts[1].(map[string]any)
			interval := "HOURLY"
			switch scenario {
			case "negative":
				hourly["unit_price_minor"] = -1
			case "monthly three months":
				interval = "MONTHLY"
				monthly["commitment_months"] = 3
			case "hourly committed":
				hourly["committed"] = true
			case "missing period":
				delete(hourly, "commitment_period")
			}
			p := computePlan{PlanID: "plan", BillingCatalog: cat}
			if err := p.selectBillingTerm(interval); err != nil {
				t.Fatal(err)
			}
			selected := hourly
			if interval == "MONTHLY" {
				selected = monthly
			}
			for _, k := range computeBillingTermFields {
				if p.BillingCatalog[k] != selected[k] {
					t.Fatalf("%s changed: %v != %v", k, p.BillingCatalog[k], selected[k])
				}
			}
		})
	}
}

// Production VM GET responses omit or null commitment_period for explicitly
// uncommitted hourly purchases. Both variants must refresh and import safely.
func TestComputeHourlyCanonicalReadWithoutCommitmentPeriod(t *testing.T) {
	for _, kind := range []string{"cloud", "gpu"} {
		for _, shape := range []string{"omitted", "null"} {
			t.Run(kind+"/"+shape, func(t *testing.T) {
				term := computeTestSelectedBillingTerm("HOURLY")
				if shape == "omitted" {
					delete(term, "commitment_period")
				} else {
					term["commitment_period"] = nil
				}
				r := &cloudVmResource{vmType: kind}
				r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
					if req.Method != http.MethodGet || req.URL.Path != "/compute/"+kind+"-vms/vm-1" {
						t.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
					}
					v := computeCanonicalVM(kind)
					v["billing_catalog"] = term
					computeJSON(w, v)
				})
				imported := resource.ImportStateResponse{State: computeState(t, r, nil)}
				r.ImportState(context.Background(), resource.ImportStateRequest{ID: "vm-1"}, &imported)
				computeNoErrors(t, imported.Diagnostics)
				read := resource.ReadResponse{State: imported.State}
				r.Read(context.Background(), resource.ReadRequest{State: imported.State}, &read)
				computeNoErrors(t, read.Diagnostics)
				var got cloudVmModel
				computeNoErrors(t, read.State.Get(context.Background(), &got))
				if !got.BillingInterval.Equal(types.StringValue("HOURLY")) || got.Status.ValueString() != "running" {
					t.Fatalf("canonical hourly response not hydrated: %+v", got)
				}
			})
		}
	}
}
func TestComputeBillingTermReadImportAndDrift(t *testing.T) {
	r := &cloudVmResource{}
	interval := "MONTHLY"
	r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		v := computeCanonicalVM("cloud")
		v["billing_catalog"] = computeTestSelectedBillingTerm(interval)
		computeJSON(w, v)
	})
	imported := resource.ImportStateResponse{State: computeState(t, r, nil)}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "vm-1"}, &imported)
	computeNoErrors(t, imported.Diagnostics)
	read := resource.ReadResponse{State: imported.State}
	r.Read(context.Background(), resource.ReadRequest{State: imported.State}, &read)
	computeNoErrors(t, read.Diagnostics)
	var got cloudVmModel
	computeNoErrors(t, read.State.Get(context.Background(), &got))
	if got.BillingInterval.ValueString() != "MONTHLY" {
		t.Fatal("import guessed term")
	}
	interval = "HOURLY"
	next := resource.ReadResponse{State: read.State}
	r.Read(context.Background(), resource.ReadRequest{State: read.State}, &next)
	computeNoErrors(t, next.Diagnostics)
	computeNoErrors(t, next.State.Get(context.Background(), &got))
	if !got.BillingInterval.Equal(types.StringValue("HOURLY")) {
		t.Fatal("billing drift ignored")
	}
	interval = ""
	unreported := resource.ReadResponse{State: next.State}
	r.Read(context.Background(), resource.ReadRequest{State: next.State}, &unreported)
	computeNoErrors(t, unreported.Diagnostics)
	computeNoErrors(t, unreported.State.Get(context.Background(), &got))
	if !got.BillingInterval.Equal(types.StringValue("HOURLY")) {
		t.Fatal("a read without a term must keep the recorded term")
	}
}

// VMs bought in the portal with any commitment (for example three or six
// months) refresh and import with the term the API reports.
func TestComputeBillingTermReadRelaysAnyCommitment(t *testing.T) {
	for _, months := range []int{1, 3, 6} {
		t.Run(strings.Repeat("m", months), func(t *testing.T) {
			term := computeTestSelectedBillingTerm("MONTHLY")
			term["commitment_months"] = months
			r := &cloudVmResource{}
			r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
				v := computeCanonicalVM("cloud")
				v["billing_catalog"] = term
				computeJSON(w, v)
			})
			imported := resource.ImportStateResponse{State: computeState(t, r, nil)}
			r.ImportState(context.Background(), resource.ImportStateRequest{ID: "vm-1"}, &imported)
			computeNoErrors(t, imported.Diagnostics)
			read := resource.ReadResponse{State: imported.State}
			r.Read(context.Background(), resource.ReadRequest{State: imported.State}, &read)
			computeNoErrors(t, read.Diagnostics)
			var got cloudVmModel
			computeNoErrors(t, read.State.Get(context.Background(), &got))
			if got.BillingInterval.ValueString() != "MONTHLY" {
				t.Fatalf("term not relayed: %+v", got)
			}
		})
	}
}
