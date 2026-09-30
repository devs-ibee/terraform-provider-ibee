package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func blockVolumeTestPlan() blockVolumeModel {
	return blockVolumeModel{ID: types.StringUnknown(), Name: types.StringValue("data"), SiteID: types.StringValue("site"), SKUCode: types.StringValue("BLOCK-STD"), SizeGb: types.Int64Value(100), VolumeClass: types.StringValue("balanced"), VmType: types.StringValue("cloud"), AllowOnlineResize: types.BoolValue(false), ReplicaCount: types.Int64Unknown(), State: types.StringUnknown(), VolumeName: types.StringUnknown(), BillingCurrency: types.StringUnknown()}
}
func blockVolumeFixture(size int64) map[string]any {
	return map[string]any{"id": "vol-1", "name": "data", "site_id": "site", "size_gb": size, "state": "ready", "volume_name": "internal-volume", "volume_class": "balanced", "volume_kind": "product", "vm_type": "cloud", "replica_count": 2, "attachments": []any{}, "metadata": map[string]any{"billing_catalog": map[string]any{"sku_code": "BLOCK-STD", "currency": "INR"}}}
}
func blockEligibility(w http.ResponseWriter, allowed bool, currency string) {
	computeJSON(w, map[string]any{"allowed": allowed, "organization_id": "org", "reason": "eligible", "billing_mode": "PREPAID", "billing_state": "CURRENT", "currency": currency, "sku_code": "BLOCK-STD", "evaluated_at": "2026-09-27T00:00:00Z"})
}
func TestBlockVolumeLifecycleAndImport(t *testing.T) {
	r := &blockVolumeResource{}
	var volume map[string]any
	admissions, creates, resizes, deletes, polls := 0, 0, 0, 0, 0
	allowed := true
	operationID := "create-op"
	r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/billing/resource-eligibility":
			admissions++
			var b billingEligibilityRequest
			_ = json.NewDecoder(req.Body).Decode(&b)
			if b.SKUCode != "BLOCK-STD" || b.EstimatedCostMinor != nil {
				t.Errorf("billing must use catalog SKU without fabricated estimate: %+v", b)
			}
			blockEligibility(w, allowed, "INR")
		case "/block-storage/volumes":
			creates++
			var b map[string]any
			_ = json.NewDecoder(req.Body).Decode(&b)
			if b["sku_code"] != "BLOCK-STD" || b["idempotency_key"] != req.Header.Get("X-Idempotency-Key") || b["delete_on_termination"] != false {
				t.Errorf("invalid create body: %+v", b)
			}
			if _, ok := b["billing_catalog"]; ok {
				t.Error("provider must not forge server-owned billing fields")
			}
			volume = blockVolumeFixture(100)
			computeJSON(w, map[string]any{"volume": volume, "operation": blockVolumeOperationAPI{ID: operationID, VolumeID: "vol-1", Status: "in-progress"}})
		case "/block-storage/volumes/vol-1/operations":
			polls++
			computeJSON(w, []any{blockVolumeOperationAPI{ID: operationID, VolumeID: "vol-1", Status: "succeeded"}})
		case "/block-storage/volumes/vol-1/resize":
			resizes++
			var b map[string]any
			_ = json.NewDecoder(req.Body).Decode(&b)
			if b["idempotency_key"] != req.Header.Get("X-Idempotency-Key") || b["allow_online"] != false {
				t.Error("invalid resize policy")
			}
			volume["size_gb"] = int64(b["new_size_gb"].(float64))
			operationID = "resize-op"
			computeJSON(w, map[string]any{"volume": volume, "operation": blockVolumeOperationAPI{ID: operationID, VolumeID: "vol-1", Status: "in-progress"}})
		case "/block-storage/volumes/vol-1":
			if volume == nil {
				http.NotFound(w, req)
				return
			}
			if req.Method == http.MethodDelete {
				deletes++
				if req.URL.Query().Get("force") != "false" || req.URL.Query().Get("idempotency_key") != req.Header.Get("X-Idempotency-Key") {
					t.Error("unsafe or non-idempotent delete")
				}
				volume = nil
				computeJSON(w, map[string]any{"status": "deleted", "id": "vol-1"})
				return
			}
			computeJSON(w, volume)
		default:
			t.Errorf("unexpected route %s %s", req.Method, req.URL.Path)
			w.WriteHeader(404)
		}
	})
	created := resource.CreateResponse{State: computeState(t, r, nil)}
	r.Create(context.Background(), resource.CreateRequest{Plan: computePlanState(t, r, blockVolumeTestPlan())}, &created)
	computeNoErrors(t, created.Diagnostics)
	var got blockVolumeModel
	computeNoErrors(t, created.State.Get(context.Background(), &got))
	if got.ID.ValueString() != "vol-1" || got.ReplicaCount.ValueInt64() != 2 || got.BillingCurrency.ValueString() != "INR" || polls != 1 {
		t.Fatal(got)
	}
	imported := resource.ImportStateResponse{State: computeState(t, r, nil)}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "vol-1"}, &imported)
	computeNoErrors(t, imported.Diagnostics)
	read := resource.ReadResponse{State: imported.State}
	r.Read(context.Background(), resource.ReadRequest{State: imported.State}, &read)
	computeNoErrors(t, read.Diagnostics)
	computeNoErrors(t, read.State.Get(context.Background(), &got))
	if got.SKUCode.ValueString() != "BLOCK-STD" || got.AllowOnlineResize.ValueBool() {
		t.Fatal(got)
	}
	if admissions != 0 {
		t.Fatal("import must not check billing")
	}
	plan := got
	plan.SizeGb = types.Int64Value(200)
	updated := resource.UpdateResponse{State: read.State}
	r.Update(context.Background(), resource.UpdateRequest{Plan: computePlanState(t, r, plan), State: read.State}, &updated)
	computeNoErrors(t, updated.Diagnostics)
	computeNoErrors(t, updated.State.Get(context.Background(), &got))
	if got.SizeGb.ValueInt64() != 200 || resizes != 1 || admissions != 0 {
		t.Fatal("resize did not converge")
	}
	volume["attachments"] = []any{map[string]any{"node_name": "node", "mode": "single-writer"}}
	refused := resource.DeleteResponse{}
	r.Delete(context.Background(), resource.DeleteRequest{State: updated.State}, &refused)
	if !refused.Diagnostics.HasError() || deletes != 0 {
		t.Fatal("attached volume deletion must be refused")
	}
	volume["attachments"] = []any{}
	allowed = false
	deleted := resource.DeleteResponse{}
	r.Delete(context.Background(), resource.DeleteRequest{State: updated.State}, &deleted)
	computeNoErrors(t, deleted.Diagnostics)
	if volume != nil || deletes != 1 || creates != 1 || admissions != 0 {
		t.Fatalf("unexpected lifecycle counters: %d %d %d %d", creates, resizes, deletes, admissions)
	}
}
func TestBlockVolumeCreateDenialAndCurrency(t *testing.T) {
	for _, scenario := range []string{"denied", "wrong currency"} {
		t.Run(scenario, func(t *testing.T) {
			r := &blockVolumeResource{}
			r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path == "/billing/resource-eligibility" {
					t.Fatal("automatic write must not query eligibility")
				}
				w.WriteHeader(http.StatusPaymentRequired)
				currency := "INR"
				if scenario == "wrong currency" {
					currency = "USD"
				}
				computeJSON(w, map[string]any{"error": "billing_denied", "billing_reason": scenario, "currency": currency})
			})
			resp := resource.CreateResponse{State: computeState(t, r, nil)}
			r.Create(context.Background(), resource.CreateRequest{Plan: computePlanState(t, r, blockVolumeTestPlan())}, &resp)
			if !resp.Diagnostics.HasError() {
				t.Fatal("unsafe creation accepted")
			}
		})
	}
}
func TestBlockVolumeOperationFailureRetainsID(t *testing.T) {
	r := &blockVolumeResource{}
	r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/billing/resource-eligibility" {
			blockEligibility(w, true, "INR")
			return
		}
		computeJSON(w, map[string]any{"volume": blockVolumeFixture(100), "operation": blockVolumeOperationAPI{ID: "op", VolumeID: "vol-1", Status: "failed", Error: "No capacity"}})
	})
	resp := resource.CreateResponse{State: computeState(t, r, nil)}
	r.Create(context.Background(), resource.CreateRequest{Plan: computePlanState(t, r, blockVolumeTestPlan())}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("failed operation accepted")
	}
	var got blockVolumeModel
	computeNoErrors(t, resp.State.Get(context.Background(), &got))
	if got.ID.ValueString() != "vol-1" || got.ReplicaCount.IsUnknown() || got.State.IsUnknown() || got.VolumeName.IsUnknown() {
		t.Fatal("unrecoverable partial state")
	}
}
func TestBlockVolumeReadMalformedPreservesState(t *testing.T) {
	for _, scenario := range []string{"forbidden", "wrong identity", "missing attachments", "root disk"} {
		t.Run(scenario, func(t *testing.T) {
			r := &blockVolumeResource{}
			r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
				v := blockVolumeFixture(100)
				switch scenario {
				case "forbidden":
					w.WriteHeader(403)
					return
				case "wrong identity":
					v["id"] = "other"
				case "missing attachments":
					delete(v, "attachments")
				case "root disk":
					v["volume_kind"] = "vm-root"
				}
				computeJSON(w, v)
			})
			m := blockVolumeTestPlan()
			m.ID = types.StringValue("vol-1")
			before := computeState(t, r, m)
			resp := resource.ReadResponse{State: before}
			r.Read(context.Background(), resource.ReadRequest{State: before}, &resp)
			if !resp.Diagnostics.HasError() || !resp.State.Raw.Equal(before.Raw) {
				t.Fatal("unsafe response changed state")
			}
		})
	}
}
func TestBlockVolumeResizeErrorPreservesIdentity(t *testing.T) {
	r := &blockVolumeResource{}
	r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/billing/resource-eligibility" {
			blockEligibility(w, true, "INR")
			return
		}
		computeJSON(w, map[string]any{"volume": map[string]any{"id": "wrong"}, "operation": blockVolumeOperationAPI{ID: "op", VolumeID: "wrong", Status: "succeeded"}})
	})
	old := blockVolumeTestPlan()
	old.ID = types.StringValue("vol-1")
	before := computeState(t, r, old)
	plan := old
	plan.SizeGb = types.Int64Value(200)
	resp := resource.UpdateResponse{State: before}
	r.Update(context.Background(), resource.UpdateRequest{Plan: computePlanState(t, r, plan), State: before}, &resp)
	if !resp.Diagnostics.HasError() || !resp.State.Raw.Equal(before.Raw) {
		t.Fatal("mismatched resize response changed state")
	}
}
