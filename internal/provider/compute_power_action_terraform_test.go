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

// Exercise actual action RPCs through Terraform, using only a locally built
// provider and loopback fixtures. This test never provisions a managed resource.
func TestTerraformVmPowerAction(t *testing.T) {
	if os.Getenv("IBEE_TF_TEST") != "1" {
		t.Skip("set IBEE_TF_TEST=1 for loopback-only Terraform power action checks")
	}
	terraform, err := exec.LookPath("terraform")
	if err != nil {
		t.Fatal("Terraform 1.15+ is required for the power action CLI fixture")
	}
	dir := t.TempDir()
	bin, work := filepath.Join(dir, "bin"), filepath.Join(dir, "work")
	for _, path := range []string{bin, work} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	cli := filepath.Join(dir, "dev.tfrc")
	// No registry installation fallback or user credentials helper.
	writeTestFile(t, cli, fmt.Sprintf("provider_installation {\n dev_overrides { \"devs-ibee/ibee\" = %q }\n}\n", bin))
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "IBEE_") || strings.HasPrefix(key, "TF_") || strings.HasSuffix(strings.ToUpper(key), "_PROXY") || key == "CHECKPOINT_DISABLE" {
			continue
		}
		env = append(env, entry)
	}
	// Fail closed for accidental external HTTP; loopback fixture requests bypass
	// these proxies. Terraform gets no inherited provider settings or CLI flags.
	env = append(env, "TF_CLI_CONFIG_FILE="+cli, "TF_IN_AUTOMATION=1", "CHECKPOINT_DISABLE=1",
		"HTTP_PROXY=http://127.0.0.1:1", "HTTPS_PROXY=http://127.0.0.1:1", "NO_PROXY=127.0.0.1,localhost")
	run := func(t *testing.T, want int, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, terraform, args...)
		cmd.Dir, cmd.Env = work, env
		out, err := cmd.CombinedOutput()
		code := 0
		if err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				code = exit.ExitCode()
			} else {
				t.Fatalf("terraform %v: %v\n%s", args, err, out)
			}
		}
		if code != want {
			t.Fatalf("terraform %v returned %d, want %d:\n%s", args, code, want, out)
		}
		return string(out)
	}
	var version struct {
		Version string `json:"terraform_version"`
	}
	if err := json.Unmarshal([]byte(run(t, 0, "version", "-json")), &version); err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(version.Version, ".")
	if len(parts) < 2 {
		t.Fatalf("invalid Terraform version %q", version.Version)
	}
	major, majorErr := strconv.Atoi(parts[0])
	minor, minorErr := strconv.Atoi(parts[1])
	if majorErr != nil || minorErr != nil {
		t.Fatalf("invalid Terraform version %q", version.Version)
	}
	if major < 1 || (major == 1 && minor < 15) {
		t.Skipf("Terraform %s is older than this action fixture's minimum 1.15; no power action invocations were exercised", version.Version)
	}
	t.Logf("exercising native power actions with Terraform %s", version.Version)
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-o", filepath.Join(bin, "terraform-provider-ibee"), ".")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	fixture := &powerActionTerraformFixture{}
	server := httptest.NewServer(fixture)
	defer server.Close()
	config := fmt.Sprintf(`terraform {
 required_version = ">= 1.15"
 required_providers { ibee = { source = "devs-ibee/ibee" } }
}
provider "ibee" {
 endpoint = %q
 token = "power-fixture-only-token"
 workspace_id = "power-fixture-workspace"
 operation_timeout = "3s"
}
`, server.URL)
	for _, vmType := range []string{"cloud", "gpu"} {
		for _, operation := range []string{"start", "stop", "reboot"} {
			config += fmt.Sprintf(`
action "ibee_vm_power" "%[1]s_%[2]s" {
 config {
  vm_id = "%[1]s-vm"
  vm_type = "%[1]s"
  operation = "%[2]s"
  idempotency_key = "fixture-%[1]s-%[2]s"
 }
}
`, vmType, operation)
		}
	}
	writeTestFile(t, filepath.Join(work, "main.tf"), config)
	run(t, 0, "validate", "-no-color")
	run(t, 0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	run(t, 0, "apply", "-auto-approve", "-input=false", "-no-color")
	run(t, 0, "apply", "-refresh-only", "-auto-approve", "-input=false", "-no-color")
	fixture.mu.Lock()
	requests := append([]string(nil), fixture.requests...)
	fixture.mu.Unlock()
	if len(requests) != 0 {
		t.Fatalf("ordinary plan/apply/refresh unexpectedly invoked the action: %v", requests)
	}
	for _, vmType := range []string{"cloud", "gpu"} {
		for _, tc := range []struct {
			name, operation, status, diagnostic string
			allowed                             bool
		}{
			{"normalized start", "start", " \tStOpPeD\n", "", true},
			{"normalized stop without billing", "stop", " \tRuNnInG\n", "", false},
			{"normalized reboot", "reboot", " \tRUNNING\n", "", true},
			{"already running", "start", "running", "Invalid VM power state", false},
			{"already stopped", "stop", "stopped", "Invalid VM power state", false},
			{"reboot stopped", "reboot", "stopped", "Invalid VM power state", false},
			{"start transitional", "start", "starting", "Invalid VM power state", false},
			{"stop transitional", "stop", "stopping", "Invalid VM power state", false},
			{"reboot transitional", "reboot", "rebooting", "Invalid VM power state", false},
			{"unknown", "start", "unknown", "Invalid VM power state", false},
			{"missing status", "start", "", "Invalid VM response", false},
			{"billing denied", "start", "stopped", "VM power request failed", false},
		} {
			t.Run(vmType+"/"+tc.name, func(t *testing.T) {
				fixture.mu.Lock()
				fixture.vmType, fixture.operation, fixture.status, fixture.allowed = vmType, tc.operation, tc.status, tc.allowed
				fixture.requests = nil
				fixture.mu.Unlock()
				wantCode := 0
				if tc.diagnostic != "" {
					wantCode = 1
				}
				out := run(t, wantCode, "apply", "-invoke=action.ibee_vm_power."+vmType+"_"+tc.operation, "-auto-approve", "-input=false", "-no-color")
				if tc.diagnostic != "" && !strings.Contains(out, tc.diagnostic) {
					t.Fatalf("expected %q diagnostic:\n%s", tc.diagnostic, out)
				}
				endpoint := "/compute/" + vmType + "-vms/" + vmType + "-vm"
				wantRequests := []string{"GET " + endpoint}
				if tc.diagnostic == "VM power request failed" {
					wantRequests = append(wantRequests, "POST "+endpoint+"/actions/"+tc.operation)
				}
				if tc.diagnostic == "" {
					wantRequests = append(wantRequests, "POST "+endpoint+"/actions/"+tc.operation, "GET /compute/operations/power-op", "GET "+endpoint)
				}
				fixture.mu.Lock()
				got := append([]string(nil), fixture.requests...)
				fixture.mu.Unlock()
				if strings.Join(got, "\n") != strings.Join(wantRequests, "\n") {
					t.Fatalf("unexpected power action requests:\ngot %v\nwant %v", got, wantRequests)
				}
			})
		}
	}
}

type powerActionTerraformFixture struct {
	mu                        sync.Mutex
	vmType, operation, status string
	allowed                   bool
	requests                  []string
}

func (f *powerActionTerraformFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	if r.Header.Get("Authorization") != "Bearer power-fixture-only-token" || r.URL.Query().Get("workspace_id") != "power-fixture-workspace" {
		http.Error(w, "fixture auth/scope mismatch", http.StatusForbidden)
		return
	}
	vmID := f.vmType + "-vm"
	endpoint := "/compute/" + f.vmType + "-vms/" + vmID
	switch r.Method + " " + r.URL.Path {
	case "GET " + endpoint:
		computeJSON(w, map[string]any{"_id": vmID, "status": f.status})
	case "POST /billing/resource-eligibility":
		blockEligibility(w, f.allowed, "INR")
	case "POST " + endpoint + "/actions/" + f.operation:
		if !f.allowed && f.operation != "stop" {
			w.WriteHeader(http.StatusPaymentRequired)
			computeJSON(w, map[string]any{"error": "billing_denied", "billing_reason": "insufficient_balance"})
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["force"] != false || body["requested_by"] != "terraform" || r.Header.Get("X-Idempotency-Key") != "fixture-"+f.vmType+"-"+f.operation {
			http.Error(w, "unsafe power request or changed idempotency key", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		computeJSON(w, operationAccepted{VmID: vmID, OperationID: "power-op", Status: "accepted"})
	case "GET /compute/operations/power-op":
		f.status = " \tRuNnInG\n"
		if f.operation == "stop" {
			f.status = " \tStOpPeD\n"
		}
		computeJSON(w, map[string]any{"status": "succeeded"})
	default:
		http.Error(w, "unexpected power fixture request", http.StatusBadRequest)
	}
}
