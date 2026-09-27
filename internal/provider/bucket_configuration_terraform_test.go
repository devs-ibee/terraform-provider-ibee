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

// These fixtures verify the Terraform/plugin protocol, not object expiration,
// browser CORS enforcement, or notification delivery.
func TestTerraformLifecycleBucketConfiguration(t *testing.T) {
	if os.Getenv("IBEE_TF_TEST") != "1" {
		t.Skip("set IBEE_TF_TEST=1 for isolated Terraform bucket configuration checks")
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
	var mu sync.Mutex
	sections := map[string][]any{"cors": {}, "lifecycle": {}, "notifications": {}}
	puts := map[string]int{}
	deletes := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		section := strings.TrimPrefix(req.URL.Path, "/object-storage/buckets/config-fixture/")
		if _, ok := sections[section]; !ok || req.URL.Query().Get("workspace_id") != "fixture-workspace" {
			http.Error(w, "unexpected route", 500)
			return
		}
		collection := "rules"
		if section == "notifications" {
			collection = "configs"
		}
		switch req.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode(map[string]any{collection: sections[section]})
		case http.MethodPut:
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			items, ok := body[collection].([]any)
			if !ok || len(items) == 0 {
				http.Error(w, "missing configuration entries", 400)
				return
			}
			for _, item := range items {
				entry := item.(map[string]any)
				if section == "cors" && (entry["AllowedOrigins"] == nil || entry["allowed_origins"] != nil) {
					http.Error(w, "wrong CORS field casing", 400)
					return
				}
				if section == "lifecycle" {
					if date, ok := entry["expiration_date"].(string); ok {
						entry["expiration_date"] = strings.Replace(date, "Z", "+00:00", 1)
					}
				}
				if section == "notifications" {
					if _, exists := entry["s3_events"]; exists {
						http.Error(w, "computed field sent", 400)
						return
					}
					expanded := []any{}
					for _, event := range entry["events"].([]any) {
						if event == "upload" {
							expanded = append(expanded, "s3:ObjectCreated:*")
						} else if event == "delete" {
							expanded = append(expanded, "s3:ObjectRemoved:*")
						} else {
							http.Error(w, "invalid event category", 400)
							return
						}
					}
					entry["s3_events"] = expanded
				}
			}
			sections[section] = items
			puts[section]++
			fmt.Fprint(w, `{"detail":"stored"}`)
		case http.MethodDelete:
			sections[section] = []any{}
			deletes[section]++
			fmt.Fprint(w, `{"detail":"deleted"}`)
		default:
			http.Error(w, "unsupported method", 405)
		}
	}))
	defer server.Close()
	env := []string{}
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
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		code := 0
		if err != nil {
			if e, ok := err.(*exec.ExitError); ok {
				code = e.ExitCode()
			} else {
				t.Fatal(err)
			}
		}
		if code != want {
			t.Fatalf("terraform %v exited %d want %d:\n%s", args, code, want, out)
		}
		return string(out)
	}
	config := fmt.Sprintf(`terraform {
 required_version = ">= 1.11"
 required_providers { ibee = { source = "devs-ibee/ibee" } }
}
provider "ibee" {
 endpoint = %q
 token = "fixture-only"
 workspace_id = "fixture-workspace"
}
resource "ibee_bucket_cors" "test" {
 bucket_name = "config-fixture"
 rules = [
  { id = "web", allowed_origins = ["https://example.test"], allowed_methods = ["GET", "HEAD"], allowed_headers = ["*"], expose_headers = ["ETag"], max_age_seconds = 300 },
  { allowed_origins = ["*"], allowed_methods = ["POST", "PUT", "DELETE"] }
 ]
}
resource "ibee_bucket_lifecycle" "test" {
 bucket_name = "config-fixture"
 rules = [
  { status = "Disabled", prefix = "archive/", expiration_days = 30 },
  { status = "Disabled", expiration_date = "2100-01-01T00:00:00Z" }
 ]
}
resource "ibee_bucket_notifications" "test" {
 bucket_name = "config-fixture"
 configs = [
  { id = "webhook", events = ["upload", "delete"], filter = {prefix = "images/", suffix = ".png"}, webhook_url = "https://example.test/events" },
  { events = ["upload"] }
 ]
}
`, server.URL)
	writeTestFile(t, filepath.Join(dir, "main.tf"), config)
	run(0, "apply", "-auto-approve", "-input=false", "-no-color")
	run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	for _, section := range []string{"cors", "lifecycle", "notifications"} {
		address := "ibee_bucket_" + section + ".test"
		run(0, "state", "rm", address)
		run(0, "import", "-input=false", "-no-color", address, "config-fixture")
	}
	run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	config = strings.Replace(config, "max_age_seconds = 300", "max_age_seconds = 600", 1)
	config = strings.Replace(config, "expiration_days = 30", "expiration_days = 90", 1)
	config = strings.Replace(config, `events = ["upload", "delete"]`, `events = ["delete"]`, 1)
	config = strings.Replace(config, `, webhook_url = "https://example.test/events"`, "", 1)
	writeTestFile(t, filepath.Join(dir, "main.tf"), config)
	run(0, "apply", "-auto-approve", "-input=false", "-no-color")
	run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	mu.Lock()
	sections["cors"][0].(map[string]any)["AllowedOrigins"] = []any{"https://drift.example.test"}
	mu.Unlock()
	run(2, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	run(0, "apply", "-auto-approve", "-input=false", "-no-color")
	run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	run(0, "destroy", "-auto-approve", "-input=false", "-no-color")
	mu.Lock()
	defer mu.Unlock()
	for section, items := range sections {
		if len(items) != 0 || deletes[section] != 1 || puts[section] < 2 {
			t.Fatalf("%s config not exercised: entries=%v puts=%d deletes=%d", section, items, puts[section], deletes[section])
		}
	}
}
