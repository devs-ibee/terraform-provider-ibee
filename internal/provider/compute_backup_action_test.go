package provider

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestVmBackupCreateAction(t *testing.T) {
	for _, kind := range []string{"cloud", "gpu"} {
		for _, override := range []bool{false, true} {
			t.Run(kind+"/catalog_override="+map[bool]string{false: "false", true: "true"}[override], func(t *testing.T) {
				var sent map[string]any
				polls := 0
				a := &vmBackupCreateAction{}
				a.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
					switch req.Method + " " + req.URL.Path {
					case "POST /compute/" + kind + "-vms/vm-1/backups/runs":
						sent = decodeBody(t, req)
						computeJSON(w, map[string]any{"run_id": "run-1", "status": "queued"})
					case "GET /compute/" + kind + "-vm-backups/runs/run-1":
						polls++
						status := "running"
						if polls == 2 {
							status = "succeeded"
						}
						computeJSON(w, map[string]any{"run_id": "run-1", "status": status})
					default:
						t.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
						w.WriteHeader(500)
					}
				})
				values := map[string]attr.Value{"vm_type": types.StringValue(kind), "vm_id": types.StringValue("vm-1")}
				want := map[string]any{}
				if override {
					values["billing_catalog"] = types.StringValue(`{"sku_id":9007199254740993,"sku_code":"BACKUP"}`)
					values["requested_by"], values["reason"] = types.StringValue("terraform"), types.StringValue("before upgrade")
					want = map[string]any{"billing_catalog": map[string]any{"sku_id": json.Number("9007199254740993"), "sku_code": "BACKUP"}, "requested_by": "terraform", "reason": "before upgrade"}
				}
				resp, progress := invokeAction(t, a, values)
				computeNoErrors(t, resp.Diagnostics)
				if !reflect.DeepEqual(sent, want) || polls != 2 {
					t.Fatalf("body=%v polls=%d, want body=%v and two polls", sent, polls, want)
				}
				if !reflect.DeepEqual(progress, []string{"Waiting for backup run-1.", "Backup run-1 succeeded."}) {
					t.Fatal(progress)
				}
			})
		}
	}
}

func TestVmBackupCreateRelaysFailures(t *testing.T) {
	for _, tc := range []struct {
		name           string
		code           int
		started, final map[string]any
		want           string
		requests       int
	}{
		{"disabled", 409, map[string]any{"detail": "Backups are not enabled"}, nil, "not enabled", 1},
		{"busy", 409, map[string]any{"detail": "A backup run is already queued"}, nil, "already queued", 1},
		{"missing id", 200, map[string]any{"status": "queued"}, nil, "no run_id", 1},
		{"failed", 200, map[string]any{"run_id": "run-1", "status": "queued"}, map[string]any{"status": "failed", "error_message": "remote unavailable"}, "remote unavailable", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			a := &vmBackupCreateAction{}
			a.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
				requests++
				if req.Method == http.MethodPost {
					w.WriteHeader(tc.code)
					computeJSON(w, tc.started)
				} else {
					computeJSON(w, tc.final)
				}
			})
			resp, _ := invokeAction(t, a, map[string]attr.Value{"vm_type": types.StringValue("cloud"), "vm_id": types.StringValue("vm-1")})
			if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics[0].Detail(), tc.want) || requests != tc.requests {
				t.Fatalf("%v requests=%d", resp.Diagnostics, requests)
			}
		})
	}
}

func TestVmBackupCreateRejectsMalformedCatalogBeforeDispatch(t *testing.T) {
	a := &vmBackupCreateAction{}
	a.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		t.Error("malformed JSON catalog must not dispatch a backup")
		w.WriteHeader(500)
	})
	resp, _ := invokeAction(t, a, map[string]attr.Value{
		"vm_type": types.StringValue("cloud"), "vm_id": types.StringValue("vm-1"),
		"billing_catalog": types.StringValue(`[]`),
	})
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics[0].Detail(), "one JSON object") {
		t.Fatal(resp.Diagnostics)
	}
}

func TestVmBackupRestoreCancelRequiresConfirmation(t *testing.T) {
	a := &vmBackupRestoreCancelAction{}
	a.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		computeJSON(w, map[string]any{"restore_id": "restore-1", "status": "running"})
	})
	resp, _ := invokeAction(t, a, map[string]attr.Value{"vm_type": types.StringValue("cloud"), "restore_id": types.StringValue("restore-1")})
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics[0].Detail(), "did not confirm") {
		t.Fatal(resp.Diagnostics)
	}
}

func TestVmBackupRestoreCancelAction(t *testing.T) {
	for _, kind := range []string{"cloud", "gpu"} {
		for _, code := range []int{200, 409, 403} {
			t.Run(kind+"/"+http.StatusText(code), func(t *testing.T) {
				requests := 0
				a := &vmBackupRestoreCancelAction{}
				a.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
					requests++
					if req.Method != http.MethodPost || req.URL.Path != "/compute/"+kind+"-vm-backups/restores/restore-1/actions/cancel" {
						t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
					}
					w.WriteHeader(code)
					if code == 200 {
						computeJSON(w, map[string]any{"restore_id": "restore-1", "status": "cancelled"})
					} else {
						computeJSON(w, map[string]any{"detail": "restore is running or not authorized"})
					}
				})
				resp, progress := invokeAction(t, a, map[string]attr.Value{"vm_type": types.StringValue(kind), "restore_id": types.StringValue("restore-1")})
				if requests != 1 {
					t.Fatalf("cancellation must be sent once: %d", requests)
				}
				if code == 200 {
					computeNoErrors(t, resp.Diagnostics)
					if !reflect.DeepEqual(progress, []string{"Restore restore-1 cancelled."}) {
						t.Fatal(progress)
					}
				} else if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics[0].Detail(), "running or not authorized") {
					t.Fatal(resp.Diagnostics)
				}
			})
		}
	}
}
