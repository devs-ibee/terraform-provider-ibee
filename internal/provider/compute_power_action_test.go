package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func powerActionConfig(t *testing.T, a *vmPowerAction, vmType, operation string, overrides ...map[string]attr.Value) tfsdk.Config {
	t.Helper()
	var sch action.SchemaResponse
	a.Schema(context.Background(), action.SchemaRequest{}, &sch)
	ts := map[string]attr.Type{}
	values := map[string]attr.Value{}
	for k, v := range sch.Schema.Attributes {
		ts[k] = v.GetType()
		values[k], _ = networkValue(v.GetType(), nil)
	}
	values["vm_id"], values["vm_type"], values["operation"] = types.StringValue("vm"), types.StringValue(vmType), types.StringValue(operation)
	for _, override := range overrides {
		for key, value := range override {
			values[key] = value
		}
	}
	raw, err := types.ObjectValueMust(ts, values).ToTerraformValue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return tfsdk.Config{Schema: sch.Schema, Raw: raw}
}

func TestVmPowerAction(t *testing.T) {
	for _, vmType := range []string{"cloud", "gpu"} {
		for _, operation := range []string{"start", "stop", "reboot"} {
			t.Run(vmType+"/"+operation, func(t *testing.T) {
				a := &vmPowerAction{}
				var body map[string]any
				key := ""
				a.client = computeTestClient(t, func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/compute/" + vmType + "-vms/vm/actions/" + operation:
						key = r.Header.Get("X-Idempotency-Key")
						_ = json.NewDecoder(r.Body).Decode(&body)
						computeJSON(w, operationAccepted{OperationID: "op", VmID: "vm", Status: "accepted"})
					case "/compute/operations/op":
						computeJSON(w, map[string]any{"status": "succeeded"})
					default:
						t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
						w.WriteHeader(500)
					}
				})
				resp := action.InvokeResponse{}
				a.Invoke(context.Background(), action.InvokeRequest{Config: powerActionConfig(t, a, vmType, operation)}, &resp)
				computeNoErrors(t, resp.Diagnostics)
				if !strings.HasPrefix(key, "tf-") || len(body) != 0 {
					t.Fatalf("key %q body %v: want a generated key and an empty body", key, body)
				}
			})
		}
	}
}

func TestVmPowerActionSendsForceAndKeyAndRelaysRefusal(t *testing.T) {
	a := &vmPowerAction{}
	a.client = computeTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.Header.Get("X-Idempotency-Key") != "my-key" || body["force"] != true {
			t.Errorf("key %q body %v", r.Header.Get("X-Idempotency-Key"), body)
		}
		w.WriteHeader(409)
		_, _ = w.Write([]byte(`{"detail":"VM operation 'stop' is already in progress"}`))
	})
	resp := action.InvokeResponse{}
	a.Invoke(context.Background(), action.InvokeRequest{Config: powerActionConfig(t, a, "cloud", "stop", map[string]attr.Value{"force": types.BoolValue(true), "idempotency_key": types.StringValue("my-key")})}, &resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics[0].Detail(), "already in progress") || !strings.Contains(resp.Diagnostics[0].Detail(), "my-key") {
		t.Fatalf("API refusal not relayed: %v", resp.Diagnostics)
	}
}
