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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Real Terraform verifies protocol, diff, import and state behavior against
// canonical CDN service fixtures. No account credentials or live API are used.
func TestTerraformLifecycleCDN(t *testing.T) {
	if os.Getenv("IBEE_TF_TEST") != "1" {
		t.Skip("set IBEE_TF_TEST=1 for isolated Terraform CLI lifecycle checks")
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
	work := filepath.Join(dir, "work")
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
	f := &cdnTerraformFixture{allowed: true, distributions: map[string]map[string]any{}}
	server := httptest.NewServer(f)
	defer server.Close()
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
			t.Fatalf("terraform %v returned %d, expected %d:\n%s", args, code, want, out)
		}
		return string(out)
	}
	bucketOriginID := "existing-fixture-bucket"
	config := func(enabled bool, policy, originURL, index string) {
		writeTestFile(t, filepath.Join(work, "main.tf"), fmt.Sprintf(`terraform {
 required_version = ">= 1.11"
 required_providers { ibee = { source = "devs-ibee/ibee" } }
}
provider "ibee" {
 endpoint = %q
 token = "cdn-fixture-only-token"
 workspace_id = "cdn-fixture-workspace"
 organization_id = "cdn-fixture-org"
 operation_timeout = "5s"
}
resource "ibee_cdn_origin" "test" {
 name = "fixture-origin"
 origin_url = %q
}
resource "ibee_cdn_distribution" "custom" {
 name = "custom-distribution"
 origin_type = "custom"
 origin_id = ibee_cdn_origin.test.id
 cache_policy = %q
 enabled = %t
}
resource "ibee_cdn_distribution" "bucket" {
 name = "bucket-distribution"
 origin_type = "bucket"
 origin_id = %q
}
resource "ibee_cdn_website" "test" {
 distribution_id = ibee_cdn_distribution.bucket.id
 index_document = %q
}
resource "ibee_cdn_domain" "test" {
 distribution_id = ibee_cdn_distribution.custom.id
 domain = "cdn.fixture.example"
}
`, server.URL, originURL, policy, enabled, bucketOriginID, index))
	}
	assertAttribute := func(resourceType, name, attribute, want string) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(work, "terraform.tfstate"))
		if err != nil {
			t.Fatal(err)
		}
		var saved struct {
			Resources []struct {
				Type      string `json:"type"`
				Name      string `json:"name"`
				Instances []struct {
					Attributes map[string]any `json:"attributes"`
				} `json:"instances"`
			} `json:"resources"`
		}
		if err := json.Unmarshal(data, &saved); err != nil {
			t.Fatal(err)
		}
		for _, resource := range saved.Resources {
			if resource.Type == resourceType && resource.Name == name && len(resource.Instances) > 0 {
				if got := resource.Instances[0].Attributes[attribute]; got != want {
					t.Fatalf("%s.%s.%s=%v, want %q", resourceType, name, attribute, got, want)
				}
				return
			}
		}
		t.Fatalf("resource %s.%s missing from saved state", resourceType, name)
	}
	config(false, "static-assets", "https://origin.fixture.example", "index.html")
	run(0, "validate", "-no-color")
	run(0, "apply", "-auto-approve", "-input=false", "-no-color")
	run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	assertAttribute("ibee_cdn_distribution", "bucket", "origin_id", "existing-fixture-bucket")
	assertAttribute("ibee_cdn_distribution", "bucket", "canonical_origin_id", "canonical-bucket-7")
	assertAttribute("ibee_cdn_domain", "test", "cname_name", "cdn.fixture.example")
	assertAttribute("ibee_cdn_domain", "test", "cname_target", "custom-distribution.cdn.fixture.example")
	f.mu.Lock()
	if f.distributions["custom-distribution"]["enabled"] != false || f.disablePatches != 1 || f.createSentEnabled || f.domain["status"] != "pending_validation" {
		t.Errorf("creation failed disabled/update-only/pending-domain semantics: %+v", f)
	}
	f.mu.Unlock()
	// Native provider actions are available from Terraform 1.14. Older CLI
	// versions still exercise all managed resources in the rest of this test.
	var version struct {
		Version string `json:"terraform_version"`
	}
	if err := json.Unmarshal([]byte(run(0, "version", "-json")), &version); err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(version.Version, ".")
	major, _ := strconv.Atoi(parts[0])
	minor := 0
	if len(parts) > 1 {
		minor, _ = strconv.Atoi(parts[1])
	}
	if major > 1 || (major == 1 && minor >= 14) {
		actions := filepath.Join(work, "actions.tf")
		writeTestFile(t, actions, `action "ibee_cdn_purge" "test" {
 config {
  distribution_id = ibee_cdn_distribution.custom.id
  mode = "url"
  paths = ["/assets/example.js"]
 }
}
action "ibee_cdn_verify_domain" "test" {
 config {
  distribution_id = ibee_cdn_distribution.custom.id
  domain = ibee_cdn_domain.test.domain
 }
}
`)
		run(0, "validate", "-no-color")
		run(0, "apply", "-invoke=action.ibee_cdn_purge.test", "-auto-approve", "-input=false", "-no-color")
		pending := run(0, "apply", "-invoke=action.ibee_cdn_verify_domain.test", "-auto-approve", "-input=false", "-no-color")
		if !strings.Contains(pending, "Domain is still pending") {
			t.Fatal("native verification action did not report pending DNS")
		}
		f.mu.Lock()
		if f.purges != 1 || f.verifications != 1 {
			t.Errorf("action invocation did not reach exact endpoints: purge=%d verify=%d", f.purges, f.verifications)
		}
		f.mu.Unlock()
		if err := os.Remove(actions); err != nil {
			t.Fatal(err)
		}
	}
	// Imports discover the canonical ID, rather than reconstructing a former
	// configuration alias. Align configuration before importing that resource.
	bucketOriginID = "canonical-bucket-7"
	config(false, "static-assets", "https://origin.fixture.example", "index.html")
	for _, item := range []struct{ address, id string }{
		{"ibee_cdn_origin.test", "origin-1"},
		{"ibee_cdn_distribution.custom", "custom-distribution"},
		{"ibee_cdn_distribution.bucket", "bucket-distribution"},
		{"ibee_cdn_website.test", "bucket-distribution"},
		{"ibee_cdn_domain.test", "custom-distribution/cdn.fixture.example"},
	} {
		run(0, "state", "rm", item.address)
		run(0, "import", "-input=false", "-no-color", item.address, item.id)
	}
	run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	assertAttribute("ibee_cdn_distribution", "bucket", "origin_id", "canonical-bucket-7")
	assertAttribute("ibee_cdn_domain", "test", "cname_target", "custom-distribution.cdn.fixture.example")
	config(true, "media", "https://new-origin.fixture.example/assets", "home.html")
	run(0, "apply", "-auto-approve", "-input=false", "-no-color")
	run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	f.mu.Lock()
	if f.distributions["custom-distribution"]["enabled"] != true || f.distributions["custom-distribution"]["cache_policy"] != "media" || f.origin["origin_url"] != "https://new-origin.fixture.example/assets" || f.website["index_document"] != "home.html" {
		t.Error("mutable CDN fields did not converge")
	}
	f.distributions["custom-distribution"]["cache_policy"] = "short"
	f.mu.Unlock()
	run(2, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	run(0, "apply", "-auto-approve", "-input=false", "-no-color")
	// Missing fields and access failures must retain all managed identities.
	for _, faultPath := range []string{"/cdn/origins/origin-1", "/cdn/distributions/custom-distribution", "/cdn/distributions/bucket-distribution/website-config", "/cdn/distributions/custom-distribution/custom-domains/cdn.fixture.example"} {
		for _, kind := range []string{"malformed", "forbidden"} {
			before, err := os.ReadFile(filepath.Join(work, "terraform.tfstate"))
			if err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			f.faultPath, f.faultKind = faultPath, kind
			f.mu.Unlock()
			out := run(1, "plan", "-detailed-exitcode", "-input=false", "-no-color")
			if !strings.Contains(out, "Failed to read") {
				t.Fatalf("read failure lacks diagnostic: %s", out)
			}
			after, err := os.ReadFile(filepath.Join(work, "terraform.tfstate"))
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatalf("failed %s read changed saved state for %s", kind, faultPath)
			}
			f.mu.Lock()
			f.faultPath, f.faultKind = "", ""
			f.mu.Unlock()
		}
	}
	run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	// Website destroy disables routing while retaining its distribution and data.
	run(0, "destroy", "-target=ibee_cdn_website.test", "-auto-approve", "-input=false", "-no-color")
	f.mu.Lock()
	if f.website["enabled"] != false || f.distributions["bucket-distribution"]["status"] == "deleted" {
		t.Error("website destruction removed its parent instead of disabling routing")
	}
	f.mu.Unlock()
	run(0, "apply", "-auto-approve", "-input=false", "-no-color")
	// Billing denial prevents enabling delivery; disabling remains permitted.
	config(false, "media", "https://new-origin.fixture.example/assets", "home.html")
	f.mu.Lock()
	f.allowed = false
	beforeAdmission := f.admissions
	f.mu.Unlock()
	run(0, "apply", "-auto-approve", "-input=false", "-no-color")
	f.mu.Lock()
	if f.admissions != beforeAdmission {
		t.Error("disabling CDN checked purchase admission")
	}
	f.mu.Unlock()
	config(true, "media", "https://new-origin.fixture.example/assets", "home.html")
	denied := run(1, "apply", "-auto-approve", "-input=false", "-no-color")
	if !strings.Contains(denied, "Add Credits") {
		t.Fatal("denied CDN enable lacks billing recovery message")
	}
	f.mu.Lock()
	if f.distributions["custom-distribution"]["enabled"] != false {
		t.Error("billing-denied enable reached mutation")
	}
	beforeAdmission = f.admissions
	f.mu.Unlock()
	run(0, "destroy", "-auto-approve", "-input=false", "-no-color")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.origin != nil || f.domain != nil || f.website["enabled"] != false || f.admissions != beforeAdmission {
		t.Errorf("cleanup/blocked billing invariant failed: origin=%v domain=%v website=%v admissions=%d/%d", f.origin, f.domain, f.website, f.admissions, beforeAdmission)
	}
	for id, d := range f.distributions {
		if d["status"] != "deleted" {
			t.Errorf("distribution %s was not soft-deleted: %v", id, d)
		}
	}
}

type cdnTerraformFixture struct {
	mu                         sync.Mutex
	allowed                    bool
	admissions, disablePatches int
	createSentEnabled          bool
	distributions              map[string]map[string]any
	origin, website, domain    map[string]any
	faultPath, faultKind       string
	purges, verifications      int
}

func (f *cdnTerraformFixture) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if req.Header.Get("Authorization") != "Bearer cdn-fixture-only-token" || req.URL.Query().Get("workspace_id") != "cdn-fixture-workspace" {
		http.Error(w, "fixture auth/scope mismatch", 403)
		return
	}
	if req.Method == http.MethodGet && req.URL.Path == f.faultPath {
		if f.faultKind == "forbidden" {
			http.Error(w, `{"detail":"access denied"}`, 403)
			return
		}
		// Preserve identity, omit fields needed to reconstruct managed settings.
		switch req.URL.Path {
		case "/cdn/origins/origin-1":
			fmt.Fprint(w, `{"id":"origin-1"}`)
		case "/cdn/distributions/custom-distribution":
			fmt.Fprint(w, `{"id":"custom-distribution"}`)
		case "/cdn/distributions/bucket-distribution/website-config":
			fmt.Fprint(w, `{"distribution_id":"bucket-distribution"}`)
		default:
			fmt.Fprint(w, `{"domain":"cdn.fixture.example"}`)
		}
		return
	}
	send := func(v any) { json.NewEncoder(w).Encode(v) }
	body := func() map[string]any {
		var out map[string]any
		if err := json.NewDecoder(req.Body).Decode(&out); err != nil {
			http.Error(w, "invalid JSON", 400)
		}
		return out
	}
	p := req.URL.Path
	switch {
	case p == "/billing/resource-eligibility":
		f.admissions++
		reason := "eligible"
		if !f.allowed {
			reason = "initial_topup_required"
		}
		send(map[string]any{"organization_id": "cdn-fixture-org", "allowed": f.allowed, "reason": reason, "billing_mode": "PREPAID", "billing_state": "CURRENT", "currency": "INR", "evaluated_at": "2026-09-27T00:00:00Z"})
	case p == "/cdn/origins" && req.Method == http.MethodPost:
		f.origin = body()
		f.origin["id"] = "origin-1"
		f.origin["default_url"] = "https://origin.fixture.cdn.example"
		send(f.origin)
	case p == "/cdn/origins/origin-1":
		if f.origin == nil {
			http.NotFound(w, req)
			return
		}
		if req.Method == http.MethodDelete {
			f.origin = nil
			w.WriteHeader(204)
			return
		}
		if req.Method == http.MethodPatch {
			for k, v := range body() {
				f.origin[k] = v
			}
		}
		send(f.origin)
	case p == "/cdn/distributions" && req.Method == http.MethodPost:
		b := body()
		if _, exists := b["enabled"]; exists {
			f.createSentEnabled = true
			http.Error(w, "enabled is update-only", 422)
			return
		}
		id, _ := b["name"].(string)
		b["id"] = id
		b["enabled"] = true
		b["status"] = "active"
		b["default_domain"] = id + ".cdn.fixture.example"
		b["default_url"] = "https://" + id + ".cdn.fixture.example"
		if b["origin_type"] == "bucket" {
			b["origin_id"] = "canonical-bucket-7"
			b["bucket_name"] = "existing-fixture-bucket"
		}
		f.distributions[id] = b
		send(b)
	case strings.HasSuffix(p, "/website-config"):
		if req.Method == http.MethodPut {
			f.website = body()
			f.website["distribution_id"] = "bucket-distribution"
			f.website["enabled"] = true
		}
		if f.website == nil {
			http.NotFound(w, req)
			return
		}
		if req.Method == http.MethodDelete {
			f.website["enabled"] = false
		}
		send(f.website)
	case strings.HasSuffix(p, "/custom-domains") && req.Method == http.MethodPost:
		f.domain = body()
		f.domain["status"] = "pending_validation"
		f.domain["validation"] = map[string]any{"cname_record": map[string]any{"type": "CNAME", "name": "cdn.fixture.example", "value": "custom-distribution.cdn.fixture.example"}}
		f.domain["created_at"] = "2026-09-27T00:00:00Z"
		send(f.domain)
	case p == "/cdn/distributions/custom-distribution/purge" && req.Method == http.MethodPost:
		request := body()
		paths, ok := request["paths"].([]any)
		if request["mode"] != "url" || !ok || len(paths) != 1 || paths[0] != "/assets/example.js" {
			http.Error(w, "unexpected purge payload", 422)
			return
		}
		f.purges++
		send(map[string]any{"success": true, "mode": "url"})
	case p == "/cdn/distributions/custom-distribution/custom-domains/cdn.fixture.example/verify" && req.Method == http.MethodPost:
		f.verifications++
		send(f.domain)
	case strings.Contains(p, "/custom-domains/"):
		if f.domain == nil {
			http.NotFound(w, req)
			return
		}
		if req.Method == http.MethodDelete {
			f.domain = nil
			send(map[string]any{"domain": "cdn.fixture.example", "message": "removed"})
			return
		}
		send(f.domain)
	case strings.HasPrefix(p, "/cdn/distributions/"):
		id := strings.TrimPrefix(p, "/cdn/distributions/")
		d := f.distributions[id]
		if d == nil {
			http.NotFound(w, req)
			return
		}
		if req.Method == http.MethodDelete {
			d["status"] = "deleted"
			d["enabled"] = false
		}
		if req.Method == http.MethodPatch {
			for k, v := range body() {
				if k == "enabled" && v == false {
					f.disablePatches++
				}
				d[k] = v
			}
			if d["enabled"] == true {
				d["status"] = "active"
			} else {
				d["status"] = "disabled"
			}
		}
		send(d)
	default:
		http.Error(w, "unexpected fixture route "+req.Method+" "+p, 500)
	}
}
