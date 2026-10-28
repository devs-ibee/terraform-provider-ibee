package provider

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func secretIdentityTestModel() secretIdentityModel {
	return secretIdentityModel{ID: types.StringUnknown(), StoreID: types.StringValue("store-1"), Name: types.StringValue("app"), AuthMethod: types.StringValue("approle"), TokenPolicyMode: types.StringUnknown(), K8sNamespace: types.StringNull(), K8sServiceAccount: types.StringNull(), Enabled: types.BoolValue(false), Status: types.StringUnknown(), OpenbaoRoleName: types.StringUnknown(), RoleID: types.StringUnknown(), SecretID: types.StringUnknown()}
}

func secretIdentityTestResponse(status, mode string) map[string]any {
	return map[string]any{"id": "id-1", "organization_id": "org", "workspace_id": "workspace", "name": "app", "auth_method": "approle", "openbao_role_name": "role-app", "status": status, "token_policy_mode": mode, "created_at": "2026-10-19T00:00:00Z", "updated_at": "2026-10-19T00:00:00Z"}
}

func TestSecretIdentityLifecycle(t *testing.T) {
	ctx := context.Background()
	var calls []string
	status, mode := "active", "read_only"
	r := &secretIdentityResource{}
	r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		switch req.Method + " " + req.URL.Path {
		case "POST /secret-store/stores/store-1/identities":
			body := decodeBody(t, req)
			if !reflect.DeepEqual(body, map[string]any{"name": "app", "auth_method": "approle"}) {
				t.Errorf("create must send only configured fields: %v", body)
			}
			w.WriteHeader(201)
			computeJSON(w, secretIdentityTestResponse(status, mode))
		case "GET /secret-store/identities/id-1/access":
			computeJSON(w, map[string]any{"identity_id": "id-1", "auth_method": "approle", "role_id": "role-123", "secret_id": "fixture-secret-id"})
		case "POST /secret-store/identities/id-1/disable":
			status = "disabled"
			computeJSON(w, secretIdentityTestResponse(status, mode))
		case "POST /secret-store/identities/id-1/enable":
			status = "active"
			computeJSON(w, secretIdentityTestResponse(status, mode))
		case "GET /secret-store/identities/id-1":
			computeJSON(w, secretIdentityTestResponse(status, mode))
		case "DELETE /secret-store/identities/id-1":
			computeJSON(w, map[string]any{"status": "deleted"})
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
			w.WriteHeader(500)
		}
	})
	create := resource.CreateResponse{State: computeState(t, r, nil)}
	r.Create(ctx, resource.CreateRequest{Plan: computePlanState(t, r, secretIdentityTestModel())}, &create)
	computeNoErrors(t, create.Diagnostics)
	var created secretIdentityModel
	computeNoErrors(t, create.State.Get(ctx, &created))
	if created.SecretID.ValueString() != "fixture-secret-id" || created.RoleID.ValueString() != "role-123" || created.Enabled.ValueBool() || created.Status.ValueString() != "disabled" || created.TokenPolicyMode.ValueString() != "read_only" {
		t.Fatalf("unexpected created state: %+v", created)
	}

	read := resource.ReadResponse{State: create.State}
	r.Read(ctx, resource.ReadRequest{State: create.State}, &read)
	computeNoErrors(t, read.Diagnostics)
	if !read.State.Raw.Equal(create.State.Raw) {
		t.Fatal("refresh changed stable state")
	}

	plan := created
	plan.Enabled = types.BoolValue(true)
	update := resource.UpdateResponse{State: create.State}
	r.Update(ctx, resource.UpdateRequest{Plan: computePlanState(t, r, plan), State: create.State}, &update)
	computeNoErrors(t, update.Diagnostics)
	var updated secretIdentityModel
	computeNoErrors(t, update.State.Get(ctx, &updated))
	if updated.TokenPolicyMode.ValueString() != "read_only" || !updated.Enabled.ValueBool() || updated.SecretID.ValueString() != "fixture-secret-id" {
		t.Fatalf("unexpected updated state: %+v", updated)
	}

	del := resource.DeleteResponse{}
	r.Delete(ctx, resource.DeleteRequest{State: update.State}, &del)
	computeNoErrors(t, del.Diagnostics)

	want := []string{"POST /secret-store/stores/store-1/identities", "GET /secret-store/identities/id-1/access", "POST /secret-store/identities/id-1/disable", "GET /secret-store/identities/id-1", "POST /secret-store/identities/id-1/enable", "DELETE /secret-store/identities/id-1"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("login details must be read once, at creation:\n got %v\nwant %v", calls, want)
	}
}

func TestSecretIdentityImportAndGone(t *testing.T) {
	ctx := context.Background()
	gone := false
	r := &secretIdentityResource{}
	r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet || req.URL.Path != "/secret-store/identities/id-1" {
			t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
		}
		if gone {
			w.WriteHeader(404)
			computeJSON(w, map[string]any{"error": "not_found"})
			return
		}
		computeJSON(w, secretIdentityTestResponse("active", "read_write"))
	})
	imp := resource.ImportStateResponse{State: computeState(t, r, nil)}
	r.ImportState(ctx, resource.ImportStateRequest{ID: "store-1/id-1"}, &imp)
	computeNoErrors(t, imp.Diagnostics)
	read := resource.ReadResponse{State: imp.State}
	r.Read(ctx, resource.ReadRequest{State: imp.State}, &read)
	computeNoErrors(t, read.Diagnostics)
	var got secretIdentityModel
	computeNoErrors(t, read.State.Get(ctx, &got))
	if got.StoreID.ValueString() != "store-1" || got.Name.ValueString() != "app" || !got.SecretID.IsNull() || !got.Enabled.ValueBool() {
		t.Fatalf("unexpected imported state: %+v", got)
	}
	gone = true
	read = resource.ReadResponse{State: imp.State}
	r.Read(ctx, resource.ReadRequest{State: read.State}, &read)
	computeNoErrors(t, read.Diagnostics)
	if !read.State.Raw.IsNull() {
		t.Fatal("a deleted identity must leave state")
	}
	for _, id := range []string{"id-1", "store-1/", "/id-1", "a/b/c"} {
		bad := resource.ImportStateResponse{State: computeState(t, r, nil)}
		r.ImportState(ctx, resource.ImportStateRequest{ID: id}, &bad)
		if !bad.Diagnostics.HasError() {
			t.Fatalf("import ID %q must be refused", id)
		}
	}
}

// GET .../access mints a new AppRole secret ID on every call, so a failed read
// is not retried: the identity is kept in state and the error is relayed.
func TestSecretIdentityAccessReadIsNeverRetried(t *testing.T) {
	ctx := context.Background()
	accessReads := 0
	r := &secretIdentityResource{}
	r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		switch req.Method + " " + req.URL.Path {
		case "POST /secret-store/stores/store-1/identities":
			w.WriteHeader(201)
			computeJSON(w, secretIdentityTestResponse("active", "read_only"))
		case "GET /secret-store/identities/id-1/access":
			accessReads++
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(503)
			computeJSON(w, map[string]any{"detail": "unavailable"})
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
			w.WriteHeader(500)
		}
	})
	create := resource.CreateResponse{State: computeState(t, r, nil)}
	r.Create(ctx, resource.CreateRequest{Plan: computePlanState(t, r, secretIdentityTestModel())}, &create)
	if accessReads != 1 {
		t.Fatalf("access read must not be retried, got %d reads", accessReads)
	}
	if create.Diagnostics.ErrorsCount() != 1 || !strings.Contains(create.Diagnostics[0].Summary(), "login details could not be read") || !strings.Contains(create.Diagnostics[0].Detail(), "HTTP 503") {
		t.Fatalf("unexpected diagnostics: %v", create.Diagnostics)
	}
	var created secretIdentityModel
	computeNoErrors(t, create.State.Get(ctx, &created))
	if created.ID.ValueString() != "id-1" || !created.RoleID.IsNull() || !created.SecretID.IsNull() {
		t.Fatalf("the identity must stay tracked without login details: %+v", created)
	}
}

func TestSecretIdentityCreateRelaysRefusal(t *testing.T) {
	r := &secretIdentityResource{}
	r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(409)
		computeJSON(w, map[string]any{"error": "conflict", "message": "identity name already exists"})
	})
	create := resource.CreateResponse{State: computeState(t, r, nil)}
	r.Create(context.Background(), resource.CreateRequest{Plan: computePlanState(t, r, secretIdentityTestModel())}, &create)
	if !create.Diagnostics.HasError() || !strings.Contains(create.Diagnostics[0].Detail(), "identity name already exists") || !create.State.Raw.IsNull() {
		t.Fatalf("refusal must be relayed without state: %v", create.Diagnostics)
	}
}

func secretScopeTestResponse(id, mode string, rollback bool) map[string]any {
	return map[string]any{"id": id, "identity_id": "id-1", "scope_type": "store", "store_id": "store-2", "organization_id": "org", "workspace_id": "workspace", "access_mode": mode, "allow_version_read": true, "allow_rollback": rollback, "allow_destroy": false, "created_at": "2026-10-19T00:00:00Z"}
}

func TestSecretIdentityScopeLifecycle(t *testing.T) {
	ctx := context.Background()
	scope := secretScopeTestResponse("scope-1", "read_only", false)
	exists := true
	r := &secretIdentityScopeResource{}
	r.client = computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		switch req.Method + " " + req.URL.Path {
		case "POST /secret-store/identities/id-1/scopes":
			if body := decodeBody(t, req); !reflect.DeepEqual(body, map[string]any{"store_id": "store-2", "access_mode": "read_only"}) {
				t.Errorf("create must send only configured fields: %v", body)
			}
			w.WriteHeader(201)
			computeJSON(w, scope)
		case "GET /secret-store/identities/id-1/scopes":
			scopes := []any{secretScopeTestResponse("scope-0", "read_only", false)}
			if exists {
				scopes = append(scopes, scope)
			}
			computeJSON(w, map[string]any{"scopes": scopes, "total": len(scopes)})
		case "PATCH /secret-store/scopes/scope-1":
			body := decodeBody(t, req)
			if body["access_mode"] != "read_write" || body["allow_rollback"] != true {
				t.Errorf("unexpected update body %v", body)
			}
			scope = secretScopeTestResponse("scope-1", "read_write", true)
			computeJSON(w, scope)
		case "DELETE /secret-store/scopes/scope-1":
			exists = false
			computeJSON(w, map[string]any{"status": "deleted"})
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
			w.WriteHeader(500)
		}
	})
	plan := secretIdentityScopeModel{ID: types.StringUnknown(), IdentityID: types.StringValue("id-1"), StoreID: types.StringValue("store-2"), AccessMode: types.StringValue("read_only"), AllowVersionRead: types.BoolUnknown(), AllowRollback: types.BoolUnknown(), AllowDestroy: types.BoolUnknown()}
	create := resource.CreateResponse{State: computeState(t, r, nil)}
	r.Create(ctx, resource.CreateRequest{Plan: computePlanState(t, r, plan)}, &create)
	computeNoErrors(t, create.Diagnostics)

	read := resource.ReadResponse{State: create.State}
	r.Read(ctx, resource.ReadRequest{State: create.State}, &read)
	computeNoErrors(t, read.Diagnostics)
	if !read.State.Raw.Equal(create.State.Raw) {
		t.Fatal("refresh changed stable state")
	}

	var state secretIdentityScopeModel
	computeNoErrors(t, create.State.Get(ctx, &state))
	next := state
	next.AccessMode, next.AllowRollback = types.StringValue("read_write"), types.BoolValue(true)
	update := resource.UpdateResponse{State: create.State}
	r.Update(ctx, resource.UpdateRequest{Plan: computePlanState(t, r, next), State: create.State}, &update)
	computeNoErrors(t, update.Diagnostics)
	var updated secretIdentityScopeModel
	computeNoErrors(t, update.State.Get(ctx, &updated))
	if updated.AccessMode.ValueString() != "read_write" || !updated.AllowRollback.ValueBool() {
		t.Fatalf("unexpected updated scope: %+v", updated)
	}

	imp := resource.ImportStateResponse{State: computeState(t, r, nil)}
	r.ImportState(ctx, resource.ImportStateRequest{ID: "id-1/scope-1"}, &imp)
	computeNoErrors(t, imp.Diagnostics)
	read = resource.ReadResponse{State: imp.State}
	r.Read(ctx, resource.ReadRequest{State: imp.State}, &read)
	computeNoErrors(t, read.Diagnostics)
	var store types.String
	computeNoErrors(t, read.State.GetAttribute(ctx, path.Root("store_id"), &store))
	if store.ValueString() != "store-2" {
		t.Fatal("import did not read the scope")
	}

	del := resource.DeleteResponse{}
	r.Delete(ctx, resource.DeleteRequest{State: update.State}, &del)
	computeNoErrors(t, del.Diagnostics)
	read = resource.ReadResponse{State: update.State}
	r.Read(ctx, resource.ReadRequest{State: update.State}, &read)
	computeNoErrors(t, read.Diagnostics)
	if !read.State.Raw.IsNull() {
		t.Fatal("a removed scope must leave state")
	}
}
