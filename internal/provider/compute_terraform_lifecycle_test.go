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
	f := &computeTerraformFixture{allowed: true, vms: map[string]map[string]any{}, snapshots: map[string]map[string]any{}, policies: map[string]map[string]any{}, resizes: map[string]int{}}
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
 os_type = "linux"
 ssh_key_ids = ["key-1"]
 tags = ["terraform"]
}
resource "ibee_%[1]s_vm_volume_attachment" "test" {
 vm_id = ibee_%[1]s_vm.test.id
 volume_id = "%[1]s-volume"
 billing_catalog = jsonencode({ sku_id = "block-sku", sku_code = "BLOCK-STD", product_code = "block_storage", site_id = "site", currency = "INR" })
}
resource "ibee_%[1]s_vm_snapshot" "test" {
 vm_id = ibee_%[1]s_vm.test.id
 name = "%[1]s-snapshot"
 billing_catalog = jsonencode({ sku_id = "snapshot-sku", sku_code = "SNAPSHOT-STD", product_code = "snapshot_storage", site_id = "site", currency = "INR" })
 depends_on = [ibee_%[1]s_vm_volume_attachment.test]
}
resource "ibee_%[1]s_vm_backup_policy" "test" {
 vm_id = ibee_%[1]s_vm.test.id
 retention_days = 7
 billing_catalog = jsonencode({ sku_id = "backup-sku", sku_code = "BACKUP-STD", product_code = "backup_storage", site_id = "site", currency = "INR" })
}
`, kind)
	}
	// The term is set explicitly here; a new VM without one is created HOURLY.
	hourly := strings.ReplaceAll(config, " tags = [\"terraform\"]", " tags = [\"terraform\"]\n billing_interval = \"HOURLY\"")
	writeTestFile(t, filepath.Join(work, "main.tf"), hourly)
	run(0, "validate", "-no-color")
	run(0, "apply", "-auto-approve", "-input=false", "-no-color")
	f.mu.Lock()
	for kind, policy := range f.policies {
		schedule := policy["schedule"].(map[string]any)
		if schedule["hour"] != float64(12) || schedule["frequency"] != "daily" || schedule["timezone"] != "UTC" {
			t.Errorf("%s new policy did not get the API's defaults: %v", kind, schedule)
		}
	}
	f.mu.Unlock()
	run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	// The production-shaped hourly response (absent/null commitment_period)
	// must also import and leave both cloud and GPU VM plans unchanged.
	for _, kind := range []string{"cloud", "gpu"} {
		address := "ibee_" + kind + "_vm.test"
		run(0, "state", "rm", address)
		run(0, "import", "-input=false", "-no-color", address, kind+"-vm-id")
	}
	run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	// Simulate pre-existing monthly purchases before importing. Omitted terms must
	// preserve the canonical one-month commitment, rather than plan replacement.
	f.mu.Lock()
	for _, kind := range []string{"cloud", "gpu"} {
		f.vms[kind]["billing_catalog"] = computeTestSelectedBillingTerm("MONTHLY")
		// Old defaults and legacy hourly schedules must survive import/upgrade.
		f.policies[kind]["schedule"].(map[string]any)["hour"] = 20
		if kind == "gpu" {
			f.policies[kind]["schedule"].(map[string]any)["frequency"] = "hourly"
		}
	}
	f.mu.Unlock()
	writeTestFile(t, filepath.Join(work, "main.tf"), config)
	for _, kind := range []string{"cloud", "gpu"} {
		for _, item := range []struct{ suffix, id string }{{"_vm", kind + "-vm-id"}, {"_vm_volume_attachment", kind + "-vm-id/" + kind + "-volume"}, {"_vm_snapshot", kind + "-snapshot-id"}, {"_vm_backup_policy", kind + "-vm-id"}} {
			address := "ibee_" + kind + item.suffix + ".test"
			run(0, "state", "rm", address)
			run(0, "import", "-input=false", "-no-color", address, item.id)
		}
	}
	run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	// Even explicit legacy frequency is safe if unchanged. New hourly schedules
	// and edits to existing hourly schedules are covered by focused schedule tests.
	// Only an explicit term change may replace either imported VM.
	writeTestFile(t, filepath.Join(work, "main.tf"), hourly)
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
	f.policies["gpu"]["retention_days"] = 14
	f.mu.Unlock()
	run(0, "apply", "-auto-approve", "-input=false", "-no-color")
	run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	f.mu.Lock()
	if f.policies["gpu"]["schedule"].(map[string]any)["frequency"] != "hourly" || f.policies["gpu"]["schedule"].(map[string]any)["hour"] != 20 {
		t.Error("retention-only update altered the imported hourly schedule")
	}
	f.mu.Unlock()
	// A plan change resizes both VMs in place; nothing is replaced.
	larger := strings.ReplaceAll(config, `-plan"`, `-plan-large"`)
	writeTestFile(t, filepath.Join(work, "main.tf"), larger)
	resize := run(2, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	for _, kind := range []string{"cloud", "gpu"} {
		if !strings.Contains(resize, "ibee_"+kind+"_vm.test will be updated in-place") || strings.Contains(resize, "ibee_"+kind+"_vm.test must be replaced") {
			t.Fatalf("plan change did not resize %s VM in place:\n%s", kind, resize)
		}
	}
	run(0, "apply", "-auto-approve", "-input=false", "-no-color")
	run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	f.mu.Lock()
	for _, kind := range []string{"cloud", "gpu"} {
		if f.resizes[kind] != 1 || f.vms[kind]["cpu"] != float64(8) || f.vms[kind]["plan_id"] != kind+"-plan-large" {
			t.Errorf("%s resize not applied in place: resizes=%d vm=%v", kind, f.resizes[kind], f.vms[kind])
		}
	}
	f.mu.Unlock()
	// Cleanup does not make a purchase-admission call, even after billing blocks creation.
	f.mu.Lock()
	f.allowed = false
	admissionCalls := f.admissions
	// Destroy performs its own refresh. Reproduce the production response here
	// as well so the compatibility regression cannot strand an hourly VM.
	for _, kind := range []string{"cloud", "gpu"} {
		f.vms[kind]["billing_catalog"] = computeTestSelectedBillingTerm("HOURLY")
	}
	f.mu.Unlock()
	// The VMs were created without delete_public_ip_action: the provider has no
	// default, and the API requires the choice to delete a VM with an automatic
	// public IP (the fixture answers 400 without it, as the API does). Adding it
	// is an in-place change that sends no mutation; destroy then sends it.
	withAction := strings.ReplaceAll(larger, " tags = [\"terraform\"]", " tags = [\"terraform\"]\n delete_public_ip_action = \"release\"")
	writeTestFile(t, filepath.Join(work, "main.tf"), withAction)
	added := run(2, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	for _, kind := range []string{"cloud", "gpu"} {
		if !strings.Contains(added, "ibee_"+kind+"_vm.test will be updated in-place") || strings.Contains(added, "ibee_"+kind+"_vm.test must be replaced") {
			t.Fatalf("adding delete_public_ip_action must be an in-place change for the %s VM:\n%s", kind, added)
		}
	}
	if !strings.Contains(added, `+ delete_public_ip_action = "release"`) {
		t.Fatalf("delete_public_ip_action not planned:\n%s", added)
	}
	run(0, "apply", "-auto-approve", "-input=false", "-no-color")
	run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	f.mu.Lock()
	for _, kind := range []string{"cloud", "gpu"} {
		if f.resizes[kind] != 1 || f.vms[kind]["plan_id"] != kind+"-plan-large" {
			t.Errorf("adding delete_public_ip_action mutated the %s VM: resizes=%d vm=%v", kind, f.resizes[kind], f.vms[kind])
		}
	}
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
	resizes                  map[string]int
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
		if interval := r.URL.Query().Get("billing_interval"); interval != "" && interval != "HOURLY" && interval != "MONTHLY" {
			http.Error(w, "fixture plans are listed for hourly or monthly discovery", 400)
			return
		}
		count := 0
		model := ""
		if kind == "gpu" {
			count = 1
			model = "A100"
		}
		var plans []any
		for _, size := range []struct {
			suffix         string
			cpu, ram, disk int
		}{{"", 4, 8192, 80}, {"-large", 8, 16384, 160}} {
			catalog := computeTestBillingCatalog(kind+"-sku"+size.suffix, 20)
			catalog["plan_id"] = kind + "-plan" + size.suffix
			plans = append(plans, map[string]any{"plan_id": kind + "-plan" + size.suffix, "name": kind + " plan", "code": kind + "-sku" + size.suffix, "cpu": size.cpu, "ram_mb": size.ram, "disk_gb": size.disk, "gpu_count": count, "gpu_model": model, "selectable": true, "pricing_status": "priced", "currency": "INR", "billing_interval": "HOURLY", "hourly_price_minor": 20, "monthly_price_minor": 12000, "site_id": "site", "billing_catalog": catalog})
		}
		send(map[string]any{"plans": plans})
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
		if p == "/block-storage/volumes/"+kind+"-volume" && r.Method == "GET" {
			send(map[string]any{"id": kind + "-volume", "site_id": "site", "metadata": map[string]any{"billing_catalog": map[string]any{"sku_id": "block-sku", "sku_code": "BLOCK-STD", "product_code": "block_storage", "site_id": "site", "currency": "INR"}}})
			return
		}
		if p == vmBase && r.Method == "POST" {
			b := body()
			catalog, ok := b["billing_catalog"].(map[string]any)
			if !ok || catalog["billing_interval"] != "HOURLY" || catalog["committed"] != false || catalog["commitment_period"] != "HOURLY" {
				http.Error(w, "purchase must preserve the complete selected catalog term", 400)
				return
			}
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
				// The API refuses to delete a VM with an automatic public IP unless the caller chooses.
				if action, _ := body()["public_ip_action"].(string); action != "release" && action != "reserve" {
					w.WriteHeader(400)
					send(map[string]any{"error": "public_ip_action_required"})
					return
				}
				delete(f.vms, kind)
				send(operationAccepted{VmID: kind + "-vm-id", OperationID: kind + "-delete", Status: "accepted"})
				return
			}
			// Read contract observed in production: hourly, explicitly uncommitted,
			// but commitment_period omitted (cloud) or null (GPU).
			projection := make(map[string]any, len(vm))
			for key, value := range vm {
				projection[key] = value
			}
			catalog := vm["billing_catalog"].(map[string]any)
			if catalog["billing_interval"] == "HOURLY" {
				term := make(map[string]any, len(catalog))
				for key, value := range catalog {
					term[key] = value
				}
				delete(term, "commitment_period")
				if kind == "gpu" {
					term["commitment_period"] = nil
				}
				projection["billing_catalog"] = term
			}
			send(projection)
			return
		}
		if p == vmPath+"/actions/resize" && r.Method == "POST" {
			// The contract's resize takes cpu, ram_mb and disk_gb only. The fixture
			// applies the shape and reports the plan that has it.
			b := body()
			if r.Header.Get("X-Idempotency-Key") == "" || len(b) != 3 || b["cpu"] == nil || b["ram_mb"] == nil || b["disk_gb"] == nil {
				http.Error(w, "resize must carry a key and only cpu, ram_mb and disk_gb", 400)
				return
			}
			f.resizes[kind]++
			for _, key := range []string{"cpu", "ram_mb", "disk_gb"} {
				f.vms[kind][key] = b[key]
			}
			if b["cpu"] == float64(8) {
				f.vms[kind]["plan_id"] = kind + "-plan-large"
			}
			send(operationAccepted{VmID: kind + "-vm-id", OperationID: kind + "-resize", Status: "accepted"})
			return
		}
		if p == vmPath+"/actions/attach-volume" {
			b := body()
			if !computeFixtureCatalog(w, b, "block_storage", "BLOCK-STD") {
				return
			}
			f.vms[kind]["data_volumes"] = []any{map[string]any{"volume_id": b["volume_id"], "mode": "single-writer", "billing_catalog": b["billing_catalog"], "guest_device": "/dev/vdb", "mount_instructions": "Mount the volume"}}
			send(operationAccepted{VmID: kind + "-vm-id", OperationID: kind + "-attach", Status: "accepted"})
			return
		}
		if p == vmPath+"/actions/detach-volume" {
			b := body()
			if b["force"] != nil || b["volume_id"] == nil {
				http.Error(w, "detach must send volume_id and must not send force", 400)
				return
			}
			f.vms[kind]["data_volumes"] = []any{}
			send(operationAccepted{VmID: kind + "-vm-id", OperationID: kind + "-detach", Status: "accepted"})
			return
		}
		if p == vmPath+"/snapshots" && r.Method == "POST" {
			b := body()
			if !computeFixtureCatalog(w, b, "snapshot_storage", "SNAPSHOT-STD") {
				return
			}
			s := map[string]any{"snapshot_set_id": kind + "-snapshot-id", "vm_id": kind + "-vm-id", "name": b["name"], "capture_scope": b["mode"], "description": b["description"], "status": "succeeded", "recovery_point_id": "rp-" + kind}
			s["billing_catalog"] = b["billing_catalog"]
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
				if !computeFixtureCatalog(w, b, "backup_storage", "BACKUP-STD") {
					return
				}
				// Like the API, fill in the settings the request leaves out.
				if b["schedule"] == nil {
					b["schedule"] = map[string]any{"frequency": "daily", "timezone": "UTC", "hour": float64(12), "minute": float64(0), "window_minutes": float64(30)}
				}
				for key, value := range map[string]any{"full_backup_interval_days": float64(7), "incremental_enabled": true} {
					if b[key] == nil {
						b[key] = value
					}
				}
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
