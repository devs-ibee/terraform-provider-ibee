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

func TestTerraformLifecycleBlockVolume(t *testing.T) {
	if os.Getenv("IBEE_TF_TEST") != "1" {
		t.Skip("set IBEE_TF_TEST=1 for loopback-only Terraform CLI lifecycle tests")
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
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var volume map[string]any
	allowed := true
	var creates, resizes, deletes, admissions, attaches, detaches int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer block-fixture-token" || r.URL.Query().Get("workspace_id") != "fixture-workspace" {
			http.Error(w, "bad fixture auth", 403)
			return
		}
		switch r.URL.Path {
		case "/billing/resource-eligibility":
			admissions++
			blockEligibility(w, allowed, "INR")
		case "/block-storage/volumes":
			if r.Method != "POST" {
				http.Error(w, "bad method", 405)
				return
			}
			creates++
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			volume = blockVolumeFixture(int64(b["size_gb"].(float64)))
			volume["name"] = b["name"]
			computeJSON(w, map[string]any{"volume": volume, "operation": blockVolumeOperationAPI{ID: "create-op", VolumeID: "vol-1", Status: "succeeded"}})
		case "/block-storage/volumes/vol-1/attachments":
			attaches++
			volume["attachments"] = []any{map[string]any{"node_name": "node", "mode": "single-writer", "device_path": "/dev/drbd100"}}
			computeJSON(w, map[string]any{"volume": volume, "operation": blockVolumeOperationAPI{ID: "attach-op", VolumeID: "vol-1", Status: "succeeded"}})
		case "/block-storage/volumes/vol-1/detach":
			detaches++
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			if b["force"] != false || b["confirm_unmounted"] != true {
				t.Error("unsafe detach")
			}
			volume["attachments"] = []any{}
			computeJSON(w, map[string]any{"volume": volume, "operation": blockVolumeOperationAPI{ID: "detach-op", VolumeID: "vol-1", Status: "succeeded"}})
		case "/block-storage/volumes/vol-1/resize":
			resizes++
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			volume["size_gb"] = b["new_size_gb"]
			computeJSON(w, map[string]any{"volume": volume, "operation": blockVolumeOperationAPI{ID: "resize-op", VolumeID: "vol-1", Status: "succeeded"}})
		case "/block-storage/volumes/vol-1":
			if volume == nil {
				http.NotFound(w, r)
				return
			}
			if r.Method == "DELETE" {
				deletes++
				volume = nil
				computeJSON(w, map[string]any{"status": "deleted", "id": "vol-1"})
				return
			}
			computeJSON(w, volume)
		default:
			http.Error(w, "unexpected mock path: "+r.URL.Path, 404)
		}
	}))
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
				t.Fatal(err)
			}
		}
		if code != want {
			t.Fatalf("terraform %v returned %d, wanted %d\n%s", args, code, want, out)
		}
		return string(out)
	}
	confirmUnmounted := false
	config := func(size int) {
		writeTestFile(t, filepath.Join(work, "main.tf"), fmt.Sprintf(`terraform {
 required_providers { ibee = { source = "devs-ibee/ibee" } }
}
provider "ibee" {
 endpoint = %q
 token = "block-fixture-token"
 workspace_id = "fixture-workspace"
 operation_timeout = "2s"
}
resource "ibee_block_volume" "test" {
 name = "data"
 site_id = "site"
 sku_code = "BLOCK-STD"
 size_gb = %d
}
resource "ibee_block_volume_attachment" "test" {
 volume_id = ibee_block_volume.test.id
 node_name = "node"
 confirm_unmounted = %t
}
`, server.URL, size, confirmUnmounted))
	}
	config(100)
	run(0, "validate", "-no-color")
	run(0, "apply", "-auto-approve", "-input=false", "-no-color")
	run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	run(0, "state", "rm", "ibee_block_volume.test")
	run(0, "import", "-input=false", "-no-color", "ibee_block_volume.test", "vol-1")
	run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	run(0, "state", "rm", "ibee_block_volume_attachment.test")
	run(0, "import", "-input=false", "-no-color", "ibee_block_volume_attachment.test", "vol-1/node")
	run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	config(200)
	run(0, "apply", "-auto-approve", "-input=false", "-no-color")
	run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	mu.Lock()
	if creates != 1 || resizes != 1 {
		t.Fatalf("expansion must not replace volume: creates=%d resizes=%d", creates, resizes)
	}
	volume["name"] = "portal-change"
	mu.Unlock()
	run(2, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	mu.Lock()
	volume["name"] = "data"
	mu.Unlock()
	confirmUnmounted = true
	config(200)
	run(0, "apply", "-auto-approve", "-input=false", "-no-color")
	mu.Lock()
	allowed = false
	calls := admissions
	mu.Unlock()
	run(0, "destroy", "-auto-approve", "-input=false", "-no-color")
	mu.Lock()
	defer mu.Unlock()
	if volume != nil || deletes != 1 || admissions != calls || attaches != 1 || detaches != 1 {
		t.Fatalf("destroy must release volume without purchase check: %d %d", deletes, admissions)
	}
}
