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

// Runs the actual Terraform CLI and provider subprocess against a loopback-only
// fixture. Never uses user credentials or a real IBEE endpoint.
func TestTerraformLifecycle(t *testing.T) {
	if os.Getenv("IBEE_TF_TEST") != "1" {
		t.Skip("set IBEE_TF_TEST=1 to run Terraform CLI lifecycle tests against local mocks")
	}
	if _, err := exec.LookPath("terraform"); err != nil {
		t.Fatal("Terraform 1.11+ is required")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err = os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-buildvcs=false", "-o", filepath.Join(bin, "terraform-provider-ibee"), ".")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	cli := filepath.Join(dir, "dev.tfrc")
	writeTestFile(t, cli, fmt.Sprintf("provider_installation {\n dev_overrides { \"devs-ibee/ibee\" = %q }\n direct {}\n}\n", bin))
	gateway := &terraformFixture{allowed: true}
	server := httptest.NewServer(gateway)
	defer server.Close()
	work := filepath.Join(dir, "work")
	os.MkdirAll(work, 0700)
	env := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "IBEE_") || strings.HasPrefix(key, "TF_") || strings.HasPrefix(key, "TF_VAR_") {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, "TF_CLI_CONFIG_FILE="+cli, "TF_IN_AUTOMATION=1", "CHECKPOINT_DISABLE=1", "TF_VAR_secret_payload=fixture-secret-never-in-state")
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
			if exit, ok := err.(*exec.ExitError); ok {
				code = exit.ExitCode()
			} else {
				t.Fatalf("terraform %v: %v", args, err)
			}
		}
		if code != want {
			t.Fatalf("terraform %v exited %d, want %d:\n%s", args, code, want, out)
		}
		if strings.Contains(string(out), "fixture-secret-never-in-state") {
			t.Fatal("secret leaked to Terraform output")
		}
		return string(out)
	}
	config := func(public bool, rotation int) {
		writeTestFile(t, filepath.Join(work, "main.tf"), fmt.Sprintf(`terraform {
 required_version = ">= 1.11"
 required_providers { ibee = { source = "devs-ibee/ibee" } }
}
provider "ibee" {
 endpoint = %q
 token = "fixture-only-token"
 workspace_id = "workspace-fixture"
 organization_id = "org-fixture"
 operation_timeout = "3s"
}
variable "secret_payload" {
 type = string
 sensitive = true
 ephemeral = true
}
data "ibee_billing_eligibility" "test" {}
data "ibee_sites" "test" {}
data "ibee_compute_plans" "test" {
 vm_type = "cloud"
 site_id = "site-1"
}
data "ibee_images" "test" { vm_type = "cloud" }
resource "ibee_bucket" "test" {
 name = "fixture-bucket"
 region = "fixture-region"
 is_public = %t
}
resource "ibee_secret_store" "test" {
 name = "fixture-store"
 description = "managed by test"
}
resource "ibee_secret" "test" {
 store_id = ibee_secret_store.test.id
 secret_name = "fixture-secret"
 value_wo = jsonencode({ password = var.secret_payload })
 value_wo_version = %d
}
`, server.URL, public, rotation))
	}
	config(false, 0)
	run(0, "validate", "-no-color")
	writeTestFile(t, filepath.Join(work, "invalid.tf"), "data \"ibee_billing_eligibility\" \"invalid\" {\n sku_code = \" \"\n estimated_cost_minor = -1\n}\n")
	invalid := run(1, "validate", "-no-color")
	if !strings.Contains(invalid, "Invalid SKU") || !strings.Contains(invalid, "Invalid cost estimate") {
		t.Fatal("billing input validation did not report both invalid inputs")
	}
	os.Remove(filepath.Join(work, "invalid.tf"))
	run(0, "apply", "-auto-approve", "-input=false", "-no-color")
	run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	assertNoSecretState := func() {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(work, "terraform.tfstate"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "fixture-secret-never-in-state") || strings.Contains(string(data), `\"password\"`) {
			t.Fatal("write-only secret persisted in state")
		}
	}
	assertNoSecretState()
	// Import all three identities and demand an immediately converged plan.
	for _, item := range []struct{ address, id string }{{"ibee_bucket.test", "fixture-bucket"}, {"ibee_secret_store.test", "store-1"}, {"ibee_secret.test", "secret-1"}} {
		run(0, "state", "rm", item.address)
		run(0, "import", "-input=false", "-no-color", item.address, item.id)
	}
	run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	config(true, 1)
	run(0, "apply", "-auto-approve", "-input=false", "-no-color")
	run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	assertNoSecretState()
	gateway.mu.Lock()
	gateway.bucket["is_public"] = false
	gateway.mu.Unlock()
	run(2, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	run(0, "apply", "-auto-approve", "-input=false", "-no-color")
	// Billing blocks new purchases, while refresh and teardown still work.
	gateway.mu.Lock()
	gateway.allowed = false
	creates := gateway.bucketCreates
	gateway.mu.Unlock()
	writeTestFile(t, filepath.Join(work, "denied.tf"), "resource \"ibee_bucket\" \"denied\" {\n name = \"denied-bucket\"\n region = \"fixture-region\"\n}\n")
	output := run(1, "apply", "-auto-approve", "-input=false", "-no-color")
	if !strings.Contains(output, "billing_denied") {
		t.Fatal("billing denial did not surface the upstream billing_denied error")
	}
	gateway.mu.Lock()
	after := gateway.bucketCreates
	gateway.mu.Unlock()
	if after != creates {
		t.Fatal("billing-denied create reached product API")
	}
	os.Remove(filepath.Join(work, "denied.tf"))
	run(0, "destroy", "-auto-approve", "-input=false", "-no-color")
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if gateway.bucket != nil || gateway.store["status"] != "archived" || gateway.secret["status"] != "soft_deleted" || gateway.secretVersion != 2 {
		t.Fatalf("unexpected final fixture state: bucket=%v store=%v secret=%v version=%d", gateway.bucket, gateway.store, gateway.secret, gateway.secretVersion)
	}
}
func writeTestFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

type terraformFixture struct {
	mu                           sync.Mutex
	allowed                      bool
	bucket, store, secret        map[string]any
	secretVersion, bucketCreates int
}

func (f *terraformFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("Authorization") != "Bearer fixture-only-token" || r.URL.Query().Get("workspace_id") != "workspace-fixture" {
		http.Error(w, "fixture auth/scope mismatch", 403)
		return
	}
	send := func(value any) { json.NewEncoder(w).Encode(value) }
	body := func() map[string]any {
		var value map[string]any
		if err := json.NewDecoder(r.Body).Decode(&value); err != nil {
			http.Error(w, "bad JSON", 400)
		}
		return value
	}
	path := r.URL.Path
	switch {
	case path == "/compute/sites":
		send(map[string]any{"sites": []map[string]any{{"site_id": "site-1", "name": "Fixture", "available": true}}})
	case path == "/compute/plans":
		send(map[string]any{"plans": []map[string]any{{"plan_id": "plan-1", "code": "cloud-fixture", "name": "Fixture", "cpu": 2, "ram_mb": 2048, "disk_gb": 40, "hourly_price_minor": 100, "monthly_price_minor": 72000, "currency": "INR", "selectable": true, "pricing_status": "priced", "billing_interval": "hourly", "gpu_count": 0}}})
	case path == "/compute/images":
		send(map[string]any{"images": []map[string]any{{"template_id": "image-1", "name": "Fixture", "os_distro": "ubuntu", "os_type": "linux"}}})
	case path == "/billing/resource-eligibility":
		reason := "eligible"
		if !f.allowed {
			reason = "initial_topup_required"
		}
		send(map[string]any{"organization_id": "org-fixture", "allowed": f.allowed, "reason": reason, "billing_mode": "PREPAID", "billing_state": "CURRENT", "currency": "INR", "evaluated_at": "2026-09-27T00:00:00Z"})
	case path == "/object-storage/buckets" && r.Method == "POST":
		if !f.allowed {
			w.WriteHeader(http.StatusPaymentRequired)
			send(map[string]any{"error": "billing_denied", "billing_reason": "insufficient_balance"})
			return
		}
		f.bucketCreates++
		b := body()
		f.bucket = map[string]any{"name": b["name"], "region": b["region"], "is_public": b["is_public"], "bucket_lock_enabled": b["object_lock_enabled"], "status": "active", "plan": "storage", "site_id": "site-1", "object_count": 0, "total_size": 0}
		send(f.bucket)
	case path == "/object-storage/buckets/fixture-bucket":
		if f.bucket == nil {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case "PATCH":
			f.bucket["is_public"] = body()["is_public"]
		case "DELETE":
			f.bucket = nil
			w.WriteHeader(204)
			return
		}
		send(f.bucket)
	case path == "/secret-store/stores" && r.Method == "POST":
		b := body()
		f.store = map[string]any{"id": "store-1", "name": b["name"], "description": b["description"], "store_key": "store-key-1", "status": "active"}
		send(f.store)
	case path == "/secret-store/stores/store-1":
		if f.store == nil {
			http.NotFound(w, r)
			return
		}
		if r.Method == "PATCH" {
			b := body()
			f.store["name"], f.store["description"] = b["name"], b["description"]
		}
		send(f.store)
	case path == "/secret-store/stores/store-1/archive":
		f.store["status"] = "archived"
		send(f.store)
	case path == "/secret-store/stores/store-1/secrets":
		if r.Method == "POST" {
			b := body()
			f.secret = map[string]any{"id": "secret-1", "store_id": "store-1", "secret_name": b["secret_name"], "store_key": "store-key-1", "status": "active"}
			f.secretVersion = 1
			send(f.secret)
			return
		}
		items := []map[string]any{}
		if f.secret != nil {
			items = append(items, f.secret)
		}
		send(map[string]any{"secrets": items, "total": len(items)})
	case path == "/secret-store/secrets/secret-1":
		if f.secret == nil {
			http.NotFound(w, r)
			return
		}
		if r.Method == "DELETE" {
			f.secret["status"] = "soft_deleted"
		}
		send(f.secret)
	case path == "/secret-store/secrets/secret-1/value":
		if r.Method == "PUT" {
			b := body()
			if b["cas"] != float64(f.secretVersion) {
				http.Error(w, "CAS conflict", 409)
				return
			}
			f.secretVersion++
		}
		send(map[string]any{"id": "secret-1", "data": map[string]any{"password": "fixture-secret-never-in-state"}, "metadata": map[string]any{"version": f.secretVersion}})
	default:
		http.Error(w, "unexpected fixture route: "+r.Method+" "+path, 404)
	}
}
