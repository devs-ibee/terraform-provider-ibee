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
			want := int64(20)
			if interval == "MONTHLY" {
				want = 16 * 731
			}
			if *p.estimatedCost() != want || p.BillingCatalog["billing_interval"] != interval || p.BillingCatalog["sku_code"] != "SKU" {
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
func TestComputeBillingTermRejectsMissingAmbiguousAndUnpricedOptions(t *testing.T) {
	for _, scenario := range []string{"missing", "duplicate", "negative", "missing commitment", "wrong period", "missing period", "null period", "monthly no hours", "monthly no months", "hourly committed"} {
		t.Run(scenario, func(t *testing.T) {
			cat := computeTestBillingCatalog("SKU", 20)
			opts := cat["billing_options"].([]any)
			interval := "HOURLY"
			hourly := opts[0].(map[string]any)
			monthly := opts[1].(map[string]any)
			switch scenario {
			case "missing":
				delete(cat, "billing_options")
			case "duplicate":
				cat["billing_options"] = append(opts, opts[0])
			case "negative":
				hourly["unit_price_minor"] = -1
			case "missing commitment":
				delete(hourly, "committed")
			case "wrong period":
				hourly["commitment_period"] = "MONTHLY"
			case "missing period":
				delete(hourly, "commitment_period")
			case "null period":
				hourly["commitment_period"] = nil
			case "monthly no hours":
				interval = "MONTHLY"
				delete(monthly, "committed_hours")
			case "monthly no months":
				interval = "MONTHLY"
				delete(monthly, "commitment_months")
			case "hourly committed":
				hourly["committed"] = true
			}
			p := computePlan{PlanID: "plan", BillingCatalog: cat}
			if p.selectBillingTerm(interval) == nil {
				t.Fatal("invalid billing term accepted")
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
	malformed := resource.ReadResponse{State: next.State}
	r.Read(context.Background(), resource.ReadRequest{State: next.State}, &malformed)
	if !malformed.Diagnostics.HasError() || malformed.State.Raw.IsNull() {
		t.Fatal("unselected catalog silently adopted")
	}
}

func TestComputeBillingTermReadRejectsUnsupportedCommitments(t *testing.T) {
	for _, scenario := range []string{"monthly three months", "monthly six months", "monthly no months", "monthly uncommitted", "monthly missing period", "monthly null period", "hourly committed", "hourly commitment months", "hourly commitment hours", "missing commitment", "wrong period", "empty period", "missing price", "missing period committed", "missing period ambiguous commitment", "missing period with months", "missing period with hours", "missing period wrong price unit", "missing period missing price", "missing period negative price"} {
		t.Run(scenario, func(t *testing.T) {
			interval := "HOURLY"
			if strings.HasPrefix(scenario, "monthly") {
				interval = "MONTHLY"
			}
			term := computeTestSelectedBillingTerm(interval)
			if strings.HasPrefix(scenario, "missing period") {
				delete(term, "commitment_period")
			}
			switch scenario {
			case "monthly three months":
				term["commitment_months"] = 3
			case "monthly six months":
				term["commitment_months"] = 6
			case "monthly no months":
				delete(term, "commitment_months")
			case "monthly uncommitted":
				term["committed"] = false
			case "monthly missing period":
				delete(term, "commitment_period")
			case "monthly null period":
				term["commitment_period"] = nil
			case "hourly committed", "missing period committed":
				term["committed"] = true
			case "hourly commitment months", "missing period with months":
				term["commitment_months"] = 1
			case "hourly commitment hours", "missing period with hours":
				term["committed_hours"] = 731
			case "missing commitment", "missing period ambiguous commitment":
				delete(term, "committed")
			case "wrong period":
				term["commitment_period"] = "YEARLY"
			case "empty period":
				term["commitment_period"] = ""
			case "missing price", "missing period missing price":
				delete(term, "unit_price_minor")
			case "missing period wrong price unit":
				term["price_unit"] = "MONTH"
			case "missing period negative price":
				term["unit_price_minor"] = -1
			}
			r := &cloudVmResource{}
			r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
				v := computeCanonicalVM("cloud")
				v["billing_catalog"] = term
				computeJSON(w, v)
			})
			imported := resource.ImportStateResponse{State: computeState(t, r, nil)}
			r.ImportState(context.Background(), resource.ImportStateRequest{ID: "vm-1"}, &imported)
			computeNoErrors(t, imported.Diagnostics)
			before := imported.State.Raw
			read := resource.ReadResponse{State: imported.State}
			r.Read(context.Background(), resource.ReadRequest{State: imported.State}, &read)
			if !read.Diagnostics.HasError() {
				t.Fatal("unsupported commitment accepted")
			}
			if !read.State.Raw.Equal(before) {
				t.Fatal("unsupported commitment overwrote state")
			}
		})
	}
}
