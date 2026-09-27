package provider

import (
	"context"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"net/http"
	"strings"
	"testing"
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
	for _, scenario := range []string{"missing", "duplicate", "negative", "missing commitment", "wrong period", "monthly no hours", "monthly no months", "hourly committed"} {
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
	for _, scenario := range []string{"monthly three months", "monthly six months", "monthly no months", "monthly uncommitted", "hourly committed", "hourly commitment months", "hourly commitment hours", "missing commitment", "wrong period", "missing price"} {
		t.Run(scenario, func(t *testing.T) {
			interval := "HOURLY"
			if strings.HasPrefix(scenario, "monthly") {
				interval = "MONTHLY"
			}
			term := computeTestSelectedBillingTerm(interval)
			switch scenario {
			case "monthly three months":
				term["commitment_months"] = 3
			case "monthly six months":
				term["commitment_months"] = 6
			case "monthly no months":
				delete(term, "commitment_months")
			case "monthly uncommitted":
				term["committed"] = false
			case "hourly committed":
				term["committed"] = true
			case "hourly commitment months":
				term["commitment_months"] = 1
			case "hourly commitment hours":
				term["committed_hours"] = 731
			case "missing commitment":
				delete(term, "committed")
			case "wrong period":
				term["commitment_period"] = "YEARLY"
			case "missing price":
				delete(term, "unit_price_minor")
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
