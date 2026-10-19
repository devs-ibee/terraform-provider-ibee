package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Runs the 0.6.0 actions and Secret Store identity resources through the
// Terraform CLI against a loopback fixture. Never uses a real IBEE endpoint.
func TestTerraformRecoveryActionsAndSecretIdentity(t *testing.T) {
	if os.Getenv("IBEE_TF_TEST") != "1" {
		t.Skip("set IBEE_TF_TEST=1 to run Terraform CLI checks against local mocks")
	}
	terraform, err := exec.LookPath("terraform")
	if err != nil {
		t.Fatal("Terraform is required")
	}
	dir := t.TempDir()
	bin, work := filepath.Join(dir, "bin"), filepath.Join(dir, "work")
	for _, p := range []string{bin, work} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	cli := filepath.Join(dir, "dev.tfrc")
	writeTestFile(t, cli, fmt.Sprintf("provider_installation {\n dev_overrides { \"devs-ibee/ibee\" = %q }\n}\n", bin))
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "IBEE_") || strings.HasPrefix(key, "TF_") || strings.HasSuffix(strings.ToUpper(key), "_PROXY") {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, "TF_CLI_CONFIG_FILE="+cli, "TF_IN_AUTOMATION=1", "CHECKPOINT_DISABLE=1", "HTTP_PROXY=http://127.0.0.1:1", "HTTPS_PROXY=http://127.0.0.1:1", "NO_PROXY=127.0.0.1,localhost", "TF_VAR_vm_password=fixture-password-never-shown")
	run := func(want int, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, terraform, args...)
		cmd.Dir, cmd.Env = work, env
		out, err := cmd.CombinedOutput()
		code := 0
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else if err != nil {
			t.Fatalf("terraform %v: %v", args, err)
		}
		if code != want {
			t.Fatalf("terraform %v exited %d, want %d:\n%s", args, code, want, out)
		}
		for _, secret := range []string{"fixture-secret-id-never-shown", "fixture-password-never-shown"} {
			if strings.Contains(string(out), secret) {
				t.Fatalf("terraform %v printed a secret:\n%s", args, out)
			}
		}
		return string(out)
	}
	var version struct {
		Version string `json:"terraform_version"`
	}
	if err := json.Unmarshal([]byte(run(0, "version", "-json")), &version); err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(version.Version, ".")
	minor, _ := strconv.Atoi(parts[1])
	actions := parts[0] != "1" || minor >= 14
	root, _ := filepath.Abs("../..")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", filepath.Join(bin, "terraform-provider-ibee"), ".")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	fixture := &recoveryIdentityFixture{t: t}
	server := httptest.NewServer(fixture)
	defer server.Close()
	config := fmt.Sprintf(`terraform {
  required_providers {
    ibee = { source = "devs-ibee/ibee" }
  }
}
provider "ibee" {
  endpoint          = %q
  token             = "fixture-only-token"
  workspace_id      = "fixture-workspace"
  operation_timeout = "5s"
}
resource "ibee_secret_identity" "app" {
  store_id    = "store-1"
  name        = "app"
  auth_method = "approle"
}
resource "ibee_secret_identity_scope" "shared" {
  identity_id = ibee_secret_identity.app.id
  store_id    = "store-2"
}
`, server.URL)
	if actions {
		config += `
variable "vm_password" {
  type      = string
  sensitive = true
}
action "ibee_vm_snapshot_restore" "new_vm" {
  config {
    vm_type         = "cloud"
    vm_id           = "vm-1"
    snapshot_set_id = "set-1"
    target_mode     = "new_vm"
    target_vm_name  = "restored"
  }
}
action "ibee_vm_backup_restore" "replace" {
  config {
    vm_type           = "gpu"
    vm_id             = "vm-1"
    recovery_point_id = "rp-1"
    target_mode       = "replace"
  }
}
action "ibee_vm_backup_run" "now" {
  config {
    vm_type         = "cloud"
    vm_id           = "vm-1"
    billing_catalog = jsonencode({ sku_id = 9007199254740993, sku_code = "BACKUP-STD" })
  }
}
action "ibee_vm_access" "password" {
  config {
    vm_type      = "cloud"
    vm_id        = "vm-1"
    new_password = var.vm_password
  }
}
`
	}
	writeTestFile(t, filepath.Join(work, "main.tf"), config)
	run(0, "validate", "-no-color")
	run(0, "apply", "-auto-approve", "-input=false", "-no-color")
	run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	state, err := os.ReadFile(filepath.Join(work, "terraform.tfstate"))
	if err != nil || !strings.Contains(string(state), "fixture-secret-id-never-shown") {
		t.Fatal("the secret ID returned at creation must be kept in state")
	}
	if fixture.count("GET /secret-store/identities/id-1/access") != 1 {
		t.Fatal("login details must be read once")
	}
	if actions {
		if got := fixture.requestsWithPrefix("POST /compute/", "PATCH /compute/"); len(got) != 0 {
			t.Fatalf("plan and apply must not invoke actions: %v", got)
		}
		for name, want := range map[string]string{
			"ibee_vm_snapshot_restore.new_vm": "POST /compute/cloud-vm-snapshots/set-1/actions/restore",
			"ibee_vm_backup_restore.replace":  "POST /compute/gpu-vms/vm-1/backups/actions/restore",
			"ibee_vm_backup_run.now":          "POST /compute/cloud-vms/vm-1/backups/runs",
			"ibee_vm_access.password":         "PATCH /compute/cloud-vms/vm-1/actions/access",
		} {
			run(0, "apply", "-invoke=action."+name, "-auto-approve", "-input=false", "-no-color")
			if fixture.count(want) != 1 {
				t.Fatalf("%s did not send %s", name, want)
			}
		}
		fixture.mu.Lock()
		backup, access := fixture.bodies["POST /compute/cloud-vms/vm-1/backups/runs"], fixture.bodies["PATCH /compute/cloud-vms/vm-1/actions/access"]
		fixture.mu.Unlock()
		if !strings.Contains(backup, `"sku_id":9007199254740993`) || !strings.Contains(access, `"new_password":"fixture-password-never-shown"`) {
			t.Fatal("action bodies were not relayed unchanged")
		}
	}
	run(0, "destroy", "-auto-approve", "-input=false", "-no-color")
	if fixture.count("DELETE /secret-store/identities/id-1") != 1 || fixture.count("DELETE /secret-store/scopes/scope-1") != 1 {
		t.Fatal("destroy must delete the scope and the identity")
	}
}

type recoveryIdentityFixture struct {
	t        *testing.T
	mu       sync.Mutex
	requests []string
	bodies   map[string]string
	identity bool
	scope    bool
}

func (f *recoveryIdentityFixture) count(request string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if r == request {
			n++
		}
	}
	return n
}

func (f *recoveryIdentityFixture) requestsWithPrefix(prefixes ...string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.requests {
		for _, p := range prefixes {
			if strings.HasPrefix(r, p) {
				out = append(out, r)
			}
		}
	}
	return out
}

func (f *recoveryIdentityFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := r.Method + " " + r.URL.Path
	f.requests = append(f.requests, key)
	raw, _ := io.ReadAll(r.Body)
	body := string(raw)
	if f.bodies == nil {
		f.bodies = map[string]string{}
	}
	f.bodies[key] = body
	identity := secretIdentityTestResponse("active", "read_only")
	scope := map[string]any{"id": "scope-1", "identity_id": "id-1", "scope_type": "store", "store_id": "store-2", "organization_id": "org", "workspace_id": "workspace", "access_mode": "read_only", "allow_version_read": true, "allow_rollback": false, "allow_destroy": false, "created_at": "2026-10-19T00:00:00Z"}
	switch key {
	case "POST /secret-store/stores/store-1/identities":
		f.identity = true
		w.WriteHeader(201)
		computeJSON(w, identity)
	case "GET /secret-store/identities/id-1/access":
		computeJSON(w, map[string]any{"identity_id": "id-1", "auth_method": "approle", "role_id": "role-1", "secret_id": "fixture-secret-id-never-shown"})
	case "GET /secret-store/identities/id-1":
		if !f.identity {
			w.WriteHeader(404)
			computeJSON(w, map[string]any{"error": "not_found"})
			return
		}
		computeJSON(w, identity)
	case "DELETE /secret-store/identities/id-1":
		f.identity = false
		computeJSON(w, map[string]any{"status": "deleted"})
	case "POST /secret-store/identities/id-1/scopes":
		f.scope = true
		w.WriteHeader(201)
		computeJSON(w, scope)
	case "GET /secret-store/identities/id-1/scopes":
		scopes := []any{}
		if f.scope {
			scopes = append(scopes, scope)
		}
		computeJSON(w, map[string]any{"scopes": scopes, "total": len(scopes)})
	case "DELETE /secret-store/scopes/scope-1":
		f.scope = false
		computeJSON(w, map[string]any{"status": "deleted"})
	case "POST /compute/cloud-vm-snapshots/set-1/actions/restore", "POST /compute/gpu-vms/vm-1/backups/actions/restore":
		if !strings.Contains(body, `"requested_by":"terraform"`) || (strings.Contains(key, "snapshots") && r.URL.Query().Get("vm_id") != "vm-1") {
			f.t.Errorf("unexpected restore request %s %s", r.URL.String(), body)
		}
		computeJSON(w, map[string]any{"restore_id": "rs-1", "status": "queued"})
	case "GET /compute/cloud-vm-snapshots/restores/rs-1", "GET /compute/gpu-vm-backups/restores/rs-1":
		computeJSON(w, map[string]any{"restore_id": "rs-1", "status": "succeeded"})
	case "POST /compute/cloud-vms/vm-1/backups/runs":
		computeJSON(w, map[string]any{"run_id": "run-1", "status": "queued"})
	case "GET /compute/cloud-vm-backups/runs/run-1":
		computeJSON(w, map[string]any{"run_id": "run-1", "status": "succeeded"})
	case "PATCH /compute/cloud-vms/vm-1/actions/access":
		computeJSON(w, operationAccepted{OperationID: "op-1", VmID: "vm-1", Status: "accepted"})
	case "GET /compute/operations/op-1":
		computeJSON(w, map[string]any{"status": "succeeded"})
	default:
		f.t.Errorf("unexpected request %s", key)
		w.WriteHeader(500)
	}
}
