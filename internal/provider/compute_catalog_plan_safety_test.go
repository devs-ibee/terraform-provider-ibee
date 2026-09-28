package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Exercise the modifiers actually installed on each resource's schema. Config
// omission and the framework's unknown proposed value must remain distinct.
func catalogSafetyModify(t *testing.T, r resource.Resource, old, next any, configured types.String) planmodifier.StringResponse {
	t.Helper()
	ctx := context.Background()
	state := computeState(t, r, old)
	plan := computePlanState(t, r, next)
	var configModel any
	if next != nil {
		configModel = recoveryTestSetCatalog(next, configured)
	}
	config := computeState(t, r, configModel)
	stateValue, planValue := types.StringNull(), types.StringNull()
	if old != nil {
		computeNoErrors(t, state.GetAttribute(ctx, path.Root("billing_catalog"), &stateValue))
	}
	if next != nil {
		computeNoErrors(t, plan.GetAttribute(ctx, path.Root("billing_catalog"), &planValue))
	}
	req := planmodifier.StringRequest{
		Path:   path.Root("billing_catalog"),
		Config: tfsdk.Config{Raw: config.Raw, Schema: config.Schema}, ConfigValue: configured,
		State: state, StateValue: stateValue, Plan: plan, PlanValue: planValue,
	}
	resp := planmodifier.StringResponse{PlanValue: planValue}
	var resourceSchema resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &resourceSchema)
	computeNoErrors(t, resourceSchema.Diagnostics)
	attribute := resourceSchema.Schema.Attributes["billing_catalog"].(schema.StringAttribute)
	for _, modifier := range attribute.PlanModifiers {
		modifier.PlanModifyString(ctx, req, &resp)
		if resp.Diagnostics.HasError() {
			break
		}
		req.PlanValue = resp.PlanValue
	}
	return resp
}

func catalogSafetyRequireDiagnostic(t *testing.T, diagnostics diag.Diagnostics) {
	t.Helper()
	if !diagnostics.HasError() {
		t.Fatal("missing catalog was accepted during planning")
	}
	for _, diagnostic := range diagnostics {
		if located, ok := diagnostic.(diag.DiagnosticWithPath); ok && located.Path().Equal(path.Root("billing_catalog")) && diagnostic.Severity() == diag.SeverityError {
			return
		}
	}
	t.Fatalf("expected an error on billing_catalog, got %v", diagnostics)
}

func TestComputeCatalogPlanSafetyMissingSelection(t *testing.T) {
	for _, kind := range []string{"cloud", "gpu"} {
		for _, product := range []string{"snapshot_storage", "backup_storage", "block_storage"} {
			for _, scenario := range []string{"new missing", "new unknown", "new supplied", "unchanged legacy", "omitted existing selection", "destroy legacy"} {
				t.Run(kind+"/"+product+"/"+scenario, func(t *testing.T) {
					r, model := recoveryTestResource(kind, product, nil)
					var old any
					next := recoveryTestSetCatalog(model, types.StringUnknown())
					configured := types.StringNull()
					wantError := scenario == "new missing" && product != "block_storage"
					wantValue := types.StringUnknown()
					switch scenario {
					case "new unknown":
						configured = types.StringUnknown()
					case "new supplied":
						configured = recoveryTestCatalog(product)
						next, wantValue = model, configured
					case "unchanged legacy":
						old = recoveryTestSetCatalog(model, types.StringNull())
						wantValue = types.StringNull()
					case "omitted existing selection":
						old, wantValue = model, recoveryTestCatalog(product)
					case "destroy legacy":
						old = recoveryTestSetCatalog(model, types.StringNull())
						next, wantValue = nil, types.StringNull()
					}
					resp := catalogSafetyModify(t, r, old, next, configured)
					if wantError {
						catalogSafetyRequireDiagnostic(t, resp.Diagnostics)
						return
					}
					computeNoErrors(t, resp.Diagnostics)
					if resp.RequiresReplace || !resp.PlanValue.Equal(wantValue) {
						t.Fatalf("replace=%v value=%s; want no replacement and %s", resp.RequiresReplace, resp.PlanValue, wantValue)
					}
				})
			}
		}
	}
}

func TestComputeCatalogPlanSafetyLegacySnapshotReplacement(t *testing.T) {
	for _, kind := range []string{"cloud", "gpu"} {
		for _, field := range []string{"vm_id", "unknown vm_id", "name", "description", "mode", "selected_data_volume_ids"} {
			t.Run(kind+"/"+field, func(t *testing.T) {
				r, model := recoveryTestResource(kind, "snapshot_storage", nil)
				old := model.(vmSnapshotModel)
				old.ID = types.StringValue("snap-legacy")
				old.BillingCatalog = types.StringNull()
				// Keep selective mode valid when changing only the selected set.
				if field == "selected_data_volume_ids" {
					old.Mode = types.StringValue("selective")
					old.SelectedDataVolumeIDs = types.SetValueMust(types.StringType, []attr.Value{types.StringValue("vol-1")})
				}
				next := old
				next.BillingCatalog = types.StringUnknown()
				switch field {
				case "vm_id":
					next.VmID = types.StringValue("vm-2")
				case "unknown vm_id":
					next.VmID = types.StringUnknown()
				case "name":
					next.Name = types.StringValue("renamed")
				case "description":
					next.Description = types.StringValue("new description")
				case "mode":
					next.Mode = types.StringValue("all_attached")
				case "selected_data_volume_ids":
					next.SelectedDataVolumeIDs = types.SetValueMust(types.StringType, []attr.Value{types.StringValue("vol-2")})
				}
				resp := catalogSafetyModify(t, r, old, next, types.StringNull())
				catalogSafetyRequireDiagnostic(t, resp.Diagnostics)
			})
		}
	}
}

func TestComputeCatalogPlanSafetyUnknownSelection(t *testing.T) {
	for _, kind := range []string{"cloud", "gpu"} {
		for _, product := range []string{"snapshot_storage", "backup_storage", "block_storage"} {
			for _, priorKnown := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/prior_known=%t", kind, product, priorKnown), func(t *testing.T) {
					r, model := recoveryTestResource(kind, product, nil)
					old := model
					if !priorKnown {
						old = recoveryTestSetCatalog(model, types.StringNull())
					}
					next := recoveryTestSetCatalog(model, types.StringUnknown())
					resp := catalogSafetyModify(t, r, old, next, types.StringUnknown())
					computeNoErrors(t, resp.Diagnostics)
					wantReplace := priorKnown && product != "backup_storage"
					if resp.RequiresReplace != wantReplace || !resp.PlanValue.IsUnknown() {
						t.Fatalf("replace=%v, want %v; explicit unknown selection must remain unknown: %s", resp.RequiresReplace, wantReplace, resp.PlanValue)
					}
				})
			}
		}
	}
}

// Real Core planning matters here: replacement entails a second provider plan
// with null prior state, and unknown expressions must already plan replacement.
func TestTerraformComputeCatalogPlanSafety(t *testing.T) {
	if os.Getenv("IBEE_TF_TEST") != "1" {
		t.Skip("set IBEE_TF_TEST=1 for loopback-only catalog plan safety checks")
	}
	if _, err := exec.LookPath("terraform"); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	build := exec.Command("go", "build", "-buildvcs=false", "-o", filepath.Join(bin, "terraform-provider-ibee"), ".")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	cli := filepath.Join(t.TempDir(), "dev.tfrc")
	writeTestFile(t, cli, fmt.Sprintf("provider_installation {\n dev_overrides { \"devs-ibee/ibee\" = %q }\n direct {}\n}\n", bin))
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(key, "IBEE_") && !strings.HasPrefix(key, "TF_") && key != "CHECKPOINT_DISABLE" {
			env = append(env, entry)
		}
	}
	env = append(env, "TF_CLI_CONFIG_FILE="+cli, "TF_IN_AUTOMATION=1", "CHECKPOINT_DISABLE=1")
	for _, kind := range []string{"cloud", "gpu"} {
		t.Run(kind, func(t *testing.T) {
			for _, scenario := range []string{"legacy replacement", "new snapshot missing", "new backup missing", "unknown catalog replacement"} {
				t.Run(scenario, func(t *testing.T) {
					var mutations atomic.Int64
					snapshot := map[string]any{
						"snapshot_set_id": "snap-legacy", "vm_id": "vm-1", "name": "before", "description": nil,
						"capture_scope": "root_only", "status": "succeeded", "recovery_point_id": "rp-legacy",
					}
					if scenario == "unknown catalog replacement" {
						snapshot["billing_catalog"] = map[string]any{"sku_id": "snapshot-sku", "sku_code": "SNAPSHOT-STD"}
					}
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
						if req.Method != http.MethodGet {
							mutations.Add(1)
							http.Error(w, "planning must not mutate the API", http.StatusBadRequest)
							return
						}
						if req.URL.Path != "/compute/"+kind+"-vm-snapshots/snap-legacy" {
							t.Errorf("unexpected fixture read: %s", req.URL.Path)
							http.NotFound(w, req)
							return
						}
						computeJSON(w, snapshot)
					}))
					defer srv.Close()
					t.Cleanup(func() {
						if got := mutations.Load(); got != 0 {
							t.Errorf("unsafe plan/apply attempted %d API mutations, including possible snapshot deletion", got)
						}
					})
					work := t.TempDir()
					run := func(want int, args ...string) string {
						t.Helper()
						ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
						defer cancel()
						cmd := exec.CommandContext(ctx, "terraform", args...)
						cmd.Dir, cmd.Env = work, env
						out, err := cmd.CombinedOutput()
						code := 0
						if err != nil {
							if exit, ok := err.(*exec.ExitError); ok {
								code = exit.ExitCode()
							} else {
								t.Fatalf("terraform %v: %v", args, err)
							}
						}
						if code != want {
							t.Fatalf("terraform %v: exit %d, want %d\n%s", args, code, want, out)
						}
						return string(out)
					}
					prefix := fmt.Sprintf(`terraform {
 required_providers { ibee = { source = "devs-ibee/ibee" } }
}
provider "ibee" {
 endpoint = %q
 token = "catalog-fixture-token"
 workspace_id = "catalog-fixture-workspace"
}
`, srv.URL)
					address := "ibee_" + kind + "_vm_snapshot.legacy"
					config := prefix + fmt.Sprintf(`resource "ibee_%s_vm_snapshot" "legacy" {
 vm_id = "vm-1"
 name = "before"
 mode = "root_only"
}
`, kind)
					assertCatalogError := func(output string) {
						t.Helper()
						if !strings.Contains(output, "billing_catalog") || !strings.Contains(output, "Error:") {
							t.Fatalf("expected missing-catalog planning diagnostic:\n%s", output)
						}
					}
					assertSnapshotTracked := func() {
						t.Helper()
						out := run(0, "state", "show", "-no-color", address)
						if !strings.Contains(out, `"snap-legacy"`) || !strings.Contains(out, `"before"`) {
							t.Fatalf("legacy snapshot identity/state lost:\n%s", out)
						}
					}
					switch scenario {
					case "legacy replacement":
						writeTestFile(t, filepath.Join(work, "main.tf"), config)
						run(0, "import", "-input=false", "-no-color", address, "snap-legacy")
						run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
						writeTestFile(t, filepath.Join(work, "main.tf"), strings.Replace(config, `name = "before"`, `name = "after"`, 1))
						assertCatalogError(run(1, "plan", "-detailed-exitcode", "-input=false", "-no-color"))
						// apply must fail in planning, before reaching Delete, too.
						assertCatalogError(run(1, "apply", "-auto-approve", "-input=false", "-no-color"))
						assertSnapshotTracked()
						writeTestFile(t, filepath.Join(work, "main.tf"), config)
						assertCatalogError(run(1, "plan", "-replace="+address, "-detailed-exitcode", "-input=false", "-no-color"))
						run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
						run(2, "plan", "-destroy", "-detailed-exitcode", "-input=false", "-no-color")
						assertSnapshotTracked()
					case "new snapshot missing", "new backup missing":
						if scenario == "new backup missing" {
							config = prefix + fmt.Sprintf("resource \"ibee_%s_vm_backup_policy\" \"new\" {\n vm_id = \"vm-1\"\n}\n", kind)
						}
						writeTestFile(t, filepath.Join(work, "main.tf"), config)
						assertCatalogError(run(1, "plan", "-detailed-exitcode", "-input=false", "-no-color"))
					case "unknown catalog replacement":
						config = strings.Replace(config, ` mode = "root_only"`, " mode = \"root_only\"\n billing_catalog = terraform_data.catalog.output", 1)
						config += `resource "terraform_data" "catalog" {
 input = jsonencode({ sku_id = "snapshot-sku", sku_code = "SNAPSHOT-STD" })
}
`
						writeTestFile(t, filepath.Join(work, "main.tf"), config)
						// Seed only Terraform's built-in resource; never create a cloud resource.
						run(0, "apply", "-target=terraform_data.catalog", "-auto-approve", "-input=false", "-no-color")
						run(0, "import", "-input=false", "-no-color", address, "snap-legacy")
						run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
						config = strings.ReplaceAll(config, "SNAPSHOT-STD", "SNAPSHOT-OTHER")
						writeTestFile(t, filepath.Join(work, "main.tf"), config)
						run(2, "plan", "-out=unknown.tfplan", "-detailed-exitcode", "-input=false", "-no-color")
						var plan struct {
							Changes []struct {
								Address string `json:"address"`
								Change  struct {
									Actions      []string       `json:"actions"`
									AfterUnknown map[string]any `json:"after_unknown"`
									ReplacePaths [][]string     `json:"replace_paths"`
								} `json:"change"`
							} `json:"resource_changes"`
						}
						if err := json.Unmarshal([]byte(run(0, "show", "-json", "unknown.tfplan")), &plan); err != nil {
							t.Fatal(err)
						}
						found := false
						for _, change := range plan.Changes {
							if change.Address != address {
								continue
							}
							found = true
							if !reflect.DeepEqual(change.Change.Actions, []string{"delete", "create"}) || change.Change.AfterUnknown["billing_catalog"] != true || !reflect.DeepEqual(change.Change.ReplacePaths, [][]string{{"billing_catalog"}}) {
								t.Fatalf("unknown explicit catalog must already require replacement: %+v", change.Change)
							}
						}
						if !found {
							t.Fatal("snapshot missing from Terraform plan")
						}
						assertSnapshotTracked()
					}
				})
			}
		})
	}
}
