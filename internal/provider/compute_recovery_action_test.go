package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// actionConfig builds an action config with every attribute null except values.
func actionConfig(t *testing.T, a action.Action, values map[string]attr.Value) tfsdk.Config {
	t.Helper()
	var sch action.SchemaResponse
	a.Schema(context.Background(), action.SchemaRequest{}, &sch)
	ts, all := map[string]attr.Type{}, map[string]attr.Value{}
	for name, attribute := range sch.Schema.Attributes {
		ts[name] = attribute.GetType()
		value, err := networkValue(attribute.GetType(), nil)
		if err != nil {
			t.Fatal(err)
		}
		all[name] = value
	}
	for name, value := range values {
		if _, ok := ts[name]; !ok {
			t.Fatalf("unknown attribute %s", name)
		}
		all[name] = value
	}
	raw, err := types.ObjectValueMust(ts, all).ToTerraformValue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return tfsdk.Config{Schema: sch.Schema, Raw: raw}
}

func invokeAction(t *testing.T, a action.Action, values map[string]attr.Value) (action.InvokeResponse, []string) {
	t.Helper()
	var progress []string
	resp := action.InvokeResponse{SendProgress: func(event action.InvokeProgressEvent) { progress = append(progress, event.Message) }}
	a.Invoke(context.Background(), action.InvokeRequest{Config: actionConfig(t, a, values)}, &resp)
	return resp, progress
}

func decodeBody(t *testing.T, req *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	dec := json.NewDecoder(req.Body)
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestVmRestoreActions(t *testing.T) {
	for _, kind := range []string{"cloud", "gpu"} {
		for _, backup := range []bool{false, true} {
			name := kind + "/snapshot"
			if backup {
				name = kind + "/backup"
			}
			t.Run(name, func(t *testing.T) {
				requestPath, statusPath := "/compute/"+kind+"-vm-snapshots/set-1/actions/restore", "/compute/"+kind+"-vm-snapshots/restores/rs-1"
				if backup {
					requestPath, statusPath = "/compute/"+kind+"-vms/vm-1/backups/actions/restore", "/compute/"+kind+"-vm-backups/restores/rs-1"
				}
				var sent map[string]any
				statuses := []string{"queued", "running", "succeeded"}
				a := &vmRestoreAction{backup: backup}
				a.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
					switch req.Method + " " + req.URL.Path {
					case "POST " + requestPath:
						if !backup && req.URL.Query().Get("vm_id") != "vm-1" {
							t.Error("snapshot restore must name the source VM", req.URL.RawQuery)
						}
						sent = decodeBody(t, req)
						computeJSON(w, map[string]any{"restore_id": "rs-1", "status": "queued"})
					case "GET " + statusPath:
						status := statuses[0]
						statuses = statuses[1:]
						computeJSON(w, map[string]any{"restore_id": "rs-1", "status": status, "target_vm_id": "vm-2"})
					default:
						t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
						w.WriteHeader(500)
					}
				})
				values := map[string]attr.Value{"vm_type": types.StringValue(kind), "vm_id": types.StringValue("vm-1"), "target_mode": types.StringValue("new_vm"), "target_vm_name": types.StringValue("restored"), "target_plan_id": types.StringValue("plan-2"), "target_cpu": types.Int64Value(4), "auto_start": types.BoolValue(false)}
				want := map[string]any{"target_mode": "new_vm", "target_vm_name": "restored", "target_plan_id": "plan-2", "target_cpu": json.Number("4"), "auto_start": false}
				if backup {
					values["recovery_point_id"] = types.StringValue("rp-1")
					want["recovery_point_id"] = "rp-1"
				} else {
					values["snapshot_set_id"] = types.StringValue("set-1")
				}
				resp, progress := invokeAction(t, a, values)
				computeNoErrors(t, resp.Diagnostics)
				if !reflect.DeepEqual(sent, want) {
					t.Fatalf("restore must send only the fields that are set:\n got %v\nwant %v", sent, want)
				}
				if !reflect.DeepEqual(progress, []string{"Waiting for restore rs-1.", "Restore rs-1 succeeded on VM vm-2."}) {
					t.Fatalf("unexpected progress: %v", progress)
				}
			})
		}
	}
}

func TestVmRestoreActionRelaysFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		code         int
		started      map[string]any
		final        map[string]any
		wantInError  string
		wantRequests int
	}{
		"refused":         {409, map[string]any{"error": "conflict", "message": "VM is busy"}, nil, "VM is busy", 1},
		"no restore id":   {200, map[string]any{"status": "queued"}, nil, "no restore_id", 1},
		"failed":          {200, map[string]any{"restore_id": "rs-1", "status": "queued"}, map[string]any{"restore_id": "rs-1", "status": "failed", "error_message": "disk full"}, "failed: disk full", 2},
		"cancelled":       {200, map[string]any{"restore_id": "rs-1", "status": "queued"}, map[string]any{"restore_id": "rs-1", "status": "cancelled"}, "cancelled", 2},
		"status read 403": {200, map[string]any{"restore_id": "rs-1", "status": "queued"}, map[string]any{"error": "insufficient_scope"}, "insufficient_scope", 2},
	} {
		t.Run(name, func(t *testing.T) {
			requests := 0
			a := &vmRestoreAction{backup: true}
			a.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
				requests++
				if req.Method == http.MethodPost {
					w.WriteHeader(tc.code)
					computeJSON(w, tc.started)
					return
				}
				if tc.final["error"] != nil {
					w.WriteHeader(403)
				}
				computeJSON(w, tc.final)
			})
			resp, _ := invokeAction(t, a, map[string]attr.Value{"vm_type": types.StringValue("cloud"), "vm_id": types.StringValue("vm-1"), "target_mode": types.StringValue("replace"), "recovery_point_id": types.StringValue("rp-1")})
			if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics[0].Detail(), tc.wantInError) {
				t.Fatalf("want error containing %q, got %v", tc.wantInError, resp.Diagnostics)
			}
			if requests != tc.wantRequests {
				t.Fatalf("requests = %d, want %d", requests, tc.wantRequests)
			}
		})
	}
}

func TestVmAccessAction(t *testing.T) {
	for _, kind := range []string{"cloud", "gpu"} {
		t.Run(kind, func(t *testing.T) {
			var sent map[string]any
			waited := false
			a := &vmAccessAction{}
			a.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
				switch req.Method + " " + req.URL.Path {
				case "PATCH /compute/" + kind + "-vms/vm-1/actions/access":
					if req.Header.Get("X-Idempotency-Key") != "access-1" {
						t.Error("idempotency key not sent")
					}
					sent = decodeBody(t, req)
					computeJSON(w, operationAccepted{VmID: "vm-1", OperationID: "op-access", Status: "accepted"})
				case "GET /compute/operations/op-access":
					waited = true
					computeJSON(w, map[string]any{"status": "succeeded"})
				default:
					t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
					w.WriteHeader(500)
				}
			})
			resp, progress := invokeAction(t, a, map[string]attr.Value{
				"vm_type": types.StringValue(kind), "vm_id": types.StringValue("vm-1"), "idempotency_key": types.StringValue("access-1"),
				"ssh_key_mode": types.StringValue("add"), "ssh_keys": types.ListValueMust(types.StringType, []attr.Value{types.StringValue("ssh-ed25519 AAAA test")}),
				"new_password": types.StringValue("fixture-password"), "password_auth_enabled": types.BoolValue(false),
			})
			computeNoErrors(t, resp.Diagnostics)
			want := map[string]any{"ssh_key_mode": "add", "ssh_keys": []any{"ssh-ed25519 AAAA test"}, "new_password": "fixture-password", "password_auth_enabled": false}
			if !reflect.DeepEqual(sent, want) {
				t.Fatalf("access body:\n got %v\nwant %v", sent, want)
			}
			if !waited || !reflect.DeepEqual(progress, []string{"Waiting for VM access operation op-access."}) {
				t.Fatalf("did not wait for the operation: %v", progress)
			}
		})
	}
}

func TestVmAccessActionRelaysRefusal(t *testing.T) {
	a := &vmAccessAction{}
	a.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("X-Idempotency-Key") == "" {
			t.Error("a generated idempotency key is required")
		}
		if body := decodeBody(t, req); !reflect.DeepEqual(body, map[string]any{"ssh_key_mode": "remove", "ssh_key_ids": []any{"key-1"}}) {
			t.Errorf("unexpected body %v", body)
		}
		w.WriteHeader(422)
		computeJSON(w, map[string]any{"error": "last_ssh_key", "message": "confirm_remove_last_ssh_key is required"})
	})
	resp, _ := invokeAction(t, a, map[string]attr.Value{"vm_type": types.StringValue("cloud"), "vm_id": types.StringValue("vm-1"), "ssh_key_mode": types.StringValue("remove"), "ssh_key_ids": types.ListValueMust(types.StringType, []attr.Value{types.StringValue("key-1")})})
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics[0].Detail(), "confirm_remove_last_ssh_key is required") {
		t.Fatalf("the API's refusal must be relayed: %v", resp.Diagnostics)
	}
}
