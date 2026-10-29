package provider

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Runs the Container Registry resources and data source through the Terraform
// CLI against a loopback fixture. Never uses a real IBEE endpoint.
func TestTerraformContainerRegistry(t *testing.T) {
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
	env = append(env, "TF_CLI_CONFIG_FILE="+cli, "TF_IN_AUTOMATION=1", "CHECKPOINT_DISABLE=1", "HTTP_PROXY=http://127.0.0.1:1", "HTTPS_PROXY=http://127.0.0.1:1", "NO_PROXY=127.0.0.1,localhost")
	const secret = "fixture-registry-secret-never-shown"
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
		if strings.Contains(string(out), secret) {
			t.Fatalf("terraform %v printed the token secret:\n%s", args, out)
		}
		return string(out)
	}
	root, _ := filepath.Abs("../..")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", filepath.Join(bin, "terraform-provider-ibee"), ".")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	fixture := newRegistryFixture(t)
	server := httptest.NewServer(fixture)
	defer server.Close()
	writeTestFile(t, filepath.Join(work, "main.tf"), fmt.Sprintf(`terraform {
  required_providers {
    ibee = { source = "devs-ibee/ibee" }
  }
}
provider "ibee" {
  endpoint          = %q
  token             = "fixture-only-token"
  workspace_id      = "workspace"
  operation_timeout = "5s"
}
resource "ibee_container_registry" "apps" {
  name     = "acme-apps"
  sku_code = "REGISTRY-STORAGE-250GB"
}
resource "ibee_container_registry_access_token" "ci" {
  registry_id     = ibee_container_registry.apps.id
  name            = "ci"
  permission      = "PULL_PUSH"
  expires_in_days = 30
}
data "ibee_container_registries" "all" {
  depends_on = [ibee_container_registry.apps]
}
output "registry_count" {
  value = length(data.ibee_container_registries.all.registries)
}
`, server.URL))
	run(0, "validate", "-no-color")
	run(0, "apply", "-auto-approve", "-input=false", "-no-color")
	run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	state, err := os.ReadFile(filepath.Join(work, "terraform.tfstate"))
	if err != nil || !strings.Contains(string(state), secret) {
		t.Fatal("the secret returned at creation must be kept in state")
	}
	base := "/container-registry/registries/" + testRegistryID
	if fixture.count("POST /container-registry/registries") != 1 || fixture.count("POST "+base+"/access-tokens") != 1 {
		t.Fatal("apply must create the registry and the token once")
	}
	run(0, "destroy", "-auto-approve", "-input=false", "-no-color")
	if fixture.count("DELETE "+base+"/access-tokens/tok-1") != 1 || fixture.count("DELETE "+base) != 1 {
		t.Fatal("destroy must revoke the token and delete the registry")
	}
}
