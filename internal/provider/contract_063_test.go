package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// Tests for the behaviour public API contract 0.7.1 (0.6.3 plus the backups/disable body) asks of the provider.

// dataSourceRead runs a data source with the given configuration values.
func dataSourceRead(t *testing.T, d datasource.DataSource, values map[string]attr.Value) datasource.ReadResponse {
	t.Helper()
	var sch datasource.SchemaResponse
	d.Schema(context.Background(), datasource.SchemaRequest{}, &sch)
	ts, all := map[string]attr.Type{}, map[string]attr.Value{}
	for name, attribute := range sch.Schema.Attributes {
		ts[name] = attribute.GetType()
		all[name], _ = networkValue(attribute.GetType(), nil)
	}
	for name, value := range values {
		all[name] = value
	}
	raw, err := types.ObjectValueMust(ts, all).ToTerraformValue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	resp := datasource.ReadResponse{State: tfsdk.State{Schema: sch.Schema, Raw: raw}}
	d.Read(context.Background(), datasource.ReadRequest{Config: tfsdk.Config{Schema: sch.Schema, Raw: raw}}, &resp)
	return resp
}

func TestVmCreateSendsTheWholeBillingOption(t *testing.T) {
	for _, interval := range []string{"HOURLY", "MONTHLY"} {
		t.Run(interval, func(t *testing.T) {
			var sent map[string]any
			r := &cloudVmResource{vmType: "cloud"}
			r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
				switch req.Method + " " + req.URL.Path {
				case "GET /compute/plans":
					catalog := computeTestBillingCatalog("SKU-1", 20)
					catalog["price_currency"] = "INR"
					catalog["billing_options"].([]any)[1].(map[string]any)["discount_percent"] = 19.5
					computeJSON(w, map[string]any{"plans": []any{map[string]any{"plan_id": "plan", "cpu": 1, "ram_mb": 2048, "disk_gb": 50, "billing_catalog": catalog}}})
				case "POST /compute/cloud-vms":
					sent = decodeBody(t, req)
					computeJSON(w, operationAccepted{VmID: "vm-1", OperationID: "op-1"})
				case "GET /compute/operations/op-1":
					computeJSON(w, map[string]any{"status": "succeeded"})
				case "GET /compute/cloud-vms/vm-1":
					computeJSON(w, computeCanonicalVM("cloud"))
				default:
					t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
				}
			})
			plan := computeValidVMPlan()
			plan.BillingInterval = types.StringValue(interval)
			resp := resource.CreateResponse{State: computeState(t, r, nil)}
			r.Create(context.Background(), resource.CreateRequest{Plan: computePlanState(t, r, plan)}, &resp)
			computeNoErrors(t, resp.Diagnostics)
			catalog := sent["billing_catalog"].(map[string]any)
			if catalog["price_currency"] != "INR" || catalog["sku_code"] != "SKU-1" || catalog["billing_interval"] != interval || catalog["commitment_period"] != interval {
				t.Fatalf("selection is not the plan's catalog with the %s entry merged in: %v", interval, catalog)
			}
			if interval == "MONTHLY" && (catalog["commitment_months"] != json.Number("1") || catalog["committed_hours"] != json.Number("731") || catalog["price_unit"] != "HOUR" || catalog["discount_percent"] != json.Number("19.5")) {
				t.Fatalf("monthly selection lost fields of the entry: %v", catalog)
			}
			if interval == "HOURLY" && (catalog["committed"] != false || catalog["commitment_months"] != nil || catalog["price_unit"] != nil) {
				t.Fatalf("hourly selection carries another entry's fields: %v", catalog)
			}
			if sent["cpu"] != json.Number("1") || sent["ram_mb"] != json.Number("2048") || sent["disk_gb"] != json.Number("50") {
				t.Fatalf("create must send the plan's shape: %v", sent)
			}
		})
	}
}

func TestVmBillingIntervalDefaultsToHourlyForNewVMsOnly(t *testing.T) {
	r := &cloudVmResource{vmType: "cloud"}
	var sch resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &sch)
	objectType := sch.Schema.Type().TerraformType(context.Background())
	modify := func(state *tfsdk.State, stateValue types.String) types.String {
		req := planmodifier.StringRequest{Path: path.Root("billing_interval"), ConfigValue: types.StringNull(), PlanValue: types.StringUnknown(), StateValue: stateValue, Plan: tfsdk.Plan{Schema: sch.Schema, Raw: tftypes.NewValue(objectType, tftypes.UnknownValue)}, State: *state}
		resp := planmodifier.StringResponse{PlanValue: req.PlanValue}
		billingIntervalDefault().PlanModifyString(context.Background(), req, &resp)
		return resp.PlanValue
	}
	fresh := tfsdk.State{Schema: sch.Schema, Raw: tftypes.NewValue(objectType, nil)}
	if got := modify(&fresh, types.StringNull()); got.ValueString() != "HOURLY" {
		t.Fatalf("a new VM must default to HOURLY, got %s", got)
	}
	existing := computeState(t, r, computeValidVMPlanKnown("MONTHLY"))
	if got := modify(&existing, types.StringValue("MONTHLY")); got.ValueString() != "MONTHLY" {
		t.Fatalf("an existing VM must keep its recorded term, got %s", got)
	}
}

func computeValidVMPlanKnown(interval string) cloudVmModel {
	m := computeValidVMPlan()
	m.ID, m.BillingInterval = types.StringValue("vm-1"), types.StringValue(interval)
	m.Cpu, m.RamMb, m.DiskGb, m.GpuCount = types.Int64Value(1), types.Int64Value(2048), types.Int64Value(50), types.Int64Value(0)
	m.GpuModel, m.Status, m.PublicIP, m.PrivateIP = types.StringNull(), types.StringValue("running"), types.StringNull(), types.StringNull()
	return m
}

func TestBackupPolicyUpdateAndDisableBodies(t *testing.T) {
	var requests []string
	var patched map[string]any
	disableBody := "unset"
	r := &vmBackupPolicyResource{vmType: "gpu"}
	r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		requests = append(requests, req.Method+" "+req.URL.Path)
		switch req.Method + " " + req.URL.Path {
		case "PATCH /compute/gpu-vms/vm-1/backups/policy":
			patched = decodeBody(t, req)
			computeJSON(w, computeBackupResponse(true))
		case "GET /compute/gpu-vms/vm-1/backups/policy":
			policy := computeBackupResponse(true)
			policy["retention_days"] = 14
			computeJSON(w, policy)
		case "POST /compute/gpu-vms/vm-1/backups/disable":
			raw, _ := io.ReadAll(req.Body)
			disableBody = string(raw)
			computeJSON(w, computeBackupResponse(false))
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
		}
	})
	state := computeBackupModel()
	state.BillingCatalog = types.StringNull()
	plan := state
	plan.RetentionDays = types.Int64Value(14)
	update := resource.UpdateResponse{State: computeState(t, r, state)}
	r.Update(context.Background(), resource.UpdateRequest{Plan: computePlanState(t, r, plan), State: computeState(t, r, state)}, &update)
	computeNoErrors(t, update.Diagnostics)
	if patched["retention_days"] != json.Number("14") || patched["billing_catalog"] != nil || patched["requested_by"] != nil {
		t.Fatalf("policy update body: %v", patched)
	}
	del := resource.DeleteResponse{}
	r.Delete(context.Background(), resource.DeleteRequest{State: update.State}, &del)
	computeNoErrors(t, del.Diagnostics)
	if disableBody != "{}" {
		t.Fatalf("backups/disable sends an empty JSON object (contract 0.7.1), got %q", disableBody)
	}
}

func TestImagesDataSourceQuery(t *testing.T) {
	d := &imagesDataSource{}
	d.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/compute/images" || req.URL.Query().Get("vm_type") != "gpu" || req.URL.Query().Get("site_id") != "site-1" {
			t.Errorf("unexpected request %s", req.URL)
		}
		computeJSON(w, map[string]any{"images": []any{map[string]any{"template_id": "tmpl", "name": "Ubuntu", "os_distro": "ubuntu", "os_type": "linux"}}})
	})
	resp := dataSourceRead(t, d, map[string]attr.Value{"vm_type": types.StringValue("gpu"), "site_id": types.StringValue("site-1")})
	computeNoErrors(t, resp.Diagnostics)
	var got imagesModel
	computeNoErrors(t, resp.State.Get(context.Background(), &got))
	if len(got.Images) != 1 || got.Images[0].TemplateID.ValueString() != "tmpl" {
		t.Fatal(got)
	}
}

func TestStoragePlanDataSources(t *testing.T) {
	for _, product := range []string{"snapshot", "backup"} {
		t.Run(product, func(t *testing.T) {
			var d datasource.DataSource = NewSnapshotStoragePlansDataSource()
			if product == "backup" {
				d = NewBackupStoragePlansDataSource()
			}
			sku := map[string]string{"snapshot": "SNAPSHOT-STD", "backup": "BACKUP-STD"}[product]
			d.(*recoveryStoragePlansDataSource).client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path != "/compute/plans/"+product+"-storage" || req.URL.Query().Get("billing_interval") != "MONTHLY" || req.URL.Query().Has("currency") {
					t.Errorf("unexpected request %s", req.URL)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"items":[{"sku_id":"019f31ad-00bf","sku_code":"` + sku + `","name":"Storage","selectable":true,"pricing_status":"priced","billing_catalog":{"sku_id":"019f31ad-00bf","sku_code":"` + sku + `","plan_version":117,"unit_price_minor":450,"price_currency":"INR","billing_options":[{"billing_interval":"HOURLY","committed":false}]}}],"source":"billing_v2","product_code":"` + product + `_storage","plan_id":"plan_default","plan_version":117,"billing_interval":"MONTHLY","currency":"INR"}`))
			})
			resp := dataSourceRead(t, d, map[string]attr.Value{"billing_interval": types.StringValue("MONTHLY")})
			computeNoErrors(t, resp.Diagnostics)
			var got recoveryStoragePlansModel
			computeNoErrors(t, resp.State.Get(context.Background(), &got))
			if len(got.Plans) != 1 || got.Plans[0].SkuCode.ValueString() != sku || !got.Plans[0].Selectable.ValueBool() {
				t.Fatal(got)
			}
			catalog, err := decodeRecoveryCatalog(got.Plans[0].BillingCatalog.ValueString())
			if err != nil || catalog["plan_version"] != json.Number("117") || catalog["sku_code"] != sku || catalog["billing_options"] == nil {
				t.Fatalf("billing_catalog must be the API's object unchanged: %s", got.Plans[0].BillingCatalog)
			}
		})
	}
	t.Run("block", func(t *testing.T) {
		d := &blockStoragePlansDataSource{}
		d.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
			if req.URL.Path != "/block-storage/plans" || req.URL.Query().Get("site_id") != "site-1" {
				t.Errorf("unexpected request %s", req.URL)
			}
			w.Header().Set("Content-Type", "application/json")
			// iops and throughput are display text (contract 0.6.3).
			_, _ = w.Write([]byte(`{"items":[{"id":"p1","sku_code":"BLOCKSTO-NVME","plan_code":"BLOCKSTO-NVME","name":"NVMe SSD","storage_type":"nvme","status":"ACTIVE","selectable":true,"included_usage":{"min_size_gb":10,"max_size_gb":2000,"price_per_gb_month_minor":700,"iops":"Up to 10K","throughput":"Up to 400 MiB/s","allowed_sizes_gb":[10,50,100]},"billing_catalog":{"sku_code":"BLOCKSTO-NVME"}}],"currency":"INR","billing_interval":"MONTHLY"}`))
		})
		resp := dataSourceRead(t, d, map[string]attr.Value{"site_id": types.StringValue("site-1")})
		computeNoErrors(t, resp.Diagnostics)
		var got blockStoragePlansModel
		computeNoErrors(t, resp.State.Get(context.Background(), &got))
		if len(got.Plans) != 1 {
			t.Fatal(got)
		}
		p := got.Plans[0]
		var sizes []int64
		computeNoErrors(t, p.AllowedSizesGb.ElementsAs(context.Background(), &sizes, false))
		if p.SkuCode.ValueString() != "BLOCKSTO-NVME" || p.MinSizeGb.ValueInt64() != 10 || p.MaxSizeGb.ValueInt64() != 2000 || p.PricePerGbMonthMinor.ValueInt64() != 700 || p.Iops.ValueString() != "Up to 10K" || p.Throughput.ValueString() != "Up to 400 MiB/s" || !reflect.DeepEqual(sizes, []int64{10, 50, 100}) || !strings.Contains(p.IncludedUsage.ValueString(), `"iops":"Up to 10K"`) || p.BillingCatalog.ValueString() != `{"sku_code":"BLOCKSTO-NVME"}` {
			t.Fatalf("unexpected plan: %+v", p)
		}
	})
}

func TestBucketDefaultRetention(t *testing.T) {
	var created, lockPut map[string]any
	rule := map[string]any{"mode": "GOVERNANCE", "days": 1, "years": nil}
	r := &bucketResource{client: storageTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		switch req.Method + " " + req.URL.Path {
		case "POST /object-storage/buckets":
			created = decodeBody(t, req)
			_, _ = w.Write([]byte(`{"name":"locked","region":"in-south-2","is_public":false,"bucket_lock_enabled":true,"status":"active"}`))
		case "GET /object-storage/buckets/locked":
			_, _ = w.Write([]byte(`{"name":"locked","region":"in-south-2","is_public":false,"bucket_lock_enabled":true,"status":"active","object_count":0,"total_size":0}`))
		case "GET /object-storage/buckets/locked/object-lock":
			_ = json.NewEncoder(w).Encode(map[string]any{"object_lock_enabled": "Enabled", "rule": rule})
		case "PUT /object-storage/buckets/locked/object-lock":
			lockPut = decodeBody(t, req)
			rule = map[string]any{"mode": "GOVERNANCE", "days": 2, "years": nil}
			_, _ = w.Write([]byte(`{"detail":"Object Lock configuration set successfully"}`))
		case "DELETE /object-storage/buckets/locked":
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"detail":"Bucket 'locked' has Object Lock (WORM) enabled and cannot be deleted."}`))
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
		}
	})}
	retention := func(days int64) types.Object {
		return types.ObjectValueMust(bucketRetentionTypes, map[string]attr.Value{"mode": types.StringValue("GOVERNANCE"), "days": types.Int64Value(days), "years": types.Int64Null()})
	}
	plan := bucketResourceModel{ID: types.StringUnknown(), Name: types.StringValue("locked"), Region: types.StringValue("in-south-2"), IsPublic: types.BoolUnknown(), ObjectLockEnabled: types.BoolValue(true), DefaultRetention: retention(1), Status: types.StringUnknown(), Plan: types.StringUnknown(), SiteID: types.StringUnknown(), ObjectCount: types.Int64Unknown(), TotalSize: types.Int64Unknown()}
	create := resource.CreateResponse{State: storageTestState(t, r, plan)}
	r.Create(storageTestContext, resource.CreateRequest{Plan: storageTestPlan(storageTestState(t, r, plan))}, &create)
	storageTestCheck(t, create.Diagnostics)
	if created["object_lock_enabled"] != true || !reflect.DeepEqual(created["default_retention"], map[string]any{"mode": "GOVERNANCE", "days": json.Number("1")}) || created["is_public"] != nil {
		t.Fatalf("create body: %v", created)
	}
	var state bucketResourceModel
	storageTestCheck(t, create.State.Get(storageTestContext, &state))
	if !state.DefaultRetention.Equal(retention(1)) || state.IsPublic.ValueBool() {
		t.Fatalf("created state: %+v", state)
	}
	next := state
	next.DefaultRetention = retention(2)
	update := resource.UpdateResponse{State: create.State}
	r.Update(storageTestContext, resource.UpdateRequest{Plan: storageTestPlan(storageTestState(t, r, next)), State: create.State}, &update)
	storageTestCheck(t, update.Diagnostics)
	if !reflect.DeepEqual(lockPut, map[string]any{"rule": map[string]any{"mode": "GOVERNANCE", "days": json.Number("2")}}) {
		t.Fatalf("object-lock body: %v", lockPut)
	}
	del := resource.DeleteResponse{State: update.State}
	r.Delete(storageTestContext, resource.DeleteRequest{State: update.State}, &del)
	if !del.Diagnostics.HasError() || !strings.Contains(del.Diagnostics[0].Detail(), "Object Lock (WORM)") {
		t.Fatalf("the API's refusal must be relayed: %v", del.Diagnostics)
	}
}

func TestReservedIPReadAndLabelUpdate(t *testing.T) {
	var patched map[string]any
	r := NewReservedIPResource().(*reservedIPResource)
	r.client = networkTestClient(func(w http.ResponseWriter, req *http.Request) {
		switch req.Method + " " + req.URL.Path {
		case "GET /networking/reserved-ips/ip-1":
			_ = json.NewEncoder(w).Encode(map[string]any{"public_ip_id": "ip-1", "site_id": "site", "label": "web", "address": "203.0.113.5", "status": "reserved"})
		case "PATCH /networking/reserved-ips/ip-1":
			_ = json.NewDecoder(req.Body).Decode(&patched)
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
		}
	})
	s := networkTestSchema(r)
	v := networkTestValues(t, r.networkResource, map[string]any{"id": "ip-1", "site_id": "site", "label": "old"})
	state := networkTestState(t, s, types.ObjectValueMust(r.attributeTypes(), v))
	read := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &read)
	computeNoErrors(t, read.Diagnostics)
	var label types.String
	computeNoErrors(t, read.State.GetAttribute(context.Background(), path.Root("label"), &label))
	if label.ValueString() != "web" {
		t.Fatalf("label = %s", label)
	}
	v["label"] = types.StringValue("api")
	update := resource.UpdateResponse{State: read.State}
	r.Update(context.Background(), resource.UpdateRequest{Plan: networkTestPlan(t, s, types.ObjectValueMust(r.attributeTypes(), v)), State: read.State}, &update)
	computeNoErrors(t, update.Diagnostics)
	if patched["label"] != "api" {
		t.Fatalf("update body: %v", patched)
	}
}

func TestCDNPurgeAndVerifyActions(t *testing.T) {
	var purged map[string]any
	a := &cdnAction{}
	a.client = networkTestClient(func(w http.ResponseWriter, req *http.Request) {
		switch req.Method + " " + req.URL.Path {
		case "POST /cdn/distributions/dist-1/purge":
			_ = json.NewDecoder(req.Body).Decode(&purged)
			_, _ = w.Write([]byte(`{"success":true,"mode":"url"}`))
		case "POST /cdn/distributions/dist-1/custom-domains/cdn.example.com/verify":
			_, _ = w.Write([]byte(`{"domain":"cdn.example.com","status":"pending_validation"}`))
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
		}
	})
	resp, _ := invokeAction(t, a, map[string]attr.Value{"distribution_id": types.StringValue("dist-1"), "mode": types.StringValue("url"), "paths": types.ListValueMust(types.StringType, []attr.Value{types.StringValue("/index.html")})})
	computeNoErrors(t, resp.Diagnostics)
	if !reflect.DeepEqual(purged, map[string]any{"mode": "url", "paths": []any{"/index.html"}}) {
		t.Fatalf("purge body: %v", purged)
	}
	verify := &cdnAction{verify: true, client: a.client}
	resp, progress := invokeAction(t, verify, map[string]attr.Value{"distribution_id": types.StringValue("dist-1"), "domain": types.StringValue("cdn.example.com")})
	computeNoErrors(t, resp.Diagnostics)
	if len(progress) != 1 || !strings.Contains(progress[0], "pending_validation") {
		t.Fatalf("verify must report the API's status: %v", progress)
	}
}

func TestS3CredentialPermissionTypeListsContractValues(t *testing.T) {
	var sch resource.SchemaResponse
	(&s3CredentialResource{}).Schema(context.Background(), resource.SchemaRequest{}, &sch)
	validators := sch.Schema.Attributes["permission_type"].(interface {
		StringValidators() []validator.String
	}).StringValidators()
	for value, valid := range map[string]bool{"admin_rw": true, "admin_ro": true, "object_rw": true, "object_ro": true, "read_only": false} {
		resp := validator.StringResponse{}
		validators[0].ValidateString(context.Background(), validator.StringRequest{Path: path.Root("permission_type"), ConfigValue: types.StringValue(value)}, &resp)
		if resp.Diagnostics.HasError() == valid {
			t.Errorf("%s: valid=%v, diagnostics=%v", value, valid, resp.Diagnostics)
		}
	}
}
