package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func blockAttachmentPlan() blockVolumeAttachmentModel {
	return blockVolumeAttachmentModel{ID: types.StringUnknown(), VolumeID: types.StringValue("vol-1"), NodeName: types.StringValue("node"), VmID: types.StringValue("vm"), VmType: types.StringUnknown(), Mode: types.StringValue("single-writer"), DevicePath: types.StringUnknown(), ConfirmUnmounted: types.BoolValue(false)}
}
func TestBlockVolumeAttachmentLifecycle(t *testing.T) {
	ctx := context.Background()
	r := &blockVolumeAttachmentResource{}
	v := blockVolumeFixture(100)
	var attaches, detaches, admissions int
	r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/billing/resource-eligibility":
			admissions++
			blockEligibility(w, true, "INR")
		case "/block-storage/volumes/vol-1":
			computeJSON(w, v)
		case "/block-storage/volumes/vol-1/attachments":
			attaches++
			var b map[string]any
			_ = json.NewDecoder(req.Body).Decode(&b)
			if b["node_name"] != "node" || b["vm_id"] != "vm" || b["vm_type"] != "cloud" || b["idempotency_key"] == "" || b["idempotency_key"] != req.Header.Get("X-Idempotency-Key") {
				t.Error("invalid attachment request", b)
			}
			v["attachments"] = []any{map[string]any{"node_name": "node", "vm_id": "vm", "mode": "single-writer", "device_path": "/dev/drbd100"}}
			computeJSON(w, map[string]any{"volume": v, "operation": blockVolumeOperationAPI{ID: "attach-op", VolumeID: "vol-1", Status: "in-progress"}})
		case "/block-storage/volumes/vol-1/operations":
			computeJSON(w, []any{blockVolumeOperationAPI{ID: "attach-op", VolumeID: "vol-1", Status: "succeeded"}})
		case "/block-storage/volumes/vol-1/detach":
			detaches++
			var b map[string]any
			_ = json.NewDecoder(req.Body).Decode(&b)
			if b["force"] != false || b["confirm_unmounted"] != true || b["idempotency_key"] != req.Header.Get("X-Idempotency-Key") {
				t.Error("unsafe detach", b)
			}
			v["attachments"] = []any{}
			computeJSON(w, map[string]any{"volume": v, "operation": blockVolumeOperationAPI{ID: "detach-op", VolumeID: "vol-1", Status: "succeeded"}})
		default:
			t.Errorf("unexpected path %s", req.URL.Path)
			w.WriteHeader(404)
		}
	})
	created := resource.CreateResponse{State: computeState(t, r, nil)}
	r.Create(ctx, resource.CreateRequest{Plan: computePlanState(t, r, blockAttachmentPlan())}, &created)
	computeNoErrors(t, created.Diagnostics)
	var m blockVolumeAttachmentModel
	computeNoErrors(t, created.State.Get(ctx, &m))
	if m.ID.ValueString() != "vol-1/node" || m.DevicePath.ValueString() != "/dev/drbd100" {
		t.Fatal(m)
	}
	duplicate := resource.CreateResponse{State: computeState(t, r, nil)}
	r.Create(ctx, resource.CreateRequest{Plan: computePlanState(t, r, blockAttachmentPlan())}, &duplicate)
	if !duplicate.Diagnostics.HasError() || attaches != 1 {
		t.Fatal("implicitly adopted existing attachment")
	}
	imported := resource.ImportStateResponse{State: computeState(t, r, nil)}
	r.ImportState(ctx, resource.ImportStateRequest{ID: "vol-1/node"}, &imported)
	computeNoErrors(t, imported.Diagnostics)
	read := resource.ReadResponse{State: imported.State}
	r.Read(ctx, resource.ReadRequest{State: imported.State}, &read)
	computeNoErrors(t, read.Diagnostics)
	computeNoErrors(t, read.State.Get(ctx, &m))
	if m.VmID.ValueString() != "vm" || m.Mode.ValueString() != "single-writer" || m.ConfirmUnmounted.ValueBool() {
		t.Fatal(m)
	}
	refused := resource.DeleteResponse{}
	r.Delete(ctx, resource.DeleteRequest{State: read.State}, &refused)
	if !refused.Diagnostics.HasError() || detaches != 0 {
		t.Fatal("unsafe unconfirmed detach")
	}
	m.ConfirmUnmounted = types.BoolValue(true)
	updated := resource.UpdateResponse{State: read.State}
	r.Update(ctx, resource.UpdateRequest{Plan: computePlanState(t, r, m), State: read.State}, &updated)
	computeNoErrors(t, updated.Diagnostics)
	deleted := resource.DeleteResponse{}
	r.Delete(ctx, resource.DeleteRequest{State: updated.State}, &deleted)
	computeNoErrors(t, deleted.Diagnostics)
	if attaches != 1 || detaches != 1 || admissions != 1 {
		t.Fatal("invalid counters", attaches, detaches, admissions)
	}
	gone := resource.ReadResponse{State: updated.State}
	r.Read(ctx, resource.ReadRequest{State: updated.State}, &gone)
	computeNoErrors(t, gone.Diagnostics)
	if !gone.State.Raw.IsNull() {
		t.Fatal("out-of-band detach must remove resource")
	}
}
func TestBlockVolumeAttachmentMalformedAndWrongOwnership(t *testing.T) {
	for _, scenario := range []string{"missing attachments", "missing device", "wrong identity", "wrong root kind", "forbidden", "changed owner"} {
		t.Run(scenario, func(t *testing.T) {
			r := &blockVolumeAttachmentResource{}
			m := blockAttachmentPlan()
			m.ID = types.StringValue("vol-1/node")
			m.VmType = types.StringValue("cloud")
			m.DevicePath = types.StringValue("/dev/drbd100")
			m.ConfirmUnmounted = types.BoolValue(true)
			r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
				if req.Method != "GET" {
					t.Fatal("unsafe mutation", req.Method)
				}
				v := blockVolumeFixture(100)
				a := map[string]any{"node_name": "node", "vm_id": "vm", "mode": "single-writer", "device_path": "/dev/drbd100"}
				v["attachments"] = []any{a}
				switch scenario {
				case "missing attachments":
					delete(v, "attachments")
				case "missing device":
					delete(a, "device_path")
				case "wrong identity":
					v["id"] = "another-volume"
				case "wrong root kind":
					v["volume_kind"] = "vm-root"
				case "forbidden":
					w.WriteHeader(403)
					return
				case "changed owner":
					a["vm_id"] = "different-vm"
				}
				computeJSON(w, v)
			})
			state := computeState(t, r, m)
			if scenario == "changed owner" {
				resp := resource.DeleteResponse{}
				r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
				if !resp.Diagnostics.HasError() {
					t.Fatal("detached changed owner")
				}
				return
			}
			resp := resource.ReadResponse{State: state}
			r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
			if !resp.Diagnostics.HasError() || resp.State.Raw.IsNull() {
				t.Fatal("malformed read erased state")
			}
		})
	}
}
func TestBlockVolumeAttachmentFailureRetainsIdentity(t *testing.T) {
	r := &blockVolumeAttachmentResource{}
	r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/billing/resource-eligibility" {
			blockEligibility(w, true, "INR")
			return
		}
		v := blockVolumeFixture(100)
		if req.Method == "GET" {
			computeJSON(w, v)
			return
		}
		computeJSON(w, map[string]any{"volume": v, "operation": blockVolumeOperationAPI{ID: "op", VolumeID: "vol-1", Status: "failed", Error: "attachment failed"}})
	})
	resp := resource.CreateResponse{State: computeState(t, r, nil)}
	r.Create(context.Background(), resource.CreateRequest{Plan: computePlanState(t, r, blockAttachmentPlan())}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("failed action accepted")
	}
	var m blockVolumeAttachmentModel
	computeNoErrors(t, resp.State.Get(context.Background(), &m))
	if m.ID.ValueString() != "vol-1/node" || m.DevicePath.IsUnknown() || m.VmType.IsUnknown() {
		t.Fatal("unrecoverable partial state", m)
	}
}
