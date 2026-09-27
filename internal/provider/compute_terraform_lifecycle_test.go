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
	"strings"
	"sync"
	"testing"
	"time"
)

// Real Terraform protocol/state verification with isolated, loopback-only fixtures.
func TestTerraformLifecycleCompute(t *testing.T) {
	if os.Getenv("IBEE_TF_TEST") != "1" {
		t.Skip("set IBEE_TF_TEST=1 for Terraform CLI lifecycle checks against local mocks")
	}
	if _, err := exec.LookPath("terraform"); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-buildvcs=false", "-o", filepath.Join(bin, "terraform-provider-ibee"), ".")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	cli := filepath.Join(dir, "dev.tfrc")
	writeTestFile(t, cli, fmt.Sprintf("provider_installation {\n dev_overrides { \"devs-ibee/ibee\" = %q }\n direct {}\n}\n", bin))
	f := &computeTerraformFixture{allowed: true, vms: map[string]map[string]any{}, snapshots: map[string]map[string]any{}, policies: map[string]map[string]any{}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "IBEE_") || strings.HasPrefix(key, "TF_") {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, "TF_CLI_CONFIG_FILE="+cli, "TF_IN_AUTOMATION=1", "CHECKPOINT_DISABLE=1")
	run := func(want int, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "terraform", args...)
		cmd.Dir = work
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		code := 0
		if err != nil {
			if e, ok := err.(*exec.ExitError); ok {
				code = e.ExitCode()
			} else {
				t.Fatalf("terraform %v: %v", args, err)
			}
		}
		if code != want {
			t.Fatalf("terraform %v returned %d, expected %d\n%s", args, code, want, out)
		}
		return string(out)
	}
	config := fmt.Sprintf(`terraform {
 required_providers { ibee = { source = "devs-ibee/ibee" } }
}
provider "ibee" {
 endpoint = %q
 token = "compute-fixture-token"
 workspace_id = "compute-workspace"
 operation_timeout = "3s"
}
data "ibee_sites" "all" {}
`, srv.URL)
	for _, kind := range []string{"cloud", "gpu"} {
		config += fmt.Sprintf(`
data "ibee_compute_plans" "%[1]s" {
 vm_type = "%[1]s"
 site_id = "site"
}
data "ibee_images" "%[1]s" { vm_type = "%[1]s" }
resource "ibee_%[1]s_vm" "test" {
 name = "%[1]s-vm"
 site_id = "site"
 plan_id = "%[1]s-plan"
 template_id = "image"
 os_distro = "ubuntu"
 ssh_key_ids = ["key-1"]
 tags = ["terraform"]
}
resource "ibee_%[1]s_vm_volume_attachment" "test" {
 vm_id = ibee_%[1]s_vm.test.id
 volume_id = "%[1]s-volume"
}
resource "ibee_%[1]s_vm_snapshot" "test" {
 vm_id = ibee_%[1]s_vm.test.id
 name = "%[1]s-snapshot"
 depends_on = [ibee_%[1]s_vm_volume_attachment.test]
}
resource "ibee_%[1]s_vm_backup_policy" "test" {
 vm_id = ibee_%[1]s_vm.test.id
 retention_days = 7
}
`, kind)
	}
	writeTestFile(t, filepath.Join(work, "main.tf"), config)
	run(0, "validate", "-no-color")
	run(0, "apply", "-auto-approve", "-input=false", "-no-color")
	run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	// Simulate pre-existing monthly purchases before importing. Omitted terms must
	// preserve the canonical one-month commitment, rather than plan replacement.
	f.mu.Lock()
	for _, kind := range []string{"cloud", "gpu"} {
		f.vms[kind]["billing_catalog"] = computeTestSelectedBillingTerm("MONTHLY")
	}
	f.mu.Unlock()
	for _, kind := range []string{"cloud", "gpu"} {
		for _, item := range []struct{ suffix, id string }{{"_vm", kind + "-vm-id"}, {"_vm_volume_attachment", kind + "-vm-id/" + kind + "-volume"}, {"_vm_snapshot", kind + "-snapshot-id"}, {"_vm_backup_policy", kind + "-vm-id"}} {
			address := "ibee_" + kind + item.suffix + ".test"
			run(0, "state", "rm", address)
			run(0, "import", "-input=false", "-no-color", address, item.id)
		}
	}
	run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	// Only an explicit term change may replace either imported VM.
	explicitHourly := strings.ReplaceAll(config, " tags = [\"terraform\"]", " tags = [\"terraform\"]\n billing_interval = \"HOURLY\"")
	writeTestFile(t, filepath.Join(work, "main.tf"), explicitHourly)
	replacement := run(2, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	for _, kind := range []string{"cloud", "gpu"} {
		if !strings.Contains(replacement, "ibee_"+kind+"_vm.test must be replaced") {
			t.Fatalf("explicit hourly term did not replace imported monthly %s VM:\n%s", kind, replacement)
		}
	}
	writeTestFile(t, filepath.Join(work, "main.tf"), config)
	run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	// Portal changes must appear in the next plan. A schedule update converges after apply.
	f.mu.Lock()
	f.vms["cloud"]["name"] = "changed-in-portal"
	f.mu.Unlock()
	run(2, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	f.mu.Lock()
	f.vms["cloud"]["name"] = "cloud-vm"
	f.policies["cloud"]["retention_days"] = 14
	f.mu.Unlock()
	run(0, "apply", "-auto-approve", "-input=false", "-no-color")
	run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	// Cleanup does not make a purchase-admission call, even after billing blocks creation.
	f.mu.Lock()
	f.allowed = false
	admissionCalls := f.admissions
	f.mu.Unlock()
	run(0, "destroy", "-auto-approve", "-input=false", "-no-color")
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.vms) != 0 || len(f.snapshots) != 0 || f.admissions != admissionCalls {
		t.Fatalf("cleanup failed: VMs=%d snapshots=%d admission calls=%d/%d", len(f.vms), len(f.snapshots), f.admissions, admissionCalls)
	}
	for kind, p := range f.policies {
		if p["enabled"] != false {
			t.Errorf("%s backups not disabled", kind)
		}
	}
}

type computeTerraformFixture struct {
	mu                       sync.Mutex
	allowed                  bool
	admissions               int
	vms, snapshots, policies map[string]map[string]any
}

func (f *computeTerraformFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer compute-fixture-token" || r.URL.Query().Get("workspace_id") != "compute-workspace" {
		http.Error(w, "bad fixture auth", 403)
		return
	}
	send := func(v any) { w.Header().Set("Content-Type", "application/json"); _ = json.NewEncoder(w).Encode(v) }
	body := func() map[string]any { var b map[string]any; _ = json.NewDecoder(r.Body).Decode(&b); return b }
	p := r.URL.Path
	if p == "/billing/resource-eligibility" {
		b := body()
		f.admissions++
		send(map[string]any{"allowed": f.allowed, "organization_id": "org", "reason": "eligible", "billing_mode": "PREPAID", "billing_state": "CURRENT", "currency": "INR", "sku_code": b["sku_code"], "evaluated_at": "2026-09-27T00:00:00Z"})
		return
	}
	if p == "/compute/plans" {
		kind := r.URL.Query().Get("vm_type")
		count := 0
		model := ""
		if kind == "gpu" {
			count = 1
			model = "A100"
		}
		send(map[string]any{"plans": []any{map[string]any{"plan_id": kind + "-plan", "name": kind + " plan", "code": kind + "-sku", "cpu": 4, "ram_mb": 8192, "disk_gb": 80, "gpu_count": count, "gpu_model": model, "selectable": true, "pricing_status": "priced", "currency": "INR", "billing_interval": "MONTHLY", "hourly_price_minor": 20, "monthly_price_minor": 12000, "site_id": "site", "billing_catalog": computeTestBillingCatalog(kind+"-sku", 20)}}})
		return
	}
	if p == "/compute/images" {
		send(map[string]any{"images": []any{map[string]any{"template_id": "image", "name": "Ubuntu", "os_distro": "ubuntu", "os_type": "linux"}}})
		return
	}
	if p == "/compute/sites" {
		send(map[string]any{"sites": []any{map[string]any{"site_id": "site", "name": "Test site"}}})
		return
	}
	if strings.HasPrefix(p, "/compute/operations/") {
		send(map[string]any{"status": "succeeded"})
		return
	}
	for _, kind := range []string{"cloud", "gpu"} {
		vmBase := "/compute/" + kind + "-vms"
		vmPath := vmBase + "/" + kind + "-vm-id"
		snapshotPath := "/compute/" + kind + "-vm-snapshots/" + kind + "-snapshot-id"
		if p == vmBase && r.Method == "POST" {
			b := body()
			b["_id"] = kind + "-vm-id"
			b["status"] = "running"
			b["public_ip"] = "192.0.2.1"
			b["data_volumes"] = []any{}
			f.vms[kind] = b
			send(operationAccepted{VmID: kind + "-vm-id", OperationID: kind + "-create", Status: "accepted"})
			return
		}
		if p == vmPath {
			vm, ok := f.vms[kind]
			if !ok {
				http.NotFound(w, r)
				return
			}
			if r.Method == "DELETE" {
				delete(f.vms, kind)
				send(operationAccepted{VmID: kind + "-vm-id", OperationID: kind + "-delete", Status: "accepted"})
				return
			}
			send(vm)
			return
		}
		if p == vmPath+"/actions/attach-volume" {
			b := body()
			f.vms[kind]["data_volumes"] = []any{map[string]any{"volume_id": b["volume_id"], "mode": b["mode"], "guest_device": "/dev/vdb", "mount_instructions": "Mount the volume"}}
			send(operationAccepted{VmID: kind + "-vm-id", OperationID: kind + "-attach", Status: "accepted"})
			return
		}
		if p == vmPath+"/actions/detach-volume" {
			b := body()
			if b["force"] != false {
				http.Error(w, "unsafe force detach", 400)
				return
			}
			f.vms[kind]["data_volumes"] = []any{}
			send(operationAccepted{VmID: kind + "-vm-id", OperationID: kind + "-detach", Status: "accepted"})
			return
		}
		if p == vmPath+"/snapshots" && r.Method == "POST" {
			b := body()
			s := map[string]any{"snapshot_set_id": kind + "-snapshot-id", "vm_id": kind + "-vm-id", "name": b["name"], "capture_scope": b["mode"], "description": b["description"], "status": "succeeded", "recovery_point_id": "rp-" + kind}
			f.snapshots[kind] = s
			send(s)
			return
		}
		if p == snapshotPath {
			s, ok := f.snapshots[kind]
			if !ok {
				http.NotFound(w, r)
				return
			}
			if r.Method == "DELETE" {
				delete(f.snapshots, kind)
				send(map[string]any{"status": "deleted", "snapshot_set_id": kind + "-snapshot-id"})
				return
			}
			send(s)
			return
		}
		if strings.HasPrefix(p, vmPath+"/backups/") {
			action := strings.TrimPrefix(p, vmPath+"/backups/")
			if action == "enable" {
				b := body()
				b["vm_id"] = kind + "-vm-id"
				b["policy_id"] = kind + "-policy"
				b["enabled"] = true
				f.policies[kind] = b
				send(b)
				return
			}
			policy, ok := f.policies[kind]
			if !ok {
				http.NotFound(w, r)
				return
			}
			if action == "disable" {
				policy["enabled"] = false
			}
			if action == "policy" && r.Method == "PATCH" {
				for key, v := range body() {
					policy[key] = v
				}
			}
			send(policy)
			return
		}
	}
	http.Error(w, "unexpected fixture route: "+r.Method+" "+p, 404)
}
