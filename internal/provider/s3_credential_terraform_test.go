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

// Uses only local fixtures, including the one-time generated secret.
func TestTerraformLifecycleS3Credential(t *testing.T) {
	if os.Getenv("IBEE_TF_TEST") != "1" {
		t.Skip("set IBEE_TF_TEST=1 for local Terraform CLI lifecycle checks")
	}
	if _, err := exec.LookPath("terraform"); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin, work := filepath.Join(dir, "bin"), filepath.Join(dir, "work")
	for _, p := range []string{bin, work} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	build := exec.Command("go", "build", "-buildvcs=false", "-o", filepath.Join(bin, "terraform-provider-ibee"), ".")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	cli := filepath.Join(dir, "dev.tfrc")
	writeTestFile(t, cli, fmt.Sprintf("provider_installation {\n dev_overrides { \"devs-ibee/ibee\" = %q }\n direct {}\n}\n", bin))
	f := &s3TerraformFixture{allowed: true, keys: map[string]map[string]any{}}
	server := httptest.NewServer(f)
	defer server.Close()
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(key, "IBEE_") && !strings.HasPrefix(key, "TF_") {
			env = append(env, entry)
		}
	}
	env = append(env, "TF_CLI_CONFIG_FILE="+cli, "TF_IN_AUTOMATION=1", "CHECKPOINT_DISABLE=1")
	run := func(want int, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "terraform", args...)
		cmd.Dir, cmd.Env = work, env
		out, err := cmd.CombinedOutput()
		code := 0
		if err != nil {
			if e, ok := err.(*exec.ExitError); ok {
				code = e.ExitCode()
			} else {
				t.Fatal(err)
			}
		}
		if strings.Contains(string(out), "fixture-only-generated-s3-secret") {
			t.Fatal("generated credential secret appeared in Terraform output")
		}
		if code != want {
			t.Fatalf("terraform %v returned %d, want %d:\n%s", args, code, want, out)
		}
		return string(out)
	}
	config := func(permission, scope, buckets string) {
		writeTestFile(t, filepath.Join(work, "main.tf"), fmt.Sprintf(`terraform {
 required_version = ">= 1.11"
 required_providers { ibee = { source = "devs-ibee/ibee" } }
}
provider "ibee" {
 endpoint = %q
 token = "s3-fixture-token"
 workspace_id = "s3-fixture-workspace"
 organization_id = "s3-fixture-org"
}
resource "ibee_s3_credential" "test" {
 name = "application-reader"
 permission_type = %q
 bucket_scope = %q
 %s
}
`, server.URL, permission, scope, buckets))
	}
	stateBytes := func() []byte {
		t.Helper()
		out, err := os.ReadFile(filepath.Join(work, "terraform.tfstate"))
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	stateSecret := func() any {
		t.Helper()
		var state struct {
			Resources []struct {
				Instances []struct {
					Attributes map[string]any `json:"attributes"`
				} `json:"instances"`
			} `json:"resources"`
		}
		if err := json.Unmarshal(stateBytes(), &state); err != nil {
			t.Fatal(err)
		}
		return state.Resources[0].Instances[0].Attributes["secret_access_key"]
	}
	config("object_ro", "specific", `allowed_buckets = ["assets"]`)
	run(0, "validate", "-no-color")
	run(0, "apply", "-auto-approve", "-input=false", "-no-color")
	run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	if stateSecret() != "fixture-only-generated-s3-secret" {
		t.Fatal("creation-only secret did not persist in sensitive Terraform state")
	}
	for _, fault := range []string{"forbidden"} {
		before := string(stateBytes())
		f.mu.Lock()
		f.fault = fault
		f.mu.Unlock()
		run(1, "plan", "-input=false", "-no-color")
		if string(stateBytes()) != before {
			t.Fatal("failed refresh changed credential state")
		}
	}
	f.mu.Lock()
	f.fault = ""
	f.mu.Unlock()
	run(0, "state", "rm", "ibee_s3_credential.test")
	run(0, "import", "-input=false", "-no-color", "ibee_s3_credential.test", "AKIAFIXTURE1")
	run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	if stateSecret() != nil {
		t.Fatal("import fabricated a secret that cannot be recovered")
	}
	// Remote scope drift must be detected and corrected by replacing the key.
	f.mu.Lock()
	f.keys["AKIAFIXTURE1"]["permission_type"] = "object_rw"
	f.mu.Unlock()
	plan := run(2, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	if !strings.Contains(plan, "must be replaced") {
		t.Fatal("scope drift did not require replacement")
	}
	run(0, "apply", "-auto-approve", "-input=false", "-no-color")
	run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	// allowed_buckets left out of the configuration is not sent; the API's value is recorded.
	config("object_ro", "all", "")
	run(0, "apply", "-auto-approve", "-input=false", "-no-color")
	run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	f.mu.Lock()
	f.allowed = false
	before := f.admissions
	f.mu.Unlock()
	run(0, "destroy", "-auto-approve", "-input=false", "-no-color")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.admissions != before || f.creates != 3 {
		t.Errorf("unexpected billing/creation counts: %d/%d creates=%d", f.admissions, before, f.creates)
	}
	if len(f.keys) != 0 {
		t.Errorf("keys remain after destroy and replacement: %v", f.keys)
	}
}

type s3TerraformFixture struct {
	mu                  sync.Mutex
	allowed             bool
	keys                map[string]map[string]any
	creates, admissions int
	fault               string
}

func (f *s3TerraformFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("Authorization") != "Bearer s3-fixture-token" || r.URL.Query().Get("workspace_id") != "s3-fixture-workspace" {
		http.Error(w, "fixture auth/scope mismatch", 403)
		return
	}
	send := func(v any) { json.NewEncoder(w).Encode(v) }
	if r.URL.Path == "/billing/resource-eligibility" {
		f.admissions++
		reason := "eligible"
		if !f.allowed {
			reason = "initial_topup_required"
		}
		send(map[string]any{"organization_id": "s3-fixture-org", "allowed": f.allowed, "reason": reason, "billing_mode": "PREPAID", "billing_state": "CURRENT", "currency": "INR", "evaluated_at": "2026-09-27T00:00:00Z"})
		return
	}
	if r.URL.Path == "/object-storage/credentials" && r.Method == http.MethodPost {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !f.allowed {
			http.Error(w, "invalid body or denied admission", 422)
			return
		}
		// Like the API, apply defaults to what the request leaves out.
		if body["allowed_buckets"] == nil {
			body["allowed_buckets"] = []any{}
		}
		f.creates++
		id := fmt.Sprintf("AKIAFIXTURE%d", f.creates)
		body["access_key_id"], body["organization_id"], body["workspace_id"] = id, "s3-fixture-org", "s3-fixture-workspace"
		body["status"] = "active"
		f.keys[id] = body
		response := map[string]any{}
		for k, v := range body {
			response[k] = v
		}
		response["secret_access_key"] = "fixture-only-generated-s3-secret"
		send(response)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/object-storage/credentials/")
	id, revoke := strings.CutSuffix(id, "/revoke")
	key := f.keys[id]
	if key == nil {
		http.NotFound(w, r)
		return
	}
	if revoke && r.Method == http.MethodPost {
		key["status"] = "revoked"
		send(map[string]any{"success": true, "message": "revoked"})
		return
	}
	if r.Method == http.MethodDelete {
		if key["status"] != "revoked" {
			http.Error(w, "revoke the credential first", 400)
			return
		}
		delete(f.keys, id)
		send(map[string]any{"success": true, "message": "deleted"})
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "unexpected fixture method", 405)
		return
	}
	if f.fault == "forbidden" {
		http.Error(w, `{"detail":"access denied"}`, 403)
		return
	}
	send(key)
}
