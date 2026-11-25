package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

func TestComputeCatalogPlanSafetyMissingSelection(t *testing.T) {
	for _, kind := range []string{"cloud", "gpu"} {
		for _, product := range []string{"snapshot_storage", "backup_storage", "block_storage"} {
			for _, scenario := range []string{"new missing", "new unknown", "new supplied", "unchanged legacy", "omitted existing selection", "destroy legacy"} {
				t.Run(kind+"/"+product+"/"+scenario, func(t *testing.T) {
					r, model := recoveryTestResource(kind, product, nil)
					var old any
					next := recoveryTestSetCatalog(model, types.StringUnknown())
					configured := types.StringNull()
					// A missing catalog is the API's to refuse, not the plan's.
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
					computeNoErrors(t, resp.Diagnostics)
					if resp.RequiresReplace || !resp.PlanValue.Equal(wantValue) {
						t.Fatalf("replace=%v value=%s; want no replacement and %s", resp.RequiresReplace, resp.PlanValue, wantValue)
					}
				})
			}
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
					wantReplace := priorKnown
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
			// A new backup policy without a billing catalog must plan, not call the API.
			var requests atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				requests.Add(1)
				http.Error(w, "planning must not call the API", http.StatusBadRequest)
			}))
			defer srv.Close()
			work := t.TempDir()
			writeTestFile(t, filepath.Join(work, "main.tf"), fmt.Sprintf(`terraform {
 required_providers { ibee = { source = "devs-ibee/ibee" } }
}
provider "ibee" {
 endpoint = %q
 token = "catalog-fixture-token"
 workspace_id = "catalog-fixture-workspace"
}
resource "ibee_%s_vm_backup_policy" "new" {
 vm_id = "vm-1"
}
`, srv.URL, kind))
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "terraform", "plan", "-detailed-exitcode", "-input=false", "-no-color")
			cmd.Dir, cmd.Env = work, env
			out, err := cmd.CombinedOutput()
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 {
				t.Fatalf("plan must propose the policy: %v\n%s", err, out)
			}
			if got := requests.Load(); got != 0 {
				t.Fatalf("planning made %d API calls", got)
			}
		})
	}
}
