package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Independent payload assertions: mocks must not simply reuse the implementation
// validator and accidentally bless the same contract mistake.
func computeFixtureCatalog(w http.ResponseWriter, body map[string]any, product, sku string) bool {
	c, ok := body["billing_catalog"].(map[string]any)
	if !ok || c["sku_id"] == nil || c["sku_id"] == "" || c["sku_code"] != sku || c["product_code"] != product || c["currency"] != "INR" || c["site_id"] != "site" {
		http.Error(w, "required canonical billing_catalog missing or incorrect", 422)
		return false
	}
	return true
}

func TestComputeRecoveryCatalogPlanAndReadCompatibility(t *testing.T) {
	ctx := context.Background()
	base := recoveryTestCatalog("snapshot_storage")
	enriched, _ := decodeRecoveryCatalog(base.ValueString())
	enriched["billing_interval"] = "HOURLY"
	raw, _ := json.Marshal(enriched)
	prior := types.StringValue(string(raw))
	for _, scenario := range []string{"omitted", "new metadata", "enriched import", "changed sku", "changed price", "format only"} {
		t.Run(scenario, func(t *testing.T) {
			r, model := recoveryTestResource("cloud", "snapshot_storage", nil)
			old := prior
			cfg := base
			wantReplace := false
			switch scenario {
			case "omitted":
				cfg = types.StringNull()
			case "new metadata":
				old = types.StringNull()
			case "changed sku":
				cfg = types.StringValue(strings.Replace(base.ValueString(), "SNAPSHOT-STD", "SNAPSHOT-OTHER", 1))
				wantReplace = true
			case "changed price":
				cfg = types.StringValue(strings.Replace(base.ValueString(), ":17", ":19", 1))
				wantReplace = true
			case "format only":
				old = base
				cfg = types.StringValue("\n" + base.ValueString() + "\n")
			}
			state := computeState(t, r, recoveryTestSetCatalog(model, old))
			plan := computePlanState(t, r, recoveryTestSetCatalog(model, cfg))
			resp := planmodifier.StringResponse{PlanValue: cfg}
			recoveryCatalogPlan{immutable: true}.PlanModifyString(ctx, planmodifier.StringRequest{State: state, Plan: plan, StateValue: old, ConfigValue: cfg, PlanValue: cfg}, &resp)
			if resp.RequiresReplace != wantReplace {
				t.Fatalf("replacement=%v wanted %v", resp.RequiresReplace, wantReplace)
			}
			if scenario == "omitted" && !resp.PlanValue.Equal(old) {
				t.Fatal("omission changed recorded selection")
			}
		})
	}
	// Enrichment must not rewrite configured bytes; omissions must retain inputs.
	current := base
	if err := hydrateRecoveryCatalog(&current, enriched); err != nil || !current.Equal(base) {
		t.Fatalf("enriched response changed selection: %v %s", err, current)
	}
	if err := hydrateRecoveryCatalog(&current, nil); err != nil || !current.Equal(base) {
		t.Fatal("missing projection discarded existing catalog")
	}
	if recoveryCatalogSelectionUnchanged(prior, types.StringValue(strings.Replace(base.ValueString(), ":17", ":19", 1))) {
		t.Fatal("price change hidden as metadata")
	}
}

func TestComputeRecoveryCatalogRefreshDenialsPreserveState(t *testing.T) {
	for _, kind := range []string{"cloud", "gpu"} {
		for _, product := range []string{"snapshot_storage", "backup_storage", "block_storage"} {
			for _, status := range []int{403, 423} {
				t.Run(fmt.Sprintf("%s/%s/%d", kind, product, status), func(t *testing.T) {
					client := computeTestClient(t, func(w http.ResponseWriter, req *http.Request) { w.WriteHeader(status) })
					r, model := recoveryTestResource(kind, product, client)
					state := computeState(t, r, model)
					resp := resource.ReadResponse{State: state}
					r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
					if !resp.Diagnostics.HasError() || !resp.State.Raw.Equal(state.Raw) {
						t.Fatal("denial discarded state")
					}
				})
			}
		}
	}
}

func TestComputeAttachmentCatalogLookupDoesNotBlockDeleteOrEraseState(t *testing.T) {
	for _, kind := range []string{"cloud", "gpu"} {
		t.Run(kind, func(t *testing.T) {
			attached := true
			lookups, detaches := 0, 0
			client := computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
				switch {
				case strings.HasPrefix(req.URL.Path, "/block-storage/"):
					lookups++
					http.NotFound(w, req)
				case strings.HasSuffix(req.URL.Path, "/actions/detach-volume"):
					attached = false
					detaches++
					computeJSON(w, operationAccepted{OperationID: "detach"})
				case strings.HasPrefix(req.URL.Path, "/compute/operations/"):
					computeJSON(w, map[string]any{"status": "succeeded"})
				default:
					volumes := []any{}
					if attached {
						volumes = append(volumes, map[string]any{"volume_id": "vol-1", "mode": "single-writer"})
					}
					computeJSON(w, map[string]any{"id": "vm-1", "data_volumes": volumes})
				}
			})
			r, model := recoveryTestResource(kind, "block_storage", client)
			model = recoveryTestSetCatalog(model, types.StringNull())
			state := computeState(t, r, model)
			read := resource.ReadResponse{State: state}
			r.Read(context.Background(), resource.ReadRequest{State: state}, &read)
			if !read.Diagnostics.HasError() || !read.State.Raw.Equal(state.Raw) {
				t.Fatal("catalog 404 removed attachment state")
			}
			del := resource.DeleteResponse{}
			r.Delete(context.Background(), resource.DeleteRequest{State: state}, &del)
			computeNoErrors(t, del.Diagnostics)
			if lookups != 1 || detaches != 1 {
				t.Fatalf("delete needed catalog: lookups=%d detaches=%d", lookups, detaches)
			}
		})
	}
}

func TestComputeRecoveryCatalogSchemaValidation(t *testing.T) {
	for _, raw := range []string{`[]`, `{} {}`, `{"sku_id":true,"sku_code":"SKU"}`, `{"sku_id":"id","sku_code":"sku"}`, `{"sku_id":"id","sku_code":"SKU","attached_skus":[]}`, `{"sku_id":"id","sku_code":"SKU","attached_skus":{"root_disk":{"sku_id":"root","sku_code":"ROOT"}}}`, `{"sku_id":"id","sku_code":"SKU","currency":"US"}`, `{"sku_id":"id","sku_code":"SKU","site_id":false}`, `{"sku_id":"id","sku_code":"SKU","skuCode":"OTHER"}`, `{"sku_id":"id","sku_code":"SKU","unit_price_minor":0.5}`} {
		if _, err := parseRecoveryCatalog(types.StringValue(raw), "snapshot_storage"); err == nil {
			t.Errorf("accepted invalid catalog: %s", raw)
		}
	}
	for _, raw := range []string{`{"sku_id":123,"sku_code":"SKU"}`, `{"sku_id":"id","sku_code":"SKU","unit_price_minor":0}`, `{"sku_id":"id","sku_code":"SKU","unit_price_minor":null,"attached_skus":{}}`} {
		if _, err := parseRecoveryCatalog(types.StringValue(raw), "snapshot_storage"); err != nil {
			t.Errorf("rejected backend-supported catalog: %v", err)
		}
	}
}

func recoveryTestCatalog(product string) types.String {
	code := map[string]string{"snapshot_storage": "SNAPSHOT-STD", "backup_storage": "BACKUP-STD", "block_storage": "BLOCK-STD"}[product]
	return types.StringValue(fmt.Sprintf(`{"currency":"INR","product_code":%q,"site_id":"site","sku_code":%q,"sku_id":9007199254740993,"unit_price_minor":17}`, product, code))
}

func recoveryTestResource(kind, product string, client *Client) (resource.Resource, any) {
	catalog := recoveryTestCatalog(product)
	switch product {
	case "snapshot_storage":
		return &vmSnapshotResource{vmType: kind, client: client}, vmSnapshotModel{ID: types.StringUnknown(), VmID: types.StringValue("vm-1"), Name: types.StringValue("snapshot"), Mode: types.StringValue("root_only"), SelectedDataVolumeIDs: types.SetValueMust(types.StringType, []attr.Value{}), Status: types.StringUnknown(), RecoveryPointID: types.StringUnknown(), BillingCatalog: catalog}
	case "backup_storage":
		m := computeBackupModel()
		m.ID = types.StringUnknown()
		m.PolicyID = types.StringUnknown()
		m.Hour = types.Int64Value(12)
		m.BillingCatalog = catalog
		return &vmBackupPolicyResource{vmType: kind, client: client}, m
	default:
		return &vmVolumeAttachmentResource{vmType: kind, client: client}, vmVolumeAttachmentModel{ID: types.StringUnknown(), VmID: types.StringValue("vm-1"), VolumeID: types.StringValue("vol-1"), Mode: types.StringValue("single-writer"), ConfirmUnmounted: types.BoolValue(true), GuestDevice: types.StringUnknown(), MountInstructions: types.StringUnknown(), BillingCatalog: catalog}
	}
}

func recoveryTestSetCatalog(model any, catalog types.String) any {
	switch m := model.(type) {
	case vmSnapshotModel:
		m.BillingCatalog = catalog
		return m
	case vmBackupPolicyModel:
		m.BillingCatalog = catalog
		return m
	case vmVolumeAttachmentModel:
		m.BillingCatalog = catalog
		return m
	}
	panic("unexpected resource model")
}

func TestComputeRecoveryCatalogContractLifecycle(t *testing.T) {
	for _, kind := range []string{"cloud", "gpu"} {
		for _, product := range []string{"snapshot_storage", "backup_storage", "block_storage"} {
			t.Run(kind+"/"+product, func(t *testing.T) {
				ctx := context.Background()
				catalog, _ := decodeRecoveryCatalog(recoveryTestCatalog(product).ValueString())
				attached, created, deleted := false, 0, 0
				vmPath := "/compute/" + kind + "-vms/vm-1"
				policy := computeBackupResponse(true)
				policy["schedule"].(map[string]any)["hour"] = 12
				policy["billing_catalog"] = catalog
				snapshot := map[string]any{"snapshot_set_id": "snap", "vm_id": "vm-1", "name": "snapshot", "capture_scope": "root_only", "status": "succeeded", "recovery_point_id": "rp", "billing_catalog": catalog}
				client := computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
					var body map[string]any
					if req.Method != "GET" && req.Method != "DELETE" {
						dec := json.NewDecoder(req.Body)
						dec.UseNumber()
						if err := dec.Decode(&body); err != nil {
							t.Error(err)
							http.Error(w, "bad body", 400)
							return
						}
					}
					switch {
					case req.URL.Path == "/billing/resource-eligibility":
						if body["sku_code"] != catalog["sku_code"] || body["estimated_cost_minor"] != nil {
							t.Error("wrong SKU or invented price estimate", body)
						}
						computeEligibility(w, true, catalog["sku_code"].(string))
					case req.URL.Path == vmPath && req.Method == "GET":
						volumes := []any{}
						if attached {
							volumes = append(volumes, map[string]any{"volume_id": "vol-1", "mode": "single-writer"})
						}
						computeJSON(w, map[string]any{"id": "vm-1", "site_id": "site", "data_volumes": volumes})
					case req.URL.Path == "/block-storage/volumes/vol-1":
						computeJSON(w, map[string]any{"id": "vol-1", "site_id": "site", "metadata": map[string]any{"billing_catalog": catalog}})
					case req.URL.Path == vmPath+"/snapshots" || req.URL.Path == vmPath+"/backups/enable" || req.URL.Path == vmPath+"/actions/attach-volume":
						if !computeFixtureCatalog(w, body, product, catalog["sku_code"].(string)) {
							t.Error("invalid purchase payload")
							return
						}
						if !reflect.DeepEqual(body["billing_catalog"], catalog) {
							t.Error("catalog fields changed or numeric ID lost precision", body["billing_catalog"])
						}
						created++
						switch product {
						case "snapshot_storage":
							computeJSON(w, snapshot)
						case "backup_storage":
							schedule := body["schedule"].(map[string]any)
							if schedule["frequency"] != "daily" || schedule["hour"] != json.Number("12") || schedule["day_of_week"] != nil {
								t.Error("schedule diverged from SDK", schedule)
							}
							computeJSON(w, policy)
						default:
							attached = true
							computeJSON(w, operationAccepted{OperationID: "attach"})
						}
					case strings.HasPrefix(req.URL.Path, "/compute/operations/"):
						computeJSON(w, map[string]any{"status": "succeeded"})
					case req.URL.Path == "/compute/"+kind+"-vm-snapshots/snap":
						if req.Method == "DELETE" {
							deleted++
							computeJSON(w, map[string]any{"snapshot_set_id": "snap", "status": "deleted"})
						} else {
							computeJSON(w, snapshot)
						}
					case req.URL.Path == vmPath+"/backups/policy":
						computeJSON(w, policy)
					case req.URL.Path == vmPath+"/backups/disable":
						deleted++
						computeJSON(w, computeBackupResponse(false))
					case req.URL.Path == vmPath+"/actions/detach-volume":
						deleted++
						attached = false
						computeJSON(w, operationAccepted{OperationID: "detach"})
					default:
						t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
						http.Error(w, "unexpected", 500)
					}
				})
				r, model := recoveryTestResource(kind, product, client)
				create := resource.CreateResponse{State: computeState(t, r, nil)}
				r.Create(ctx, resource.CreateRequest{Plan: computePlanState(t, r, model)}, &create)
				computeNoErrors(t, create.Diagnostics)
				read := resource.ReadResponse{State: create.State}
				r.Read(ctx, resource.ReadRequest{State: create.State}, &read)
				computeNoErrors(t, read.Diagnostics)
				if !read.State.Raw.Equal(create.State.Raw) {
					t.Error("refresh changed stable state")
				}
				id := map[string]string{"snapshot_storage": "snap", "backup_storage": "vm-1", "block_storage": "vm-1/vol-1"}[product]
				imp := resource.ImportStateResponse{State: computeState(t, r, nil)}
				r.(resource.ResourceWithImportState).ImportState(ctx, resource.ImportStateRequest{ID: id}, &imp)
				computeNoErrors(t, imp.Diagnostics)
				read = resource.ReadResponse{State: imp.State}
				r.Read(ctx, resource.ReadRequest{State: imp.State}, &read)
				computeNoErrors(t, read.Diagnostics)
				var got types.String
				computeNoErrors(t, read.State.GetAttribute(ctx, path.Root("billing_catalog"), &got))
				if !recoveryCatalogEqual(got, recoveryTestCatalog(product)) {
					t.Errorf("import changed canonical catalog: %s", got)
				}
				del := resource.DeleteResponse{}
				r.Delete(ctx, resource.DeleteRequest{State: create.State}, &del)
				computeNoErrors(t, del.Diagnostics)
				if created != 1 || deleted != 1 {
					t.Errorf("creates=%d deletes=%d", created, deleted)
				}
			})
		}
	}
}

func TestComputeRecoveryCatalogRejectsBeforeMutation(t *testing.T) {
	for _, kind := range []string{"cloud", "gpu"} {
		for _, product := range []string{"snapshot_storage", "backup_storage", "block_storage"} {
			for _, scenario := range []string{"missing", "empty", "invalid json", "missing sku", "wrong product", "root disk", "bad currency", "negative price", "wrong site", "currency mismatch", "denied"} {
				t.Run(kind+"/"+product+"/"+scenario, func(t *testing.T) {
					value := recoveryTestCatalog(product)
					switch scenario {
					case "missing":
						value = types.StringNull()
					case "empty":
						value = types.StringValue(`{}`)
					case "invalid json":
						value = types.StringValue(`null`)
					case "missing sku":
						value = types.StringValue(`{"sku_code":"BACKUP-STD"}`)
					case "wrong product":
						value = types.StringValue(strings.Replace(value.ValueString(), product, "vm", 1))
					case "root disk":
						value = types.StringValue(`{"sku_id":"id","sku_code":"ROOTDISK-STD"}`)
					case "bad currency":
						value = types.StringValue(strings.Replace(value.ValueString(), "INR", "inr", 1))
					case "negative price":
						value = types.StringValue(strings.Replace(value.ValueString(), ":17", ":-1", 1))
					case "wrong site":
						value = types.StringValue(strings.Replace(value.ValueString(), `"site"`, `"other"`, 1))
					case "currency mismatch":
						value = types.StringValue(strings.Replace(value.ValueString(), "INR", "USD", 1))
					}
					mutations := 0
					client := computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
						if req.URL.Path == "/billing/resource-eligibility" {
							var b map[string]any
							_ = json.NewDecoder(req.Body).Decode(&b)
							computeEligibility(w, scenario != "denied", b["sku_code"].(string))
							return
						}
						if req.Method != "GET" {
							mutations++
							w.WriteHeader(http.StatusPaymentRequired)
							computeJSON(w, map[string]any{"error": "billing_denied", "billing_reason": "insufficient_balance"})
							return
						}
						if strings.HasPrefix(req.URL.Path, "/block-storage/") {
							computeJSON(w, map[string]any{"id": "vol-1", "site_id": "site"})
							return
						}
						computeJSON(w, map[string]any{"id": "vm-1", "site_id": "site", "data_volumes": []any{}})
					})
					r, model := recoveryTestResource(kind, product, client)
					model = recoveryTestSetCatalog(model, value)
					resp := resource.CreateResponse{State: computeState(t, r, nil)}
					r.Create(context.Background(), resource.CreateRequest{Plan: computePlanState(t, r, model)}, &resp)
					wantMutations := 0
					if scenario == "currency mismatch" || scenario == "denied" {
						wantMutations = 1
					}
					if !resp.Diagnostics.HasError() || mutations != wantMutations {
						t.Fatalf("invalid catalog accepted: mutations=%d diagnostics=%v", mutations, resp.Diagnostics)
					}
				})
			}
		}
	}
}
