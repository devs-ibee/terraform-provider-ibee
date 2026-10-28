package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func computeTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "token", "workspace")
	c.http = srv.Client()
	c.operationTimeout = 100 * time.Millisecond
	c.pollInterval = time.Millisecond
	c.retryDelay = time.Millisecond
	return c
}
func computeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func computeState(t *testing.T, r resource.Resource, m any) tfsdk.State {
	t.Helper()
	var sr resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &sr)
	s := tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(context.Background()), nil)}
	if m != nil {
		d := s.Set(context.Background(), m)
		if d.HasError() {
			t.Fatal(d)
		}
	}
	return s
}
func computePlanState(t *testing.T, r resource.Resource, m any) tfsdk.Plan {
	s := computeState(t, r, m)
	return tfsdk.Plan{Raw: s.Raw, Schema: s.Schema}
}
func computeNoErrors(t *testing.T, d diag.Diagnostics) {
	t.Helper()
	if d.HasError() {
		t.Fatal(d)
	}
}
func computeValidVMPlan() cloudVmModel {
	return cloudVmModel{ID: types.StringUnknown(), Name: types.StringValue("vm"), SiteID: types.StringValue("site"), PlanID: types.StringValue("plan"), BillingInterval: types.StringValue("HOURLY"), TemplateID: types.StringValue("image"), OsDistro: types.StringValue("ubuntu"), OsType: types.StringValue("linux"), Cpu: types.Int64Unknown(), RamMb: types.Int64Unknown(), DiskGb: types.Int64Unknown(), GpuCount: types.Int64Unknown(), GpuModel: types.StringUnknown(), Status: types.StringUnknown(), PublicIP: types.StringUnknown(), PrivateIP: types.StringUnknown(), PublicIPAction: types.StringValue("release"), SSHKeyIDs: types.SetValueMust(types.StringType, []attr.Value{types.StringValue("key-1")}), Tags: types.SetValueMust(types.StringType, []attr.Value{types.StringValue("web")})}
}
func computeCanonicalVM(kind string) map[string]any {
	return map[string]any{"_id": "vm-1", "name": "vm", "site_id": "site", "plan_id": "plan", "template_id": "image", "os_distro": "ubuntu", "os_type": "linux", "cpu": 4, "ram_mb": 8192, "disk_gb": 80, "gpu_count": 1, "gpu_model": "A100", "status": "running", "public_ip": "192.0.2.1", "private_ip": "10.0.0.2", "ssh_key_ids": []string{"key-1"}, "tags": []string{"web"}, "data_volumes": []any{}, "billing_catalog": computeTestSelectedBillingTerm("HOURLY")}
}
func computeEligibility(w http.ResponseWriter, allowed bool, sku string) {
	computeJSON(w, map[string]any{"allowed": allowed, "organization_id": "org", "reason": "eligible", "billing_mode": "PREPAID", "billing_state": "CURRENT", "currency": "INR", "sku_code": sku, "evaluated_at": "2026-09-27T12:00:00Z"})
}
func computeTestBillingCatalog(sku string, hourly int64) map[string]any {
	return map[string]any{"sku_code": sku, "billing_options": []any{
		map[string]any{"billing_interval": "HOURLY", "committed": false, "commitment_period": "HOURLY", "unit_price_minor": hourly},
		map[string]any{"billing_interval": "MONTHLY", "committed": true, "commitment_period": "MONTHLY", "commitment_months": 1, "committed_hours": 731, "unit_price_minor": int64(16), "price_unit": "HOUR"},
	}}
}
func computeTestSelectedBillingTerm(interval string) map[string]any {
	for _, raw := range computeTestBillingCatalog("SKU", 20)["billing_options"].([]any) {
		term := raw.(map[string]any)
		if term["billing_interval"] == interval {
			return term
		}
	}
	return map[string]any{"billing_interval": interval}
}
func computeCatalog(w http.ResponseWriter) {
	computeJSON(w, map[string]any{"plans": []any{map[string]any{"plan_id": "plan", "name": "Plan", "code": "SKU-1", "cpu": 4, "ram_mb": 8192, "disk_gb": 80, "gpu_count": 1, "gpu_model": "A100", "selectable": true, "pricing_status": "priced", "currency": "INR", "billing_interval": "MONTHLY", "hourly_price_minor": 20, "monthly_price_minor": 12000, "billing_catalog": computeTestBillingCatalog("SKU-1", 20)}}})
}
func TestComputeVMCreateBillingAndCanonicalState(t *testing.T) {
	for _, kind := range []string{"cloud", "gpu"} {
		t.Run(kind, func(t *testing.T) {
			var calls []string
			r := &cloudVmResource{vmType: kind}
			r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method+" "+req.URL.Path)
				switch req.URL.Path {
				case "/compute/plans":
					if req.URL.Query().Get("vm_type") != kind {
						t.Error("incorrect catalog type")
					}
					computeCatalog(w)
				case "/compute/" + kind + "-vms":
					var b map[string]any
					_ = json.NewDecoder(req.Body).Decode(&b)
					if req.Header.Get("X-Idempotency-Key") == "" {
						t.Error("missing idempotency")
					}
					if !reflect.DeepEqual(b["ssh_key_ids"], []any{"key-1"}) || !reflect.DeepEqual(b["tags"], []any{"web"}) {
						t.Errorf("missing access/tags: %+v", b)
					}
					if kind == "gpu" && b["gpu_model"] != "A100" {
						t.Error("missing GPU selection")
					}
					computeJSON(w, operationAccepted{VmID: "vm-1", OperationID: "op-1", Status: "accepted"})
				case "/compute/operations/op-1":
					computeJSON(w, map[string]any{"status": "succeeded"})
				case "/compute/" + kind + "-vms/vm-1":
					computeJSON(w, computeCanonicalVM(kind))
				default:
					t.Errorf("unexpected path %s", req.URL.Path)
					w.WriteHeader(500)
				}
			})
			resp := resource.CreateResponse{State: computeState(t, r, nil)}
			r.Create(context.Background(), resource.CreateRequest{Plan: computePlanState(t, r, computeValidVMPlan())}, &resp)
			computeNoErrors(t, resp.Diagnostics)
			var got cloudVmModel
			computeNoErrors(t, resp.State.Get(context.Background(), &got))
			if got.ID.ValueString() != "vm-1" || got.Status.ValueString() != "running" || got.Cpu.ValueInt64() != 4 || got.PublicIP.ValueString() != "192.0.2.1" {
				t.Fatalf("bad canonical state: %+v", got)
			}
			if len(calls) != 4 || calls[0] != "GET /compute/plans" || calls[1] != "POST /compute/"+kind+"-vms" {
				t.Fatal(calls)
			}
		})
	}
}

func TestComputeVMCreateUsesConfiguredCatalogInterval(t *testing.T) {
	for _, kind := range []string{"cloud", "gpu"} {
		for _, interval := range []string{"HOURLY", "MONTHLY"} {
			t.Run(kind+"/"+interval, func(t *testing.T) {
				r := &cloudVmResource{vmType: kind}
				catalogReads, creates := 0, 0
				r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
					switch req.URL.Path {
					case "/compute/plans":
						catalogReads++
						query := req.URL.Query()
						if query.Get("billing_interval") != interval || query.Get("vm_type") != kind || query.Get("site_id") != "site" || query.Get("currency") != "" {
							t.Errorf("catalog query does not match VM purchase: %v", query)
							http.Error(w, "no selectable plan for the requested interval", 400)
							return
						}
						computeCatalog(w)
					case "/compute/" + kind + "-vms":
						creates++
						var b struct {
							Catalog map[string]any `json:"billing_catalog"`
						}
						if err := json.NewDecoder(req.Body).Decode(&b); err != nil {
							t.Fatal(err)
						}
						if b.Catalog["billing_interval"] != interval || b.Catalog["committed"] != (interval == "MONTHLY") {
							t.Errorf("wrong selected create term: %+v", b.Catalog)
						}
						computeJSON(w, operationAccepted{VmID: "vm-1", OperationID: "op-1", Status: "accepted"})
					case "/compute/operations/op-1":
						computeJSON(w, map[string]any{"status": "succeeded"})
					case "/compute/" + kind + "-vms/vm-1":
						v := computeCanonicalVM(kind)
						v["billing_catalog"] = computeTestSelectedBillingTerm(interval)
						computeJSON(w, v)
					default:
						t.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
						w.WriteHeader(500)
					}
				})
				plan := computeValidVMPlan()
				plan.BillingInterval = types.StringValue(interval)
				resp := resource.CreateResponse{State: computeState(t, r, nil)}
				r.Create(context.Background(), resource.CreateRequest{Plan: computePlanState(t, r, plan)}, &resp)
				computeNoErrors(t, resp.Diagnostics)
				if catalogReads != 1 || creates != 1 {
					t.Fatalf("catalog reads=%d creates=%d", catalogReads, creates)
				}
			})
		}
	}
}

func TestComputePlansDataSourceBillingIntervalSelection(t *testing.T) {
	for _, kind := range []string{"cloud", "gpu"} {
		for _, configured := range []string{"", "HOURLY", "MONTHLY"} {
			t.Run(kind+"/"+configured, func(t *testing.T) {
				want := configured // An omitted interval is not sent; the API picks its default.
				d := &computePlansDataSource{}
				d.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
					query := req.URL.Query()
					if req.Method != "GET" || req.URL.Path != "/compute/plans" || query.Get("vm_type") != kind || query.Get("billing_interval") != want || query.Get("site_id") != "site" || query.Has("currency") {
						t.Errorf("incorrect catalog query: %s %s", req.Method, req.URL)
					}
					// An hourly-only GPU can carry display prices but remain unpriced
					// and unselectable for monthly discovery, as observed in production.
					selectable := query.Get("billing_interval") == "HOURLY"
					status := "unpriced"
					if selectable {
						status = "priced"
					}
					computeJSON(w, map[string]any{"plans": []any{map[string]any{"plan_id": "plan", "billing_interval": query.Get("billing_interval"), "hourly_price_minor": 3014, "selectable": selectable, "pricing_status": status}}})
				})
				var schema datasource.SchemaResponse
				d.Schema(context.Background(), datasource.SchemaRequest{}, &schema)
				cfg := computePlansModel{VmType: types.StringValue(kind), SiteID: types.StringValue("site"), Currency: types.StringNull(), BillingInterval: types.StringNull()}
				if configured != "" {
					cfg.BillingInterval = types.StringValue(configured)
				}
				state := tfsdk.State{Schema: schema.Schema}
				computeNoErrors(t, state.Set(context.Background(), cfg))
				resp := datasource.ReadResponse{State: state}
				d.Read(context.Background(), datasource.ReadRequest{Config: tfsdk.Config{Schema: schema.Schema, Raw: state.Raw}}, &resp)
				computeNoErrors(t, resp.Diagnostics)
				var got computePlansModel
				computeNoErrors(t, resp.State.Get(context.Background(), &got))
				if got.BillingInterval.ValueString() != want || len(got.Plans) != 1 || got.Plans[0].Selectable.ValueBool() != (want == "HOURLY") || got.Plans[0].BillingInterval.ValueString() != want {
					t.Fatalf("catalog interval/availability was lost: %+v", got)
				}
			})
		}
	}
}
func TestComputeVMDeniedEligibilityNeverCreates(t *testing.T) {
	r := &cloudVmResource{vmType: "cloud"}
	r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/compute/plans":
			computeCatalog(w)
		case "/billing/resource-eligibility":
			computeEligibility(w, false, "SKU-1")
		default:
			if req.URL.Path != "/compute/cloud-vms" {
				t.Errorf("unexpected request %s", req.URL.Path)
			}
			w.WriteHeader(http.StatusPaymentRequired)
			computeJSON(w, map[string]any{"error": "billing_denied", "billing_reason": "insufficient_balance"})
		}
	})
	resp := resource.CreateResponse{State: computeState(t, r, nil)}
	r.Create(context.Background(), resource.CreateRequest{Plan: computePlanState(t, r, computeValidVMPlan())}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected billing failure")
	}
}
func TestComputeVMFailureKeepsKnownPartialState(t *testing.T) {
	r := &cloudVmResource{vmType: "cloud"}
	r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/compute/plans":
			computeCatalog(w)
		case "/billing/resource-eligibility":
			computeEligibility(w, true, "SKU-1")
		case "/compute/cloud-vms":
			computeJSON(w, operationAccepted{VmID: "vm-1", OperationID: "op", Status: "accepted"})
		case "/compute/operations/op":
			computeJSON(w, map[string]string{"status": "failed", "error_code": "capacity_exhausted", "error_message": "No suitable host"})
		default:
			t.Error("unexpected request")
		}
	})
	resp := resource.CreateResponse{State: computeState(t, r, nil)}
	r.Create(context.Background(), resource.CreateRequest{Plan: computePlanState(t, r, computeValidVMPlan())}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected failure")
	}
	var got cloudVmModel
	computeNoErrors(t, resp.State.Get(context.Background(), &got))
	if got.ID.ValueString() != "vm-1" || got.PublicIP.IsUnknown() || got.PrivateIP.IsUnknown() || got.Status.IsUnknown() {
		t.Fatalf("unrecoverable partial state: %+v", got)
	}
}
func TestComputeVMImportHydratesAndDetectsDrift(t *testing.T) {
	vm := computeCanonicalVM("cloud")
	vm["name"] = "portal-change"
	vm["plan_id"] = "resized-plan"
	vm["cpu"] = 8
	r := &cloudVmResource{vmType: "cloud"}
	r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Method != "GET" {
			t.Error("billing or mutation during import")
		}
		computeJSON(w, vm)
	})
	imp := resource.ImportStateResponse{State: computeState(t, r, nil)}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "vm-1"}, &imp)
	computeNoErrors(t, imp.Diagnostics)
	resp := resource.ReadResponse{State: imp.State}
	r.Read(context.Background(), resource.ReadRequest{State: imp.State}, &resp)
	computeNoErrors(t, resp.Diagnostics)
	var got cloudVmModel
	computeNoErrors(t, resp.State.Get(context.Background(), &got))
	if got.Name.ValueString() != "portal-change" || got.PlanID.ValueString() != "resized-plan" || got.Cpu.ValueInt64() != 8 || got.SSHKeyIDs.IsNull() || !got.PublicIPAction.IsNull() {
		t.Fatal(got)
	}
}
func TestComputeVMForbiddenRefreshPreservesState(t *testing.T) {
	r := &cloudVmResource{vmType: "cloud"}
	r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) { w.WriteHeader(http.StatusForbidden) })
	m := computeValidVMPlan()
	m.ID = types.StringValue("vm-1")
	s := computeState(t, r, m)
	resp := resource.ReadResponse{State: s}
	r.Read(context.Background(), resource.ReadRequest{State: s}, &resp)
	if !resp.Diagnostics.HasError() || resp.State.Raw.IsNull() {
		t.Fatal("forbidden must preserve state")
	}
}
func TestComputeOperationTerminalErrorsAndCancellation(t *testing.T) {
	for _, status := range []string{"failed", "cancelled", "timed_out", "mystery"} {
		t.Run(status, func(t *testing.T) {
			c := computeTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				computeJSON(w, map[string]string{"status": status, "error_code": "E_CAPACITY", "error_message": "No host"})
			})
			err := c.waitOperation(context.Background(), "op", time.Second)
			if err == nil || !strings.Contains(err.Error(), status) {
				t.Fatalf("expected %s failure, got %v", status, err)
			}
		})
	}
	t.Run("forbidden", func(t *testing.T) {
		calls := 0
		c := computeTestClient(t, func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(403) })
		if c.waitOperation(context.Background(), "op", time.Second) == nil || calls != 1 {
			t.Fatal("must fail authorization immediately")
		}
	})
	t.Run("cancel", func(t *testing.T) {
		c := computeTestClient(t, func(w http.ResponseWriter, r *http.Request) { computeJSON(w, map[string]string{"status": "running"}) })
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if c.waitOperation(ctx, "op", time.Second) == nil {
			t.Fatal("must stop on cancellation")
		}
	})
}
func TestComputePlanTermSelectionPassesCatalogThrough(t *testing.T) {
	p := computePlan{PlanID: "p", BillingCatalog: map[string]any{"sku_code": "SKU"}}
	if err := p.selectBillingTerm(""); err != nil || !reflect.DeepEqual(p.BillingCatalog, map[string]any{"sku_code": "SKU"}) {
		t.Fatalf("an omitted term must send the catalog unchanged: %v %v", err, p.BillingCatalog)
	}
	if err := p.selectBillingTerm("HOURLY"); err != nil || !reflect.DeepEqual(p.BillingCatalog, map[string]any{"sku_code": "SKU"}) {
		t.Fatal("a term the catalog does not list must leave the catalog for the API to answer")
	}
	// Any term the catalog lists is merged as given; the API decides whether it can be bought.
	p.BillingCatalog = computeTestBillingCatalog("SKU", 20)
	p.BillingCatalog["billing_options"] = append(p.BillingCatalog["billing_options"].([]any), map[string]any{"billing_interval": "MONTHLY_3", "committed": true, "commitment_months": 3, "unit_price_minor": 12})
	if err := p.selectBillingTerm("MONTHLY_3"); err != nil || p.BillingCatalog["commitment_months"] != 3 || p.BillingCatalog["billing_interval"] != "MONTHLY_3" {
		t.Fatalf("listed term not merged: %v %v", err, p.BillingCatalog)
	}
}

// A plan whose catalog lists no option for the term is sent as listed; the API answers.
func TestComputeVMCreateLeavesPlanEligibilityToAPI(t *testing.T) {
	creates := 0
	r := &cloudVmResource{vmType: "cloud"}
	r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/compute/plans":
			if req.URL.Query().Get("billing_interval") != "HOURLY" {
				t.Error("an omitted term must default to HOURLY")
			}
			computeJSON(w, map[string]any{"plans": []any{map[string]any{"plan_id": "plan", "code": "SKU-1", "cpu": 4, "ram_mb": 8192, "disk_gb": 80, "selectable": false, "pricing_status": "unpriced", "billing_catalog": map[string]any{"sku_code": "OTHER"}}}})
		case "/compute/cloud-vms":
			creates++
			var b map[string]any
			_ = json.NewDecoder(req.Body).Decode(&b)
			if !reflect.DeepEqual(b["billing_catalog"], map[string]any{"sku_code": "OTHER"}) {
				t.Errorf("catalog not passed through: %v", b["billing_catalog"])
			}
			w.WriteHeader(http.StatusBadRequest)
			computeJSON(w, map[string]any{"error": "compute_billing_term_required", "admission_context_id": "adm-1"})
		default:
			t.Errorf("unexpected request %s", req.URL.Path)
		}
	})
	plan := computeValidVMPlan()
	plan.BillingInterval = types.StringUnknown()
	resp := resource.CreateResponse{State: computeState(t, r, nil)}
	r.Create(context.Background(), resource.CreateRequest{Plan: computePlanState(t, r, plan)}, &resp)
	if creates != 1 || !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "HTTP 400 compute_billing_term_required") {
		t.Fatalf("the API, not the provider, must decide: creates=%d %v", creates, resp.Diagnostics)
	}
}
func computeBackupModel() vmBackupPolicyModel {
	return vmBackupPolicyModel{ID: types.StringValue("vm-1"), VmID: types.StringValue("vm-1"), PolicyID: types.StringValue("policy"), Frequency: types.StringValue("daily"), Timezone: types.StringValue("UTC"), Hour: types.Int64Value(20), Minute: types.Int64Value(0), DayOfWeek: types.Int64Null(), WindowMinutes: types.Int64Value(30), RetentionDays: types.Int64Value(7), FullBackupIntervalDays: types.Int64Value(7), IncrementalEnabled: types.BoolValue(true), NextRunAt: types.StringNull()}
}
func computeBackupResponse(enabled bool) map[string]any {
	return map[string]any{"vm_id": "vm-1", "policy_id": "policy", "enabled": enabled, "schedule": map[string]any{"frequency": "daily", "timezone": "UTC", "hour": 20, "minute": 0, "window_minutes": 30}, "retention_days": 7, "full_backup_interval_days": 7, "incremental_enabled": true}
}
func TestComputeBackupDestroyDisablesWithoutDeletingRecoveryPoints(t *testing.T) {
	calls := 0
	r := &vmBackupPolicyResource{vmType: "cloud"}
	r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		calls++
		if req.Method != "POST" || req.URL.Path != "/compute/cloud-vms/vm-1/backups/disable" {
			t.Error("destructive or billing request during disable")
		}
		computeJSON(w, computeBackupResponse(false))
	})
	resp := resource.DeleteResponse{}
	r.Delete(context.Background(), resource.DeleteRequest{State: computeState(t, r, computeBackupModel())}, &resp)
	computeNoErrors(t, resp.Diagnostics)
	if calls != 1 {
		t.Fatal(calls)
	}
}
func TestComputeSnapshotImportRestoresSelection(t *testing.T) {
	r := &vmSnapshotResource{vmType: "gpu"}
	r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Method != "GET" || req.URL.Path != "/compute/gpu-vm-snapshots/snap" {
			t.Error(req.URL.Path)
		}
		computeJSON(w, map[string]any{"snapshot_set_id": "snap", "vm_id": "vm-1", "name": "nightly", "capture_scope": "selective", "status": "succeeded", "recovery_point_id": "rp", "volume_manifest": []any{map[string]any{"role": "root", "source_volume_id": "root"}, map[string]any{"role": "data", "source_volume_id": "data"}}})
	})
	imp := resource.ImportStateResponse{State: computeState(t, r, nil)}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "snap"}, &imp)
	computeNoErrors(t, imp.Diagnostics)
	resp := resource.ReadResponse{State: imp.State}
	r.Read(context.Background(), resource.ReadRequest{State: imp.State}, &resp)
	computeNoErrors(t, resp.Diagnostics)
	var got vmSnapshotModel
	computeNoErrors(t, resp.State.Get(context.Background(), &got))
	if got.VmID.ValueString() != "vm-1" || len(got.SelectedDataVolumeIDs.Elements()) != 1 {
		t.Fatal(got)
	}
}
func TestComputeAttachmentProjectionRequired(t *testing.T) {
	for _, expose := range []bool{false, true} {
		t.Run(fmt.Sprint(expose), func(t *testing.T) {
			r := &vmVolumeAttachmentResource{vmType: "cloud"}
			r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
				vm := map[string]any{"id": "vm-1"}
				if expose {
					vm["data_volumes"] = []any{}
				}
				computeJSON(w, vm)
			})
			m := vmVolumeAttachmentModel{VmID: types.StringValue("vm-1"), VolumeID: types.StringValue("v")}
			found, err := r.refresh(context.Background(), &m)
			if found {
				t.Fatal("no attachment")
			}
			if expose && err != nil {
				t.Fatal(err)
			}
			if !expose && err == nil {
				t.Fatal("missing projection must not be interpreted as missing attachment")
			}
		})
	}
}
func TestComputeAttachmentImportAndUnmountPolicy(t *testing.T) {
	r := &vmVolumeAttachmentResource{vmType: "cloud"}
	r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/block-storage/volumes/vol-1" {
			computeJSON(w, map[string]any{"id": "vol-1"}) // Legacy catalog omission remains importable.
			return
		}
		computeJSON(w, map[string]any{"_id": "vm-1", "data_volumes": []any{map[string]any{"volume_id": "vol-1", "mode": "single-writer", "guest_device": "/dev/vdb"}}})
	})
	imp := resource.ImportStateResponse{State: computeState(t, r, nil)}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "vm-1/vol-1"}, &imp)
	computeNoErrors(t, imp.Diagnostics)
	resp := resource.ReadResponse{State: imp.State}
	r.Read(context.Background(), resource.ReadRequest{State: imp.State}, &resp)
	computeNoErrors(t, resp.Diagnostics)
	var got vmVolumeAttachmentModel
	computeNoErrors(t, resp.State.Get(context.Background(), &got))
	if got.Mode.ValueString() != "single-writer" || got.ConfirmUnmounted.ValueBool() || got.GuestDevice.ValueString() != "/dev/vdb" {
		t.Fatal(got)
	}
}

func TestComputeSnapshotFailureRetainsRecoverableIdentity(t *testing.T) {
	r := &vmSnapshotResource{vmType: "cloud"}
	r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/billing/resource-eligibility":
			computeEligibility(w, true, "SNAPSHOT-STD")
		case "/compute/cloud-vms/vm-1/snapshots":
			var body map[string]any
			_ = json.NewDecoder(req.Body).Decode(&body)
			if body["billing_catalog"] == nil {
				t.Error("snapshot purchase omitted its catalog")
				http.Error(w, "missing catalog", 422)
				return
			}
			computeJSON(w, map[string]any{"snapshot_set_id": "snap", "status": "queued"})
		case "/compute/cloud-vm-snapshots/snap":
			computeJSON(w, map[string]any{"snapshot_set_id": "snap", "vm_id": "vm-1", "name": "snapshot", "capture_scope": "root_only", "status": "failed"})
		default:
			t.Error(req.URL.Path)
		}
	})
	m := vmSnapshotModel{ID: types.StringUnknown(), VmID: types.StringValue("vm-1"), Name: types.StringValue("snapshot"), Description: types.StringNull(), Mode: types.StringValue("root_only"), SelectedDataVolumeIDs: types.SetValueMust(types.StringType, []attr.Value{}), Status: types.StringUnknown(), RecoveryPointID: types.StringUnknown()}
	m.BillingCatalog = types.StringValue(`{"sku_id":"snapshot-sku","sku_code":"SNAPSHOT-STD","product_code":"snapshot_storage","currency":"INR"}`)
	resp := resource.CreateResponse{State: computeState(t, r, nil)}
	r.Create(context.Background(), resource.CreateRequest{Plan: computePlanState(t, r, m)}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("failed snapshot accepted")
	}
	var got vmSnapshotModel
	computeNoErrors(t, resp.State.Get(context.Background(), &got))
	if got.ID.ValueString() != "snap" || got.Status.ValueString() != "failed" || got.RecoveryPointID.IsUnknown() {
		t.Fatal("snapshot failure lost identity or unknown state")
	}
}

func TestComputeVMChangesWithAnOperationUpdateInPlace(t *testing.T) {
	r := &cloudVmResource{vmType: "cloud"}
	var schema resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schema)
	for name, wantReplace := range map[string]bool{"plan_id": false, "ssh_key_ids": false, "tags": true, "name": true, "template_id": true} {
		description := ""
		switch attribute := schema.Schema.Attributes[name].(type) {
		case rschema.StringAttribute:
			for _, m := range attribute.PlanModifiers {
				description += m.Description(context.Background())
			}
		case rschema.SetAttribute:
			for _, m := range attribute.PlanModifiers {
				description += m.Description(context.Background())
			}
		}
		if strings.Contains(description, "destroy and recreate") != wantReplace {
			t.Errorf("%s: requires replace=%v, want %v (%q)", name, !wantReplace, wantReplace, description)
		}
	}
}

func computeVMUpdate(t *testing.T, r *cloudVmResource, state, plan cloudVmModel) resource.UpdateResponse {
	t.Helper()
	prior := computeState(t, r, state)
	resp := resource.UpdateResponse{State: prior}
	r.Update(context.Background(), resource.UpdateRequest{State: prior, Plan: computePlanState(t, r, plan)}, &resp)
	return resp
}

func TestComputeVMPlanChangeResizesInPlace(t *testing.T) {
	for _, kind := range []string{"cloud", "gpu"} {
		for _, refused := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/refused=%t", kind, refused), func(t *testing.T) {
				var requests []string
				vm := computeCanonicalVM(kind)
				r := &cloudVmResource{vmType: kind}
				r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
					requests = append(requests, req.Method+" "+req.URL.Path)
					switch req.Method + " " + req.URL.Path {
					case "GET /compute/plans":
						if req.URL.Query().Has("billing_interval") {
							t.Errorf("resize needs only the plan's shape: %v", req.URL.Query())
						}
						catalog := computeTestBillingCatalog("SKU-L", 40)
						catalog["plan_id"] = "plan-large"
						computeJSON(w, map[string]any{"plans": []any{map[string]any{"plan_id": "plan-large", "code": "SKU-L", "cpu": 8, "ram_mb": 16384, "disk_gb": 160, "billing_catalog": catalog}}})
					case "POST /compute/" + kind + "-vms/vm-1/actions/resize":
						var b map[string]any
						_ = json.NewDecoder(req.Body).Decode(&b)
						if req.Header.Get("X-Idempotency-Key") == "" || len(b) != 3 || b["cpu"] != float64(8) || b["ram_mb"] != float64(16384) || b["disk_gb"] != float64(160) {
							t.Errorf("resize must send the new plan's cpu, ram_mb and disk_gb only: %v", b)
						}
						if refused {
							w.WriteHeader(http.StatusConflict)
							computeJSON(w, map[string]any{"detail": map[string]any{"message": "In-place resize cannot shrink root disk. Migration is required.", "decision": "migration_required"}})
							return
						}
						vm["plan_id"], vm["cpu"], vm["ram_mb"], vm["disk_gb"] = "plan-large", 8, 16384, 160
						computeJSON(w, operationAccepted{VmID: "vm-1", OperationID: "op-resize", Status: "accepted"})
					case "GET /compute/operations/op-resize":
						computeJSON(w, map[string]any{"status": "succeeded"})
					case "GET /compute/" + kind + "-vms/vm-1":
						computeJSON(w, vm)
					default:
						t.Errorf("unexpected request %s %s (no stop/start or replacement expected)", req.Method, req.URL.Path)
						w.WriteHeader(500)
					}
				})
				state := computeValidVMPlan()
				state.ID, state.Cpu, state.RamMb, state.DiskGb, state.GpuCount = types.StringValue("vm-1"), types.Int64Value(4), types.Int64Value(8192), types.Int64Value(80), types.Int64Value(1)
				state.GpuModel, state.Status, state.PublicIP, state.PrivateIP = types.StringValue("A100"), types.StringValue("running"), types.StringNull(), types.StringNull()
				plan := state
				plan.PlanID = types.StringValue("plan-large")
				plan.Cpu, plan.RamMb, plan.DiskGb, plan.Status = types.Int64Unknown(), types.Int64Unknown(), types.Int64Unknown(), types.StringUnknown()
				resp := computeVMUpdate(t, r, state, plan)
				var got cloudVmModel
				computeNoErrors(t, resp.State.Get(context.Background(), &got))
				if refused {
					if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "HTTP 409: In-place resize cannot shrink root disk. Migration is required.") {
						t.Fatalf("server refusal not relayed: %v", resp.Diagnostics)
					}
					if got.PlanID.ValueString() != "plan" {
						t.Fatal("refused resize changed state")
					}
					return
				}
				computeNoErrors(t, resp.Diagnostics)
				if got.ID.ValueString() != "vm-1" || got.PlanID.ValueString() != "plan-large" || got.Cpu.ValueInt64() != 8 || got.DiskGb.ValueInt64() != 160 {
					t.Fatalf("resize not reflected: %+v", got)
				}
				if want := []string{"GET /compute/plans", "POST /compute/" + kind + "-vms/vm-1/actions/resize", "GET /compute/operations/op-resize", "GET /compute/" + kind + "-vms/vm-1", "GET /compute/" + kind + "-vms/vm-1"}; !reflect.DeepEqual(requests, want) {
					t.Fatalf("requests %v, want %v", requests, want)
				}
			})
		}
	}
}

func TestComputeVMSSHKeyChangeUsesAccessOperation(t *testing.T) {
	var changes []string
	vm := computeCanonicalVM("cloud")
	r := &cloudVmResource{vmType: "cloud"}
	r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		switch req.Method + " " + req.URL.Path {
		case "PATCH /compute/cloud-vms/vm-1/actions/access":
			var b struct {
				Mode string   `json:"ssh_key_mode"`
				IDs  []string `json:"ssh_key_ids"`
			}
			_ = json.NewDecoder(req.Body).Decode(&b)
			changes = append(changes, b.Mode+" "+strings.Join(b.IDs, ","))
			computeJSON(w, operationAccepted{VmID: "vm-1", OperationID: "op-" + b.Mode, Status: "accepted"})
		case "GET /compute/operations/op-add", "GET /compute/operations/op-remove":
			computeJSON(w, map[string]any{"status": "succeeded"})
		case "GET /compute/cloud-vms/vm-1":
			vm["ssh_key_ids"] = []string{"key-2"}
			computeJSON(w, vm)
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
			w.WriteHeader(500)
		}
	})
	state := computeValidVMPlan()
	state.ID, state.Cpu, state.RamMb, state.DiskGb, state.GpuCount = types.StringValue("vm-1"), types.Int64Value(4), types.Int64Value(8192), types.Int64Value(80), types.Int64Value(1)
	state.GpuModel, state.Status, state.PublicIP, state.PrivateIP = types.StringValue("A100"), types.StringValue("running"), types.StringNull(), types.StringNull()
	plan := state
	plan.SSHKeyIDs = types.SetValueMust(types.StringType, []attr.Value{types.StringValue("key-2")})
	resp := computeVMUpdate(t, r, state, plan)
	computeNoErrors(t, resp.Diagnostics)
	if !reflect.DeepEqual(changes, []string{"add key-2", "remove key-1"}) {
		t.Fatalf("keys must be added before others are removed: %v", changes)
	}
}

func TestComputeVMDeleteWaitsForOperation(t *testing.T) {
	for _, kind := range []string{"cloud", "gpu"} {
		t.Run(kind, func(t *testing.T) {
			deleted := false
			r := &cloudVmResource{vmType: kind}
			r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
				switch req.Method + " " + req.URL.Path {
				case "DELETE /compute/" + kind + "-vms/vm-1":
					var b map[string]any
					_ = json.NewDecoder(req.Body).Decode(&b)
					if b["public_ip_action"] != "reserve" || req.Header.Get("X-Idempotency-Key") == "" {
						t.Error("unexpected delete request", b)
					}
					computeJSON(w, operationAccepted{VmID: "vm-1", OperationID: "op-delete", Status: "accepted"})
				case "GET /compute/operations/op-delete":
					deleted = true
					computeJSON(w, map[string]any{"status": "succeeded"})
				default:
					t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
					w.WriteHeader(500)
				}
			})
			state := computeValidVMPlan()
			state.ID, state.Cpu, state.RamMb, state.DiskGb, state.GpuCount = types.StringValue("vm-1"), types.Int64Value(4), types.Int64Value(8192), types.Int64Value(80), types.Int64Value(1)
			state.GpuModel, state.Status, state.PublicIP, state.PrivateIP, state.PublicIPAction = types.StringValue("A100"), types.StringValue("running"), types.StringNull(), types.StringNull(), types.StringValue("reserve")
			resp := resource.DeleteResponse{}
			r.Delete(context.Background(), resource.DeleteRequest{State: computeState(t, r, state)}, &resp)
			computeNoErrors(t, resp.Diagnostics)
			if !deleted {
				t.Fatal("delete did not wait for its operation")
			}
		})
	}
}

// Without delete_public_ip_action the provider sends no public_ip_action (no
// default of its own) and relays the API's answer, here the 400 the API gives
// for a VM with an automatically assigned public IP.
func TestComputeVMDeleteWithoutPublicIPActionRelaysTheAPIAnswer(t *testing.T) {
	for _, kind := range []string{"cloud", "gpu"} {
		t.Run(kind, func(t *testing.T) {
			deletes := 0
			r := &cloudVmResource{vmType: kind}
			r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
				switch req.Method + " " + req.URL.Path {
				case "DELETE /compute/" + kind + "-vms/vm-1":
					deletes++
					raw, _ := io.ReadAll(req.Body)
					if len(raw) != 0 || req.Header.Get("Content-Type") != "" || req.Header.Get("X-Idempotency-Key") == "" {
						t.Errorf("delete without a configured action must send no body: %q", raw)
					}
					w.WriteHeader(400)
					computeJSON(w, map[string]any{"error": "public_ip_action_required", "message": "public_ip_action is required for a VM with an automatically assigned public IP"})
				default:
					t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
					w.WriteHeader(500)
				}
			})
			state := computeValidVMPlan()
			state.ID, state.Cpu, state.RamMb, state.DiskGb, state.GpuCount = types.StringValue("vm-1"), types.Int64Value(4), types.Int64Value(8192), types.Int64Value(80), types.Int64Value(1)
			state.GpuModel, state.Status, state.PublicIP, state.PrivateIP, state.PublicIPAction = types.StringValue("A100"), types.StringValue("running"), types.StringValue("192.0.2.1"), types.StringNull(), types.StringNull()
			resp := resource.DeleteResponse{}
			r.Delete(context.Background(), resource.DeleteRequest{State: computeState(t, r, state)}, &resp)
			if deletes != 1 || resp.Diagnostics.ErrorsCount() != 1 {
				t.Fatalf("deletes=%d diagnostics=%v", deletes, resp.Diagnostics)
			}
			if d := resp.Diagnostics[0]; d.Summary() != "Failed to delete VM" || !strings.Contains(d.Detail(), "HTTP 400 public_ip_action_required") || !strings.Contains(d.Detail(), "automatically assigned public IP") {
				t.Fatalf("the API answer must reach the caller unchanged: %s / %s", d.Summary(), d.Detail())
			}
		})
	}
}

// delete_public_ip_action has no default: a configuration without it keeps a
// null value through create and refresh.
func TestComputeVMPublicIPActionHasNoDefault(t *testing.T) {
	r := &cloudVmResource{vmType: "cloud"}
	var sr resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &sr)
	attribute := sr.Schema.Attributes["delete_public_ip_action"].(rschema.StringAttribute)
	if attribute.Default != nil || attribute.Computed || !attribute.Optional {
		t.Fatalf("delete_public_ip_action must be optional without a default: %+v", attribute)
	}
	r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		computeJSON(w, computeCanonicalVM("cloud"))
	})
	state := computeValidVMPlan()
	state.ID, state.PublicIPAction = types.StringValue("vm-1"), types.StringNull()
	if err := r.refresh(context.Background(), &state); err != nil {
		t.Fatal(err)
	}
	if !state.PublicIPAction.IsNull() {
		t.Fatalf("refresh invented a public IP action: %v", state.PublicIPAction)
	}
}
