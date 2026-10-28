package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
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
	created, revoked, deleted := false, false, false
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
		case req.Method == http.MethodPost && req.URL.Path == "/object-storage/credentials/AKIAFIXTURE/revoke":
			revoked = true
			metadata["status"] = "revoked"
			fmt.Fprint(w, `{"success":true,"message":"revoked"}`)
		case req.Method == http.MethodDelete && req.URL.Path == "/object-storage/credentials/AKIAFIXTURE":
			if !revoked {
				t.Error("the key must be revoked before it is deleted")
			}
			deleted = true
			fmt.Fprint(w, `{"success":true,"message":"deleted"}`)
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
	if !revoked || !deleted || billingCalls != 0 {
		t.Fatal("destroy must revoke and then delete the key")
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

// A key revoked outside Terraform stays in state as revoked; destroy only deletes it.
func TestS3CredentialRevokedKeyIsOnlyDeleted(t *testing.T) {
	var requests []string
	r := &s3CredentialResource{client: storageTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		requests = append(requests, req.Method+" "+req.URL.Path)
		switch req.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode(s3TestMetadata("revoked"))
		case http.MethodDelete:
			fmt.Fprint(w, `{"success":true,"message":"deleted"}`)
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
		}
	})}
	state := storageTestState(t, r, s3TestModel())
	read := resource.ReadResponse{State: state}
	r.Read(storageTestContext, resource.ReadRequest{State: state}, &read)
	storageTestCheck(t, read.Diagnostics)
	var got s3CredentialResourceModel
	storageTestCheck(t, read.State.Get(storageTestContext, &got))
	if got.Status.ValueString() != "revoked" {
		t.Fatalf("a revoked key must stay in state: %+v", got)
	}
	remove := resource.DeleteResponse{State: read.State}
	r.Delete(storageTestContext, resource.DeleteRequest{State: read.State}, &remove)
	storageTestCheck(t, remove.Diagnostics)
	if len(requests) != 2 || requests[1] != "DELETE /object-storage/credentials/AKIAFIXTURE" {
		t.Fatal(requests)
	}
}
