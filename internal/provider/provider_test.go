package provider

import (
	"github.com/hashicorp/terraform-plugin-framework/types"
	"testing"
	"time"
)

func providerTestConfig(t *testing.T) ibeeProviderModel {
	t.Helper()
	for _, key := range []string{"IBEE_ENDPOINT", "IBEE_BASE_URL", "IBEE_ENV", "IBEE_TOKEN", "IBEE_WORKSPACE_ID", "IBEE_ORGANIZATION_ID"} {
		t.Setenv(key, "")
	}
	return ibeeProviderModel{Token: types.StringValue("test-token"), WorkspaceID: types.StringValue("workspace-1")}
}
func TestProviderConfigDefaultsAndPrecedence(t *testing.T) {
	cfg := providerTestConfig(t)
	client, diags := configuredClient(cfg)
	if diags.HasError() || client.endpoint != "https://api.ibee.ai/v1" || client.operationTimeout != 20*time.Minute {
		t.Fatalf("defaults: %+v %v", client, diags)
	}
	t.Setenv("IBEE_ENV", "dev")
	client, diags = configuredClient(cfg)
	if diags.HasError() || client.endpoint != "https://api.ibee.co.in/v1" {
		t.Fatalf("dev: %v", diags)
	}
	t.Setenv("IBEE_BASE_URL", "https://base.example/v1")
	t.Setenv("IBEE_ENDPOINT", "https://endpoint.example/v1")
	client, diags = configuredClient(cfg)
	if diags.HasError() || client.endpoint != "https://endpoint.example/v1" {
		t.Fatalf("env precedence: %v", diags)
	}
	cfg.Endpoint = types.StringValue("https://explicit.example/v1/")
	cfg.RequestTimeout = types.StringValue("3s")
	client, diags = configuredClient(cfg)
	if diags.HasError() || client.endpoint != "https://explicit.example/v1" || client.http.Timeout != 3*time.Second {
		t.Fatalf("explicit precedence: %v", diags)
	}
}
func TestProviderRejectsInvalidConfiguration(t *testing.T) {
	for _, name := range []string{"unknown", "missing-token", "missing-workspace", "insecure", "credentials", "query", "fragment", "duration", "environment-mismatch", "uppercase-environment", "empty-endpoint", "invalid-env"} {
		t.Run(name, func(t *testing.T) {
			cfg := providerTestConfig(t)
			switch name {
			case "unknown":
				cfg.Token = types.StringUnknown()
			case "missing-token":
				cfg.Token = types.StringNull()
			case "missing-workspace":
				cfg.WorkspaceID = types.StringValue(" ")
			case "insecure":
				cfg.Endpoint = types.StringValue("http://api.ibee.ai/v1")
			case "credentials":
				cfg.Endpoint = types.StringValue("https://user:pass@api.ibee.ai/v1")
			case "query":
				cfg.Endpoint = types.StringValue("https://api.ibee.ai/v1?token=x")
			case "fragment":
				cfg.Endpoint = types.StringValue("https://api.ibee.ai/v1#x")
			case "duration":
				cfg.RequestTimeout = types.StringValue("0s")
			case "environment-mismatch":
				cfg.Token = types.StringValue("ibee_dev_key_test")
			case "uppercase-environment":
				cfg.Endpoint = types.StringValue("https://API.IBEE.AI./v1")
				cfg.Token = types.StringValue("ibee_dev_key_test")
			case "empty-endpoint":
				t.Setenv("IBEE_ENDPOINT", "https://api.ibee.co.in/v1")
				cfg.Endpoint = types.StringValue(" ")
			case "invalid-env":
				t.Setenv("IBEE_ENV", "staging")
			}
			if client, diags := configuredClient(cfg); !diags.HasError() || client != nil {
				t.Fatalf("invalid configuration accepted: %v", diags)
			}
		})
	}
}
