package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func powerActionConfig(t *testing.T, a *vmPowerAction, vmType, operation string, overrides ...map[string]attr.Value) tfsdk.Config {
	t.Helper()
	var sch action.SchemaResponse
	a.Schema(context.Background(), action.SchemaRequest{}, &sch)
	ts := map[string]attr.Type{}
	for k, v := range sch.Schema.Attributes {
		ts[k] = v.GetType()
	}
	values := map[string]attr.Value{"vm_id": types.StringValue("vm"), "vm_type": types.StringValue(vmType), "operation": types.StringValue(operation), "idempotency_key": types.StringValue("test-request")}
	for _, override := range overrides {
		for key, value := range override {
			values[key] = value
		}
	}
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
				// Both preflight and terminal readback accept casing and whitespace.
				status := " \tStOpPeD\n"
				wanted := "running"
				if operation != "start" {
					status = " \tRuNnInG\n"
				}
				if operation == "stop" {
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
						if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
							t.Error(err)
						}
						if r.Method != http.MethodPost || b["force"] != false || b["requested_by"] != "terraform" || r.Header.Get("X-Idempotency-Key") != "test-request" {
							t.Error("unsafe power action")
						}
						computeJSON(w, operationAccepted{VmID: "vm", OperationID: "op", Status: "accepted"})
					case "/compute/operations/op":
						status = " \t" + strings.ToUpper(wanted) + "\n"
						computeJSON(w, map[string]any{"status": "succeeded"})
					default:
						t.Errorf("unexpected route %s", r.URL.Path)
						w.WriteHeader(404)
					}
				})
				var progress []string
				resp := action.InvokeResponse{SendProgress: func(event action.InvokeProgressEvent) {
					progress = append(progress, event.Message)
				}}
				a.Invoke(context.Background(), action.InvokeRequest{Config: powerActionConfig(t, a, vmType, operation)}, &resp)
				computeNoErrors(t, resp.Diagnostics)
				wantAdmission := 0
				if calls != 1 || admissions != wantAdmission || reads != 2 {
					t.Fatal("invalid lifecycle", calls, admissions, reads)
				}
				if !reflect.DeepEqual(progress, []string{"Waiting for VM power operation op.", "VM is " + wanted + "."}) {
					t.Fatalf("unexpected progress: %v", progress)
				}
			})
		}
	}
}
func TestVmPowerActionRefusesUnsafeResponses(t *testing.T) {
	for _, vmType := range []string{"cloud", "gpu"} {
		for _, tc := range []struct {
			name, summary string
			mutated       bool
		}{
			{"invalid operation", "Invalid VM power action", false},
			{"wrong VM", "Invalid VM response", false},
			{"denied", "VM power request failed", true},
			{"wrong accepted VM", "Invalid VM power response", true},
			{"missing operation", "Invalid VM power response", true},
			{"failed operation", "VM power operation did not complete", true},
			{"wrong canonical VM", "Invalid VM response", true},
			{"missing state", "Invalid VM response", false},
			{"blank state", "Invalid VM response", false},
			{"missing canonical state", "Invalid VM response", true},
			{"blank canonical state", "Invalid VM response", true},
			{"unexpected canonical state", "Unexpected VM power state", true},
		} {
			t.Run(vmType+"/"+tc.name, func(t *testing.T) {
				scenario := tc.name
				a := &vmPowerAction{}
				mutated := false
				operation := "start"
				if scenario == "invalid operation" {
					operation = "destroy"
				}
				endpoint := "/compute/" + vmType + "-vms/vm"
				a.client = computeTestClient(t, func(w http.ResponseWriter, r *http.Request) {
					if scenario == "invalid operation" {
						t.Error("invalid action made request")
					}
					switch r.URL.Path {
					case endpoint:
						id, status := "vm", "stopped"
						if mutated {
							status = "running"
						}
						if scenario == "wrong VM" || (scenario == "wrong canonical VM" && mutated) {
							id = "other"
						}
						if scenario == "missing state" || (scenario == "missing canonical state" && mutated) {
							status = ""
						}
						if scenario == "blank state" || (scenario == "blank canonical state" && mutated) {
							status = " \t\n"
						}
						if scenario == "unexpected canonical state" && mutated {
							status = "error"
						}
						computeJSON(w, map[string]any{"id": id, "status": status})
					case "/billing/resource-eligibility":
						blockEligibility(w, scenario != "denied", "INR")
					case endpoint + "/actions/start":
						mutated = true
						if scenario == "denied" {
							w.WriteHeader(http.StatusPaymentRequired)
							computeJSON(w, map[string]any{"error": "billing_denied", "billing_reason": "insufficient_balance"})
							return
						}
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
				a.Invoke(context.Background(), action.InvokeRequest{Config: powerActionConfig(t, a, vmType, operation)}, &resp)
				if resp.Diagnostics.ErrorsCount() != 1 || resp.Diagnostics[0].Summary() != tc.summary {
					t.Fatalf("wanted %q, got %v", tc.summary, resp.Diagnostics)
				}
				if mutated != tc.mutated {
					t.Fatalf("mutated=%t, want %t; diagnostics=%v", mutated, tc.mutated, resp.Diagnostics)
				}
			})
		}
	}
}

func TestVmPowerActionRejectsWrongStateBeforeAnyPOST(t *testing.T) {
	for _, vmType := range []string{"cloud", "gpu"} {
		for _, operation := range []string{"start", "stop", "reboot"} {
			required := "running"
			if operation == "start" {
				required = "stopped"
			}
			for _, status := range []string{"running", "stopped", "starting", "stopping", "rebooting", "creating", "deleting", "error", "paused", "unknown", " \tReBoOtInG\n", " \tRUNNING\n", " \tSTOPPED\n"} {
				if strings.ToLower(strings.TrimSpace(status)) == required {
					continue
				}
				t.Run(vmType+"/"+operation+"/"+status, func(t *testing.T) {
					endpoint := "/compute/" + vmType + "-vms/vm"
					var requests []string
					a := &vmPowerAction{client: computeTestClient(t, func(w http.ResponseWriter, r *http.Request) {
						requests = append(requests, r.Method+" "+r.URL.Path)
						if r.Method != http.MethodGet || r.URL.Path != endpoint {
							t.Errorf("wrong-state action reached %s %s", r.Method, r.URL.Path)
							http.Error(w, "unexpected request", http.StatusBadRequest)
							return
						}
						computeJSON(w, map[string]any{"id": "vm", "status": status})
					})}
					var resp action.InvokeResponse
					a.Invoke(context.Background(), action.InvokeRequest{Config: powerActionConfig(t, a, vmType, operation)}, &resp)
					if resp.Diagnostics.ErrorsCount() != 1 || resp.Diagnostics[0].Summary() != "Invalid VM power state" {
						t.Fatalf("expected state rejection, got %v", resp.Diagnostics)
					}
					for _, want := range []string{operation, vmType, fmt.Sprintf("%q", status), fmt.Sprintf("requires %q", required)} {
						if !strings.Contains(resp.Diagnostics[0].Detail(), want) {
							t.Errorf("diagnostic omitted %q: %v", want, resp.Diagnostics)
						}
					}
					if !reflect.DeepEqual(requests, []string{"GET " + endpoint}) {
						t.Fatalf("precondition made requests beyond VM inspection: %v", requests)
					}
				})
			}
		}
	}
}

func TestVmPowerActionWaitsForOperationAndCanonicalState(t *testing.T) {
	for _, vmType := range []string{"cloud", "gpu"} {
		for _, operation := range []string{"start", "stop", "reboot"} {
			t.Run(vmType+"/"+operation, func(t *testing.T) {
				initial, transitional, terminal := "running", "rebooting", "running"
				if operation == "start" {
					initial, transitional = "stopped", "starting"
				} else if operation == "stop" {
					transitional, terminal = "stopping", "stopped"
				}
				endpoint := "/compute/" + vmType + "-vms/vm"
				reads, polls, posts, admissions := 0, 0, 0, 0
				a := &vmPowerAction{client: computeTestClient(t, func(w http.ResponseWriter, r *http.Request) {
					switch r.Method + " " + r.URL.Path {
					case "GET " + endpoint:
						reads++
						status := initial
						if reads > 1 {
							if polls != 2 {
								t.Error("read canonical state before operation succeeded")
							}
							status = " \t" + strings.ToUpper(transitional) + "\n"
							if reads == 3 {
								status = " \t" + strings.ToUpper(terminal) + "\n"
							}
						}
						computeJSON(w, map[string]any{"_id": "vm", "status": status})
					case "POST /billing/resource-eligibility":
						admissions++
						blockEligibility(w, true, "INR")
					case "POST " + endpoint + "/actions/" + operation:
						posts++
						computeJSON(w, operationAccepted{VmID: "vm", OperationID: "op"})
					case "GET /compute/operations/op":
						polls++
						status := "running"
						if polls == 2 {
							status = "succeeded"
						}
						computeJSON(w, map[string]any{"status": status})
					default:
						t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
						http.NotFound(w, r)
					}
				})}
				a.client.operationTimeout = time.Second
				var resp action.InvokeResponse
				a.Invoke(context.Background(), action.InvokeRequest{Config: powerActionConfig(t, a, vmType, operation)}, &resp)
				computeNoErrors(t, resp.Diagnostics)
				wantAdmissions := 0
				if reads != 3 || polls != 2 || posts != 1 || admissions != wantAdmissions {
					t.Fatalf("reads=%d polls=%d posts=%d admissions=%d", reads, polls, posts, admissions)
				}
			})
		}
	}
}

func TestVmPowerActionIdempotencyKeys(t *testing.T) {
	for _, vmType := range []string{"cloud", "gpu"} {
		for _, tc := range []struct {
			name    string
			key     types.String
			invalid bool
		}{
			{"provided", types.StringValue("recovery-request"), false},
			{"generated", types.StringNull(), false},
			{"empty", types.StringValue(""), true},
			{"blank", types.StringValue(" \t\n"), true},
			{"unknown", types.StringUnknown(), true},
		} {
			t.Run(vmType+"/"+tc.name, func(t *testing.T) {
				endpoint := "/compute/" + vmType + "-vms/vm"
				var keys []string
				a := &vmPowerAction{client: computeTestClient(t, func(w http.ResponseWriter, r *http.Request) {
					if tc.invalid {
						t.Errorf("invalid key made request %s %s", r.Method, r.URL.Path)
					}
					switch r.Method + " " + r.URL.Path {
					case "GET " + endpoint:
						status := "stopped"
						if len(keys) > 0 {
							status = "running"
						}
						computeJSON(w, map[string]any{"id": "vm", "status": status})
					case "POST /billing/resource-eligibility":
						blockEligibility(w, true, "INR")
					case "POST " + endpoint + "/actions/start":
						keys = append(keys, r.Header.Get("X-Idempotency-Key"))
						if len(keys) == 1 {
							http.Error(w, "retryable fixture response", http.StatusServiceUnavailable)
							return
						}
						computeJSON(w, operationAccepted{VmID: "vm", OperationID: "op"})
					case "GET /compute/operations/op":
						computeJSON(w, map[string]any{"status": "succeeded"})
					default:
						t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
						http.NotFound(w, r)
					}
				})}
				var resp action.InvokeResponse
				a.Invoke(context.Background(), action.InvokeRequest{Config: powerActionConfig(t, a, vmType, "start", map[string]attr.Value{"idempotency_key": tc.key})}, &resp)
				if tc.invalid {
					if resp.Diagnostics.ErrorsCount() != 1 || resp.Diagnostics[0].Summary() != "Invalid idempotency key" {
						t.Fatalf("expected invalid key, got %v", resp.Diagnostics)
					}
					return
				}
				computeNoErrors(t, resp.Diagnostics)
				if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
					t.Fatalf("retry did not preserve request key: %q", keys)
				}
				if !tc.key.IsNull() && keys[0] != tc.key.ValueString() {
					t.Fatalf("caller key changed: %q", keys)
				}
			})
		}
	}
}

func TestVmPowerActionCancellationAndTimeout(t *testing.T) {
	for _, vmType := range []string{"cloud", "gpu"} {
		for _, phase := range []string{"operation", "canonical"} {
			for _, cancelled := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/cancel=%t", vmType, phase, cancelled), func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					endpoint := "/compute/" + vmType + "-vms/vm"
					var posts, reads atomic.Int32
					a := &vmPowerAction{client: computeTestClient(t, func(w http.ResponseWriter, r *http.Request) {
						switch r.Method + " " + r.URL.Path {
						case "GET " + endpoint:
							status := "running"
							if reads.Add(1) > 1 {
								status = "stopping"
								if phase == "canonical" && cancelled {
									cancel()
								}
							}
							computeJSON(w, map[string]any{"id": "vm", "status": status})
						case "POST " + endpoint + "/actions/stop":
							posts.Add(1)
							computeJSON(w, operationAccepted{VmID: "vm", OperationID: "op"})
						case "GET /compute/operations/op":
							status := "succeeded"
							if phase == "operation" {
								status = "running"
								if cancelled {
									cancel()
								}
							}
							computeJSON(w, map[string]any{"status": status})
						default:
							t.Errorf("unexpected request (stop needs no billing): %s %s", r.Method, r.URL.Path)
							http.NotFound(w, r)
						}
					})}
					a.client.operationTimeout = 100 * time.Millisecond
					var progress []string
					resp := action.InvokeResponse{SendProgress: func(event action.InvokeProgressEvent) {
						progress = append(progress, event.Message)
					}}
					a.Invoke(ctx, action.InvokeRequest{Config: powerActionConfig(t, a, vmType, "stop")}, &resp)
					want := context.DeadlineExceeded.Error()
					if cancelled {
						want = context.Canceled.Error()
					}
					if resp.Diagnostics.ErrorsCount() != 1 || !strings.Contains(resp.Diagnostics[0].Detail(), want) {
						t.Fatalf("expected %q, got %v", want, resp.Diagnostics)
					}
					if posts.Load() != 1 || !reflect.DeepEqual(progress, []string{"Waiting for VM power operation op."}) {
						t.Fatalf("replayed mutation or reported false success: posts=%d progress=%v", posts.Load(), progress)
					}
					if phase == "operation" && reads.Load() != 1 {
						t.Fatal("read canonical VM before operation succeeded")
					}
				})
			}
		}
	}
}
