package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestComputeBackupSchedulePlanning(t *testing.T) {
	for _, kind := range []string{"cloud", "gpu"} {
		for _, scenario := range []string{"new defaults", "new weekly", "weekly missing day", "daily with day", "omitted frequency with day", "new hourly", "legacy 20", "legacy hourly", "explicit legacy hourly", "legacy weekly", "changed hourly hour", "daily to hourly", "hourly to daily", "weekly to daily", "explicit hour", "unknown frequency", "new VM"} {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				ctx := context.Background()
				r := &vmBackupPolicyResource{vmType: kind}
				config := vmBackupPolicyModel{VmID: types.StringValue("vm-1")}
				old := computeBackupModel()
				newPolicy, wantError := false, false
				frequency, hour := "daily", int64(20)
				switch scenario {
				case "new defaults":
					newPolicy = true
					hour = 12
				case "new weekly":
					newPolicy = true
					frequency = "weekly"
					hour = 12
					config.Frequency = types.StringValue("weekly")
					config.DayOfWeek = types.Int64Value(6)
				case "weekly missing day":
					newPolicy = true
					config.Frequency = types.StringValue("weekly")
					wantError = true
				case "daily with day":
					newPolicy = true
					config.Frequency = types.StringValue("daily")
					config.DayOfWeek = types.Int64Value(1)
					wantError = true
				case "omitted frequency with day":
					newPolicy = true
					config.DayOfWeek = types.Int64Value(1)
					wantError = true
				case "new hourly":
					newPolicy = true
					config.Frequency = types.StringValue("hourly")
					wantError = true
				case "legacy hourly":
					old.Frequency = types.StringValue("hourly")
					frequency = "hourly"
				case "explicit legacy hourly":
					old.Frequency = types.StringValue("hourly")
					config.Frequency = types.StringValue("hourly")
					frequency = "hourly"
				case "legacy weekly":
					old.Frequency = types.StringValue("weekly")
					old.DayOfWeek = types.Int64Value(3)
					frequency = "weekly"
				case "changed hourly hour":
					old.Frequency = types.StringValue("hourly")
					config.Hour = types.Int64Value(12)
					wantError = true
				case "daily to hourly":
					config.Frequency = types.StringValue("hourly")
					wantError = true
				case "hourly to daily":
					old.Frequency = types.StringValue("hourly")
					config.Frequency = types.StringValue("daily")
				case "weekly to daily":
					old.Frequency = types.StringValue("weekly")
					old.DayOfWeek = types.Int64Value(6)
					config.Frequency = types.StringValue("daily")
				case "explicit hour":
					config.Hour = types.Int64Value(12)
					hour = 12
				case "unknown frequency":
					config.Frequency = types.StringUnknown()
				case "new VM":
					config.VmID = types.StringValue("different-vm")
					hour = 12
				}
				var prior any = old
				if newPolicy {
					prior = nil
				}
				cfg := computeState(t, r, config)
				plan := computePlanState(t, r, config)
				resp := resource.ModifyPlanResponse{Plan: plan}
				r.ModifyPlan(ctx, resource.ModifyPlanRequest{Config: tfsdk.Config{Raw: cfg.Raw, Schema: cfg.Schema}, State: computeState(t, r, prior), Plan: plan}, &resp)
				if resp.Diagnostics.HasError() != wantError {
					t.Fatalf("diagnostics: %v", resp.Diagnostics)
				}
				if wantError {
					return
				}
				var got vmBackupPolicyModel
				computeNoErrors(t, resp.Plan.Get(ctx, &got))
				if scenario == "unknown frequency" {
					if !got.Frequency.IsUnknown() || !got.DayOfWeek.IsUnknown() {
						t.Fatal("unknown inputs were defaulted")
					}
					return
				}
				if got.Frequency.ValueString() != frequency || got.Hour.ValueInt64() != hour || got.Timezone.ValueString() != "UTC" {
					t.Fatalf("incorrect planned schedule: %+v", got)
				}
				if frequency != "weekly" && !got.DayOfWeek.IsNull() {
					t.Fatal("daily retained day_of_week")
				}
				if scenario == "legacy weekly" && got.DayOfWeek.ValueInt64() != 3 {
					t.Fatal("weekly import lost its day")
				}
			})
		}
	}
}

func TestComputeBackupScheduleUpdateCompatibility(t *testing.T) {
	for _, kind := range []string{"cloud", "gpu"} {
		for _, scenario := range []string{"hourly retention", "hourly schedule change", "explicit daily migration", "explicit catalog change", "unchanged catalog"} {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				old := computeBackupModel()
				old.Frequency = types.StringValue("hourly")
				old.BillingCatalog = recoveryTestCatalog("backup_storage")
				next := old
				next.RetentionDays = types.Int64Value(3)
				switch scenario {
				case "hourly schedule change":
					next.Hour = types.Int64Value(12)
				case "explicit daily migration":
					next.Frequency = types.StringValue("daily")
					next.Hour = types.Int64Value(12)
				case "explicit catalog change":
					next.BillingCatalog = types.StringValue(strings.Replace(old.BillingCatalog.ValueString(), ":17", ":19", 1))
				}
				patches, admissions := 0, 0
				var patch map[string]any
				r := &vmBackupPolicyResource{vmType: kind}
				r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
					switch {
					case req.URL.Path == "/billing/resource-eligibility":
						admissions++
						computeEligibility(w, true, "BACKUP-STD")
					case req.Method == "PATCH":
						patches++
						_ = json.NewDecoder(req.Body).Decode(&patch)
						if scenario == "explicit daily migration" {
							s := patch["schedule"].(map[string]any)
							if s["frequency"] != "daily" || s["hour"] != float64(12) || s["day_of_week"] != nil {
								t.Error(s)
							}
						} else if patch["schedule"] != nil {
							t.Error("unchanged legacy schedule must not be sent", patch)
						}
						if (patch["billing_catalog"] != nil) != (scenario == "explicit catalog change") {
							t.Error("catalog replaced without explicit change", patch)
						}
						computeJSON(w, map[string]any{})
					case strings.HasSuffix(req.URL.Path, "/policy"):
						p := computeBackupResponse(true)
						p["schedule"] = next.body()["schedule"]
						p["retention_days"] = 3
						catalog, _ := decodeRecoveryCatalog(next.BillingCatalog.ValueString())
						p["billing_catalog"] = catalog
						computeJSON(w, p)
					default:
						computeJSON(w, map[string]any{"id": "vm-1", "site_id": "site"})
					}
				})
				state := computeState(t, r, old)
				resp := resource.UpdateResponse{State: state}
				r.Update(context.Background(), resource.UpdateRequest{State: state, Plan: computePlanState(t, r, next)}, &resp)
				if scenario == "hourly schedule change" {
					if !resp.Diagnostics.HasError() || patches != 0 || admissions != 0 {
						t.Fatal("unsupported schedule mutated existing policy")
					}
					if !resp.State.Raw.Equal(state.Raw) {
						t.Fatal("error changed state")
					}
					return
				}
				computeNoErrors(t, resp.Diagnostics)
				if patches != 1 || admissions != 0 {
					t.Fatalf("patches=%d admissions=%d", patches, admissions)
				}
			})
		}
	}
}

func TestComputeBackupCreateRejectsUnsupportedSchedulesBeforeHTTP(t *testing.T) {
	for _, kind := range []string{"cloud", "gpu"} {
		for _, scenario := range []string{"hourly", "weekly without day", "daily with day"} {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				calls := 0
				client := computeTestClient(t, func(w http.ResponseWriter, req *http.Request) { calls++; http.Error(w, "unexpected", 500) })
				r, model := recoveryTestResource(kind, "backup_storage", client)
				m := model.(vmBackupPolicyModel)
				switch scenario {
				case "hourly":
					m.Frequency = types.StringValue("hourly")
				case "weekly without day":
					m.Frequency = types.StringValue("weekly")
				default:
					m.DayOfWeek = types.Int64Value(2)
				}
				resp := resource.CreateResponse{State: computeState(t, r, nil)}
				r.Create(context.Background(), resource.CreateRequest{Plan: computePlanState(t, r, m)}, &resp)
				if !resp.Diagnostics.HasError() || calls != 0 {
					t.Fatal("invalid schedule reached API")
				}
			})
		}
	}
}
