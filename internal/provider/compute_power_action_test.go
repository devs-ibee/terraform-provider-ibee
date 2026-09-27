package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func powerActionConfig(t *testing.T, a *vmPowerAction, vmType, operation string) tfsdk.Config {
	var sch action.SchemaResponse
	a.Schema(context.Background(), action.SchemaRequest{}, &sch)
	ts := map[string]attr.Type{}
	for k, v := range sch.Schema.Attributes {
		ts[k] = v.GetType()
	}
	values := map[string]attr.Value{"vm_id": types.StringValue("vm"), "vm_type": types.StringValue(vmType), "operation": types.StringValue(operation), "idempotency_key": types.StringValue("test-request")}
	raw, err := types.ObjectValueMust(ts, values).ToTerraformValue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return tfsdk.Config{Schema: sch.Schema, Raw: raw}
}
func TestVmPowerAction(t *testing.T) {
	for _, vmType := range []string{"cloud", "gpu"} {
		for _, operation := range []string{"start", "stop", "reboot"} {
			t.Run(vmType+"/"+operation, func(t *testing.T) {
				a := &vmPowerAction{}
				calls, admissions, reads := 0, 0, 0
				status := "stopped"
				wanted := "running"
				if operation == "stop" {
					status = "running"
					wanted = "stopped"
				}
				a.client = computeTestClient(t, func(w http.ResponseWriter, r *http.Request) {
					endpoint := "/compute/" + vmType + "-vms/vm"
					switch r.URL.Path {
					case endpoint:
						reads++
						computeJSON(w, map[string]any{"id": "vm", "status": status})
					case "/billing/resource-eligibility":
						admissions++
						blockEligibility(w, true, "INR")
					case endpoint + "/actions/" + operation:
						calls++
						var b map[string]any
						_ = json.NewDecoder(r.Body).Decode(&b)
						if b["force"] != false || r.Header.Get("X-Idempotency-Key") != "test-request" {
							t.Error("unsafe power action")
						}
						computeJSON(w, operationAccepted{VmID: "vm", OperationID: "op", Status: "accepted"})
					case "/compute/operations/op":
						status = wanted
						computeJSON(w, map[string]any{"status": "succeeded"})
					default:
						t.Errorf("unexpected route %s", r.URL.Path)
						w.WriteHeader(404)
					}
				})
				var resp action.InvokeResponse
				a.Invoke(context.Background(), action.InvokeRequest{Config: powerActionConfig(t, a, vmType, operation)}, &resp)
				computeNoErrors(t, resp.Diagnostics)
				wantAdmission := 1
				if operation == "stop" {
					wantAdmission = 0
				}
				if calls != 1 || admissions != wantAdmission || reads != 2 {
					t.Fatal("invalid lifecycle", calls, admissions, reads)
				}
			})
		}
	}
}
func TestVmPowerActionRefusesUnsafeResponses(t *testing.T) {
	for _, scenario := range []string{"invalid operation", "wrong VM", "denied", "wrong accepted VM", "missing operation", "failed operation", "wrong canonical VM", "missing state"} {
		t.Run(scenario, func(t *testing.T) {
			a := &vmPowerAction{}
			mutated := false
			operation := "start"
			if scenario == "invalid operation" {
				operation = "destroy"
			}
			a.client = computeTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if scenario == "invalid operation" {
					t.Error("invalid action made request")
				}
				switch r.URL.Path {
				case "/compute/cloud-vms/vm":
					id, status := "vm", "running"
					if scenario == "wrong VM" || (scenario == "wrong canonical VM" && mutated) {
						id = "other"
					}
					if scenario == "missing state" {
						status = ""
					}
					computeJSON(w, map[string]any{"id": id, "status": status})
				case "/billing/resource-eligibility":
					blockEligibility(w, scenario != "denied", "INR")
				case "/compute/cloud-vms/vm/actions/start":
					mutated = true
					op := operationAccepted{VmID: "vm", OperationID: "op"}
					if scenario == "wrong accepted VM" {
						op.VmID = "another"
					}
					if scenario == "missing operation" {
						op.OperationID = ""
					}
					computeJSON(w, op)
				case "/compute/operations/op":
					status := "succeeded"
					if scenario == "failed operation" {
						status = "failed"
					}
					computeJSON(w, map[string]any{"status": status, "error_message": "failure"})
				default:
					t.Errorf("unexpected route %s", r.URL.Path)
					w.WriteHeader(404)
				}
			})
			var resp action.InvokeResponse
			a.Invoke(context.Background(), action.InvokeRequest{Config: powerActionConfig(t, a, "cloud", operation)}, &resp)
			if !resp.Diagnostics.HasError() {
				t.Fatal("unsafe response accepted")
			}
			if (scenario == "wrong VM" || scenario == "denied" || scenario == "missing state") && mutated {
				t.Fatal("mutated despite preflight failure")
			}
		})
	}
}
