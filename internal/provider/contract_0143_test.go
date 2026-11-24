package provider

// Contract 0.14.3: load balancer firewall, proxy_protocol, health-check hostname and
// expected_statuses, the public address from conditions, and shared volume attachment.

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestLoadBalancerFirewallOptionsAndAddress(t *testing.T) {
	spec := map[string]any{
		"name": "web", "region_id": "in-south", "traffic_type": "network",
		"listeners": []any{map[string]any{"name": "tcp-443", "protocol": "TCP", "port": float64(443), "target_group": "app"}},
		"target_groups": []any{map[string]any{
			"name": "app", "protocol": "TCP", "proxy_protocol": "v2",
			"targets":      []any{map[string]any{"kind": "vm", "resource_id": "vm-1", "port": float64(8443)}},
			"health_check": map[string]any{"protocol": "HTTP", "path": "/healthz", "hostname": "app.example.com", "expected_statuses": []any{float64(200), float64(204)}},
		}},
		"firewall": map[string]any{"mode": "restricted", "rules": []any{
			map[string]any{"address_family": "IPv4", "protocol": "TCP", "source": "203.0.113.0/24", "port": float64(443)},
		}},
	}
	var mu sync.Mutex
	var sent map[string]any
	client := computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch req.Method + " " + req.URL.Path {
		case "POST /networking/load-balancers":
			sent = decodeBody(t, req)
			w.WriteHeader(http.StatusAccepted)
			computeJSON(w, map[string]any{"id": "op-1", "lb_id": "lb-1", "status": "queued"})
		case "GET /networking/load-balancers/operations/op-1":
			computeJSON(w, map[string]any{"id": "op-1", "lb_id": "lb-1", "status": "succeeded"})
		case "GET /networking/load-balancers/lb-1":
			// Answer like the live service: every default filled in.
			var applied map[string]any
			raw, _ := json.Marshal(sent)
			_ = json.Unmarshal(raw, &applied)
			group := applied["target_groups"].([]any)[0].(map[string]any)
			check := group["health_check"].(map[string]any)
			check["interval_seconds"], check["timeout_seconds"], check["healthy_threshold"], check["unhealthy_threshold"], check["port"] = 10, 3, 1, 1, nil
			group["algorithm"], group["session_persistence"] = "least_request", nil
			rule := applied["firewall"].(map[string]any)["rules"].([]any)[0].(map[string]any)
			rule["action"] = "allow"
			applied["logging"] = map[string]any{"enabled": false, "dataset_id": nil}
			applied["managed_by"], applied["owner"] = "portal", nil
			computeJSON(w, map[string]any{"id": "lb-1", "name": "web", "region_id": "in-south", "traffic_type": "network", "scope": "regional", "capacity_mode": "fixed", "visibility": "external", "node_count": 1, "managed_by": "portal", "observed_state": "active", "version": 1,
				"conditions": map[string]any{"addresses": []any{"203.0.113.80"}, "configuration_ready": true, "frontend_ready": true, "backend_ready": true, "reason": nil},
				"spec":       applied})
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
		}
	})
	r := &loadBalancerResource{client: client}
	resp := resource.CreateResponse{State: computeState(t, r, nil)}
	r.Create(context.Background(), resource.CreateRequest{Plan: computePlanState(t, r, loadBalancerTestObject(t, r, spec))}, &resp)
	computeNoErrors(t, resp.Diagnostics)

	group := sent["target_groups"].([]any)[0].(map[string]any)
	check := group["health_check"].(map[string]any)
	if group["proxy_protocol"] != "v2" || check["hostname"] != "app.example.com" || len(check["expected_statuses"].([]any)) != 2 {
		t.Fatalf("target group options not sent: %v", group)
	}
	if _, set := check["unhealthy_threshold"]; set {
		t.Fatalf("unhealthy_threshold must be left to the server when not configured: %v", check)
	}
	firewall := sent["firewall"].(map[string]any)
	rule := firewall["rules"].([]any)[0].(map[string]any)
	if firewall["mode"] != "restricted" || rule["source"] != "203.0.113.0/24" || rule["action"] != nil {
		t.Fatalf("firewall sent %v", firewall)
	}

	ctx := context.Background()
	var address, action types.String
	var unhealthy types.Int64
	var statuses []int64
	computeNoErrors(t, resp.State.GetAttribute(ctx, path.Root("address"), &address))
	computeNoErrors(t, resp.State.GetAttribute(ctx, path.Root("firewall").AtName("rules").AtListIndex(0).AtName("action"), &action))
	computeNoErrors(t, resp.State.GetAttribute(ctx, path.Root("target_groups").AtListIndex(0).AtName("health_check").AtName("unhealthy_threshold"), &unhealthy))
	computeNoErrors(t, resp.State.GetAttribute(ctx, path.Root("target_groups").AtListIndex(0).AtName("health_check").AtName("expected_statuses"), &statuses))
	if address.ValueString() != "203.0.113.80" || action.ValueString() != "allow" || unhealthy.ValueInt64() != 1 || len(statuses) != 2 || statuses[1] != 204 {
		t.Fatalf("state: address %s action %s unhealthy %d statuses %v", address, action, unhealthy.ValueInt64(), statuses)
	}
}

func TestLoadBalancerAddressIsNullUntilTheFrontendIsReady(t *testing.T) {
	for _, conditions := range []any{nil, map[string]any{}, map[string]any{"addresses": []any{}}, map[string]any{"addresses": []any{""}}} {
		if got := loadBalancerAddress(map[string]any{"conditions": conditions}); got != nil {
			t.Fatalf("conditions %v gave address %v", conditions, got)
		}
	}
	if got := loadBalancerAddress(map[string]any{"conditions": map[string]any{"addresses": []any{"198.51.100.7", "198.51.100.8"}}}); got != "198.51.100.7" {
		t.Fatalf("got %v", got)
	}
}

func TestVolumeAttachmentSendsSharedAccessConfirmed(t *testing.T) {
	var body map[string]any
	attached := false
	r := &vmVolumeAttachmentResource{vmType: "cloud"}
	r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		switch req.Method + " " + req.URL.Path {
		case "POST /compute/cloud-vms/vm-1/actions/attach-volume":
			body = decodeBody(t, req)
			attached = true
			w.WriteHeader(http.StatusAccepted)
			computeJSON(w, map[string]any{"operation_id": "op-1", "vm_id": "vm-1", "status": "accepted"})
		case "GET /compute/operations/op-1":
			computeJSON(w, map[string]any{"operation_id": "op-1", "status": "succeeded"})
		case "GET /compute/cloud-vms/vm-1":
			volumes := []any{}
			if attached {
				volumes = append(volumes, map[string]any{"volume_id": "vol-1", "mode": "multi-writer"})
			}
			computeJSON(w, map[string]any{"id": "vm-1", "data_volumes": volumes})
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
		}
	})
	plan := vmVolumeAttachmentModel{ID: types.StringUnknown(), VmID: types.StringValue("vm-1"), VolumeID: types.StringValue("vol-1"), Mode: types.StringValue("multi-writer"), SharedAccessConfirmed: types.BoolValue(true), GuestDevice: types.StringUnknown(), MountInstructions: types.StringUnknown(), BillingCatalog: types.StringNull()}
	resp := resource.CreateResponse{State: computeState(t, r, nil)}
	r.Create(context.Background(), resource.CreateRequest{Plan: computePlanState(t, r, plan)}, &resp)
	computeNoErrors(t, resp.Diagnostics)
	if body["mode"] != "multi-writer" || body["shared_access_confirmed"] != true {
		t.Fatalf("attach body %v", body)
	}
	var got vmVolumeAttachmentModel
	computeNoErrors(t, resp.State.Get(context.Background(), &got))
	if !got.SharedAccessConfirmed.ValueBool() || got.Mode.ValueString() != "multi-writer" {
		t.Fatalf("state %+v", got)
	}

	body = nil
	attached = false
	plan.Mode, plan.SharedAccessConfirmed = types.StringValue("single-writer"), types.BoolNull()
	resp = resource.CreateResponse{State: computeState(t, r, nil)}
	r.Create(context.Background(), resource.CreateRequest{Plan: computePlanState(t, r, plan)}, &resp)
	if _, set := body["shared_access_confirmed"]; set {
		t.Fatalf("shared_access_confirmed sent although not configured: %v", body)
	}
}
