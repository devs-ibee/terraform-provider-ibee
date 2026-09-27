package provider

import (
	"context"
	"encoding/json"
	"fmt"
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
				case "/billing/resource-eligibility":
					var b billingEligibilityRequest
					_ = json.NewDecoder(req.Body).Decode(&b)
					if b.SKUCode != "" && (b.SKUCode != "SKU-1" || b.EstimatedCostMinor == nil || *b.EstimatedCostMinor != 20) {
						t.Errorf("untrusted billing payload: %+v", b)
					}
					computeEligibility(w, true, "SKU-1")
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
			if len(calls) != 6 || calls[0] != "POST /billing/resource-eligibility" || calls[2] != "POST /billing/resource-eligibility" {
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
					case "/billing/resource-eligibility":
						var b billingEligibilityRequest
						_ = json.NewDecoder(req.Body).Decode(&b)
						if b.SKUCode != "" {
							want := int64(20)
							if interval == "MONTHLY" {
								want = 16 * 731
							}
							if b.EstimatedCostMinor == nil || *b.EstimatedCostMinor != want {
								t.Errorf("wrong %s admission estimate: %+v", interval, b)
							}
						}
						computeEligibility(w, true, "SKU-1")
					case "/compute/plans":
						catalogReads++
						query := req.URL.Query()
						if query.Get("billing_interval") != interval || query.Get("vm_type") != kind || query.Get("site_id") != "site" || query.Get("currency") != "INR" {
							t.Errorf("catalog query does not match VM purchase: %v", query)
							http.Error(w, "no selectable plan for the requested interval", 400)
							return
						}
						computeCatalog(w)
					case "/compute/" + kind + "-vms":
						creates++
						var b struct {
							Catalog computeBillingTerm `json:"billing_catalog"`
						}
						if err := json.NewDecoder(req.Body).Decode(&b); err != nil {
							t.Fatal(err)
						}
						if b.Catalog.BillingInterval != interval || b.Catalog.Committed == nil || *b.Catalog.Committed != (interval == "MONTHLY") {
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
				want := configured
				if want == "" {
					want = "HOURLY"
				}
				d := &computePlansDataSource{}
				d.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
					query := req.URL.Query()
					if req.Method != "GET" || req.URL.Path != "/compute/plans" || query.Get("vm_type") != kind || query.Get("billing_interval") != want || query.Get("site_id") != "site" || query.Get("currency") != "INR" {
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
			t.Error("mutation after denial")
			w.WriteHeader(500)
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
	if got.Name.ValueString() != "portal-change" || got.PlanID.ValueString() != "resized-plan" || got.Cpu.ValueInt64() != 8 || got.SSHKeyIDs.IsNull() || got.PublicIPAction.ValueString() != "release" {
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
func TestComputePlanInvalidCatalogIsRejected(t *testing.T) {
	n := int64(10)
	p := computePlan{PlanID: "p", Code: "SKU", Cpu: 1, RamMb: 512, DiskGb: 10, Selectable: true, PricingStatus: "priced", BillingInterval: "HOURLY", HourlyPriceMinor: &n, BillingCatalog: map[string]any{"sku_code": "SKU"}}
	if err := p.validate("cloud"); err != nil {
		t.Fatal(err)
	}
	p.Selectable = false
	if p.validate("cloud") == nil {
		t.Fatal("unselectable plan accepted")
	}
	p.Selectable = true
	p.HourlyPriceMinor = nil
	if p.validate("cloud") == nil {
		t.Fatal("unknown price accepted as zero")
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
func TestComputeBackupIncreaseAdmission(t *testing.T) {
	old := computeBackupModel()
	same := old
	if backupPolicyIncreases(old, same) {
		t.Fatal("unchanged is increase")
	}
	same.RetentionDays = types.Int64Value(8)
	if !backupPolicyIncreases(old, same) {
		t.Fatal("retention increase missed")
	}
	same = old
	same.Frequency = types.StringValue("hourly")
	if !backupPolicyIncreases(old, same) {
		t.Fatal("frequency increase missed")
	}
	same = old
	same.RetentionDays = types.Int64Value(3)
	if backupPolicyIncreases(old, same) {
		t.Fatal("retention reduction is increase")
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

func TestComputeMalformedVMResponsesPreserveState(t *testing.T) {
	for _, field := range []string{"ssh_key_ids", "tags", "site_id", "plan_id", "template_id", "os_type", "os_distro"} {
		t.Run(field, func(t *testing.T) {
			r := &cloudVmResource{vmType: "cloud"}
			r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
				vm := computeCanonicalVM("cloud")
				delete(vm, field)
				computeJSON(w, vm)
			})
			m := computeValidVMPlan()
			m.ID = types.StringValue("vm-1")
			before := computeState(t, r, m)
			resp := resource.ReadResponse{State: before}
			r.Read(context.Background(), resource.ReadRequest{State: before}, &resp)
			if !resp.Diagnostics.HasError() || !resp.State.Raw.Equal(before.Raw) {
				t.Fatalf("missing %s must not overwrite state: %v", field, resp.Diagnostics)
			}
		})
	}
}
func TestComputeMissingBackupEnabledPreservesState(t *testing.T) {
	r := &vmBackupPolicyResource{vmType: "cloud"}
	r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) { computeJSON(w, map[string]string{"vm_id": "vm-1"}) })
	before := computeState(t, r, computeBackupModel())
	resp := resource.ReadResponse{State: before}
	r.Read(context.Background(), resource.ReadRequest{State: before}, &resp)
	if !resp.Diagnostics.HasError() || !resp.State.Raw.Equal(before.Raw) {
		t.Fatal("missing enabled must preserve policy state")
	}
}
func TestComputeCatalogMalformedAndSiteMismatch(t *testing.T) {
	t.Run("missing array", func(t *testing.T) {
		c := computeTestClient(t, func(w http.ResponseWriter, r *http.Request) { computeJSON(w, map[string]any{}) })
		if _, err := c.listComputePlans(context.Background(), "cloud", ""); err == nil {
			t.Fatal("missing plans accepted")
		}
	})
	t.Run("wrong site", func(t *testing.T) {
		c := computeTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			computeJSON(w, map[string]any{"plans": []any{map[string]any{"plan_id": "p", "site_id": "other"}}})
		})
		if _, err := c.findPlan(context.Background(), "cloud", "site", "p"); err == nil {
			t.Fatal("wrong site accepted")
		}
	})
	t.Run("billing reference", func(t *testing.T) {
		n := int64(10)
		p := computePlan{PlanID: "p", Code: "SKU", Cpu: 1, RamMb: 512, DiskGb: 10, Selectable: true, PricingStatus: "priced", BillingInterval: "HOURLY", HourlyPriceMinor: &n}
		if p.validate("cloud") == nil {
			t.Fatal("missing billing_catalog accepted")
		}
		p.BillingCatalog = map[string]any{"sku_code": "other"}
		if p.validate("cloud") == nil {
			t.Fatal("mismatched billing SKU accepted")
		}
	})
}
func TestComputeSnapshotFailureRetainsRecoverableIdentity(t *testing.T) {
	r := &vmSnapshotResource{vmType: "cloud"}
	r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/billing/resource-eligibility":
			computeEligibility(w, true, "")
		case "/compute/cloud-vms/vm-1/snapshots":
			computeJSON(w, map[string]any{"snapshot_set_id": "snap", "status": "queued"})
		case "/compute/cloud-vm-snapshots/snap":
			computeJSON(w, map[string]any{"snapshot_set_id": "snap", "vm_id": "vm-1", "name": "snapshot", "capture_scope": "root_only", "status": "failed"})
		default:
			t.Error(req.URL.Path)
		}
	})
	m := vmSnapshotModel{ID: types.StringUnknown(), VmID: types.StringValue("vm-1"), Name: types.StringValue("snapshot"), Description: types.StringNull(), Mode: types.StringValue("root_only"), SelectedDataVolumeIDs: types.SetValueMust(types.StringType, []attr.Value{}), Status: types.StringUnknown(), RecoveryPointID: types.StringUnknown()}
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

func TestComputeVMCurrencyUsesOrganizationBeforeQuote(t *testing.T) {
	for _, scenario := range []string{"USD success", "catalog mismatch", "admission mismatch"} {
		t.Run(scenario, func(t *testing.T) {
			r := &cloudVmResource{vmType: "cloud"}
			admissions, creates := 0, 0
			r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
				switch req.URL.Path {
				case "/billing/resource-eligibility":
					admissions++
					var b billingEligibilityRequest
					_ = json.NewDecoder(req.Body).Decode(&b)
					currency := "USD"
					if scenario == "admission mismatch" && b.SKUCode != "" {
						currency = "INR"
					}
					if b.SKUCode != "" && (b.EstimatedCostMinor == nil || *b.EstimatedCostMinor != 150) {
						t.Fatalf("expected USD catalog minor units, got %+v", b)
					}
					computeJSON(w, map[string]any{"allowed": true, "organization_id": "org", "reason": "eligible", "billing_mode": "PREPAID", "billing_state": "CURRENT", "currency": currency, "sku_code": b.SKUCode, "estimated_cost_minor": b.EstimatedCostMinor, "evaluated_at": "2026-09-27T00:00:00Z"})
				case "/compute/plans":
					if admissions != 1 || req.URL.Query().Get("currency") != "USD" {
						t.Error("catalog must follow authoritative billing currency")
					}
					currency := "USD"
					if scenario == "catalog mismatch" {
						currency = "INR"
					}
					computeJSON(w, map[string]any{"plans": []any{map[string]any{"plan_id": "plan", "code": "SKU-1", "cpu": 4, "ram_mb": 8192, "disk_gb": 80, "selectable": true, "pricing_status": "priced", "currency": currency, "billing_interval": "MONTHLY", "monthly_price_minor": 150, "billing_catalog": computeTestBillingCatalog("SKU-1", 150)}}})
				case "/compute/cloud-vms":
					creates++
					computeJSON(w, operationAccepted{VmID: "vm-1", OperationID: "op", Status: "accepted"})
				case "/compute/operations/op":
					computeJSON(w, map[string]any{"status": "succeeded"})
				case "/compute/cloud-vms/vm-1":
					computeJSON(w, computeCanonicalVM("cloud"))
				default:
					t.Error(req.URL.Path)
				}
			})
			resp := resource.CreateResponse{State: computeState(t, r, nil)}
			r.Create(context.Background(), resource.CreateRequest{Plan: computePlanState(t, r, computeValidVMPlan())}, &resp)
			if scenario == "USD success" {
				computeNoErrors(t, resp.Diagnostics)
				if creates != 1 || admissions != 2 {
					t.Fatal("expected currency lookup and fresh purchase check")
				}
			} else if !resp.Diagnostics.HasError() || creates != 0 {
				t.Fatal("currency mismatch must prevent creation")
			}
		})
	}
}
