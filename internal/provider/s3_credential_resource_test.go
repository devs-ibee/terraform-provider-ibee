package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func s3TestModel() s3CredentialResourceModel {
	return s3CredentialResourceModel{ID: types.StringValue("AKIAFIXTURE"), Name: types.StringValue("application-reader"), PermissionType: types.StringValue("object_ro"), BucketScope: types.StringValue("specific"), AllowedBuckets: types.SetValueMust(types.StringType, []attr.Value{types.StringValue("assets")}), SecretAccessKey: types.StringNull(), Status: types.StringValue("active")}
}

func s3TestMetadata(status string) map[string]any {
	return map[string]any{"access_key_id": "AKIAFIXTURE", "organization_id": "org-test", "workspace_id": "workspace-test", "name": "application-reader", "permission_type": "object_ro", "bucket_scope": "specific", "allowed_buckets": []string{"assets"}, "status": status}
}

func TestS3CredentialLifecycleAndScopedDrift(t *testing.T) {
	const generatedSecret = "fixture-generated-s3-secret"
	metadata := s3TestMetadata("active")
	billingCalls := 0
	created, revoked := false, false
	r := &s3CredentialResource{client: storageTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.URL.Path == "/billing/resource-eligibility":
			billingCalls++
			storageTestBilling(w, true)
		case req.URL.Path == "/object-storage/credentials" && req.Method == http.MethodPost:
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["permission_type"] != "object_ro" || body["bucket_scope"] != "specific" || len(body["allowed_buckets"].([]any)) != 1 {
				t.Errorf("scope changed during creation: %v", body)
			}
			if billingCalls != 0 {
				t.Error("creation consulted billing eligibility before the mutation")
			}
			created = true
			out := s3TestMetadata("active")
			out["secret_access_key"] = generatedSecret
			json.NewEncoder(w).Encode(out)
		case strings.HasSuffix(req.URL.Path, "/revoke"):
			if req.Method != http.MethodPost {
				t.Error("revocation did not use explicit revoke")
			}
			revoked = true
			metadata["status"] = "revoked"
			fmt.Fprint(w, `{"success":true,"message":"revoked"}`)
		case req.Method == http.MethodGet:
			json.NewEncoder(w).Encode(metadata)
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
		}
	})}
	model := s3TestModel()
	model.ID = types.StringUnknown()
	model.SecretAccessKey = types.StringUnknown()
	plan := storageTestState(t, r, model)
	create := resource.CreateResponse{State: plan}
	r.Create(storageTestContext, resource.CreateRequest{Plan: storageTestPlan(plan)}, &create)
	storageTestCheck(t, create.Diagnostics)
	var saved s3CredentialResourceModel
	storageTestCheck(t, create.State.Get(storageTestContext, &saved))
	if !created || saved.SecretAccessKey.ValueString() != generatedSecret {
		t.Fatal("one-time secret not saved")
	}
	if !storageTestSchema(r).Attributes["secret_access_key"].IsSensitive() {
		t.Fatal("secret state field is not sensitive")
	}
	metadata["permission_type"] = "object_rw"
	read := resource.ReadResponse{State: create.State}
	r.Read(storageTestContext, resource.ReadRequest{State: create.State}, &read)
	storageTestCheck(t, read.Diagnostics)
	storageTestCheck(t, read.State.Get(storageTestContext, &saved))
	if saved.PermissionType.ValueString() != "object_rw" || saved.SecretAccessKey.ValueString() != generatedSecret {
		t.Fatal("drift not detected or read destroyed one-time secret")
	}
	remove := resource.DeleteResponse{State: read.State}
	r.Delete(storageTestContext, resource.DeleteRequest{State: read.State}, &remove)
	storageTestCheck(t, remove.Diagnostics)
	if !revoked || billingCalls != 0 {
		t.Fatal("revocation missing or checked purchase admission")
	}
}

func TestS3CredentialImportCannotRecoverSecret(t *testing.T) {
	r := &s3CredentialResource{client: storageTestClient(t, func(w http.ResponseWriter, req *http.Request) { json.NewEncoder(w).Encode(s3TestMetadata("active")) })}
	initial := storageTestState(t, r, &s3CredentialResourceModel{AllowedBuckets: types.SetNull(types.StringType)})
	response := resource.ImportStateResponse{State: initial}
	r.ImportState(storageTestContext, resource.ImportStateRequest{ID: "AKIAFIXTURE"}, &response)
	storageTestCheck(t, response.Diagnostics)
	read := resource.ReadResponse{State: response.State}
	r.Read(storageTestContext, resource.ReadRequest{State: response.State}, &read)
	storageTestCheck(t, read.Diagnostics)
	var state s3CredentialResourceModel
	storageTestCheck(t, read.State.Get(storageTestContext, &state))
	if state.ID.ValueString() != "AKIAFIXTURE" || !state.SecretAccessKey.IsNull() || state.BucketScope.ValueString() != "specific" || state.AllowedBuckets.IsNull() {
		t.Fatalf("invalid import state: %+v", state)
	}
}

func TestS3CredentialRejectsUnintendedBroadScope(t *testing.T) {
	for _, tc := range []struct {
		permission, scope string
		buckets           []string
		wantError         bool
	}{
		{"object_ro", "specific", []string{"assets"}, false},
		{"object_rw", "all", nil, false},
		{"admin_ro", "all", nil, false},
		{"admin_rw", "specific", []string{"assets"}, true},
		{"admin_ro", "all", []string{"assets"}, true},
		{"object_ro", "specific", nil, true},
		{"object_ro", "", nil, true},
		{"", "all", nil, true},
		{"object_ro", "all", []string{"assets"}, true},
		{"object_ro", "specific", []string{" assets "}, true},
	} {
		if got := validateS3Scope(tc.permission, tc.scope, tc.buckets); (got != nil) != tc.wantError {
			t.Errorf("permission=%s scope=%s buckets=%v error=%v", tc.permission, tc.scope, tc.buckets, got)
		}
	}
}

func TestS3CredentialReadFailuresPreserveIdentityAndSecret(t *testing.T) {
	for _, kind := range []string{"forbidden", "missing-scope", "wrong-key", "wrong-workspace", "wrong-org", "unknown-status"} {
		t.Run(kind, func(t *testing.T) {
			r := &s3CredentialResource{client: storageTestClient(t, func(w http.ResponseWriter, req *http.Request) {
				if kind == "forbidden" {
					w.WriteHeader(403)
					fmt.Fprint(w, `{"detail":"private-generated-value"}`)
					return
				}
				out := s3TestMetadata("active")
				switch kind {
				case "missing-scope":
					delete(out, "allowed_buckets")
				case "wrong-key":
					out["access_key_id"] = "WRONG"
				case "wrong-workspace":
					out["workspace_id"] = "other"
				case "wrong-org":
					out["organization_id"] = "other"
				case "unknown-status":
					out["status"] = "unknown"
				}
				json.NewEncoder(w).Encode(out)
			})}
			r.client.organizationID = "org-test"
			model := s3TestModel()
			model.SecretAccessKey = types.StringValue("private-generated-value")
			state := storageTestState(t, r, model)
			response := resource.ReadResponse{State: state}
			r.Read(storageTestContext, resource.ReadRequest{State: state}, &response)
			if !response.Diagnostics.HasError() || response.State.Raw.IsNull() {
				t.Fatal("malformed or denied read lost identity")
			}
			if strings.Contains(fmt.Sprint(response.Diagnostics), "private-generated-value") {
				t.Fatal("read error leaked secret")
			}
			var after s3CredentialResourceModel
			storageTestCheck(t, response.State.Get(storageTestContext, &after))
			if after.SecretAccessKey.ValueString() != "private-generated-value" {
				t.Fatal("failed read lost stored key")
			}
		})
	}
}

func TestS3CredentialCreateErrorRedactionAndPartialRecovery(t *testing.T) {
	for _, kind := range []string{"error", "missing-secret", "billing-denied"} {
		t.Run(kind, func(t *testing.T) {
			creates := 0
			r := &s3CredentialResource{client: storageTestClient(t, func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path == "/billing/resource-eligibility" {
					storageTestBilling(w, kind != "billing-denied")
					return
				}
				creates++
				if kind == "billing-denied" {
					w.WriteHeader(http.StatusPaymentRequired)
					fmt.Fprint(w, `{"error":"billing_denied","billing_reason":"insufficient_balance"}`)
					return
				}
				if kind == "error" {
					w.WriteHeader(400)
					fmt.Fprint(w, `{"detail":"private-generated-value"}`)
					return
				}
				json.NewEncoder(w).Encode(s3TestMetadata("active"))
			})}
			model := s3TestModel()
			model.ID = types.StringUnknown()
			state := storageTestState(t, r, model)
			response := resource.CreateResponse{State: state}
			r.Create(storageTestContext, resource.CreateRequest{Plan: storageTestPlan(state)}, &response)
			if !response.Diagnostics.HasError() {
				t.Fatal("expected creation failure")
			}
			if strings.Contains(fmt.Sprint(response.Diagnostics), "private-generated-value") {
				t.Fatal("create error leaked secret")
			}
			if kind == "missing-secret" {
				var after s3CredentialResourceModel
				storageTestCheck(t, response.State.Get(storageTestContext, &after))
				if after.ID.ValueString() != "AKIAFIXTURE" {
					t.Fatal("missing one-time secret lost recoverable key ID")
				}
			}
			if kind == "billing-denied" && creates != 1 {
				t.Fatal("denied purchase reached credential API")
			}
		})
	}
}

func TestS3CredentialRevokeConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name, status      string
		postCode, getCode int
		body              string
		wantError         bool
	}{
		{"success", "revoked", 200, 200, `{"success":true}`, false},
		{"gone", "", 404, 404, `{}`, false},
		{"already-revoked", "revoked", 404, 200, `{}`, false},
		{"still-active", "active", 200, 200, `{"success":true}`, true},
		{"unsupported-revoke-route", "active", 404, 200, `{}`, true},
		{"success-missing", "active", 200, 200, `{}`, true},
		{"unsuccessful", "active", 200, 200, `{"success":false}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &s3CredentialResource{client: storageTestClient(t, func(w http.ResponseWriter, req *http.Request) {
				if req.Method == http.MethodPost {
					w.WriteHeader(tc.postCode)
					fmt.Fprint(w, tc.body)
					return
				}
				w.WriteHeader(tc.getCode)
				json.NewEncoder(w).Encode(s3TestMetadata(tc.status))
			})}
			state := storageTestState(t, r, s3TestModel())
			response := resource.DeleteResponse{State: state}
			r.Delete(storageTestContext, resource.DeleteRequest{State: state}, &response)
			if response.Diagnostics.HasError() != tc.wantError {
				t.Fatalf("revocation result: %v", response.Diagnostics)
			}
		})
	}
}

func TestS3CredentialRevokedReadRemovesInactiveKey(t *testing.T) {
	r := &s3CredentialResource{client: storageTestClient(t, func(w http.ResponseWriter, req *http.Request) { json.NewEncoder(w).Encode(s3TestMetadata("revoked")) })}
	state := storageTestState(t, r, s3TestModel())
	response := resource.ReadResponse{State: state}
	r.Read(storageTestContext, resource.ReadRequest{State: state}, &response)
	storageTestCheck(t, response.Diagnostics)
	if !response.State.Raw.IsNull() {
		t.Fatal("revoked key remained managed as active")
	}
}
