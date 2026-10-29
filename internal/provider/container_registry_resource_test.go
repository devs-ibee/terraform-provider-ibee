package provider

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const testRegistryID = "6f1c2c1e-8d3b-4c55-9a43-0d6c1f0b9e11"

// registryFixture is a minimal registry service: changes are accepted with 202
// and settle (or finish deleting) on the next read.
type registryFixture struct {
	t          *testing.T
	mu         sync.Mutex
	calls      []string
	bodies     map[string]map[string]any
	headers    map[string]http.Header
	visibility string
	policy     string
	sku        string
	state      string
	generation int
	observed   int
	deletedAt  any
	exists     bool
	tokens     []map[string]any
}

func newRegistryFixture(t *testing.T) *registryFixture {
	return &registryFixture{t: t, bodies: map[string]map[string]any{}, headers: map[string]http.Header{}}
}

func (f *registryFixture) registry() map[string]any {
	return map[string]any{
		"registry_id": testRegistryID, "registry_name": "acme-apps", "registry_kind": "CUSTOMER", "organization_id": "1",
		"workspace_id": "workspace", "visibility": f.visibility, "security": map[string]any{"vulnerability_pull_policy": f.policy},
		"desired_state": "ACTIVE", "observed_state": f.state, "generation": f.generation, "observed_generation": f.observed,
		"created_at": "2026-10-20T00:00:00Z", "updated_at": "2026-10-20T00:00:00Z", "deleted_at": f.deletedAt,
	}
}

func (f *registryFixture) count(call string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == call {
			n++
		}
	}
	return n
}

func (f *registryFixture) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := req.Method + " " + req.URL.Path
	f.calls = append(f.calls, key)
	f.headers[key] = req.Header.Clone()
	if req.ContentLength > 0 {
		f.bodies[key] = decodeBody(f.t, req)
	}
	if req.URL.Query().Get("workspace_id") != "workspace" {
		f.t.Errorf("%s sent no workspace_id", key)
	}
	base := "/container-registry/registries/" + testRegistryID
	change := func() { f.generation++; f.state = "UPDATING" }
	switch key {
	case "POST /container-registry/registries":
		f.exists, f.state, f.generation, f.observed = true, "PROVISIONING", 1, 0
		f.visibility, f.policy = "PRIVATE", "ALLOW"
		if body := f.bodies[key]; body["visibility"] != nil {
			f.visibility = body["visibility"].(string)
		}
		w.WriteHeader(202)
		computeJSON(w, f.registry())
	case "GET " + base:
		if !f.exists {
			w.WriteHeader(404)
			computeJSON(w, map[string]any{"detail": "Registry not found"})
			return
		}
		out := f.registry()
		// The next read sees the change applied.
		switch f.state {
		case "PROVISIONING", "UPDATING":
			f.state, f.observed = "ACTIVE", f.generation
		case "DELETING":
			f.state, f.deletedAt = "DELETED", "2026-10-20T01:00:00Z"
		}
		computeJSON(w, out)
	case "GET " + base + "/package":
		computeJSON(w, map[string]any{"registry_id": testRegistryID, "current": map[string]any{"sku_code": "REGISTRY-STORAGE-250GB"}, "pending": nil, "reconciliation_status": nil})
	case "PATCH " + base + "/visibility":
		f.visibility = f.bodies[key]["visibility"].(string)
		change()
		w.WriteHeader(202)
		computeJSON(w, f.registry())
	case "PATCH " + base + "/security/settings":
		f.policy = f.bodies[key]["vulnerability_pull_policy"].(string)
		change()
		w.WriteHeader(202)
		computeJSON(w, map[string]any{"vulnerability_pull_policy": f.policy, "scan_on_push": true, "sbom_generation_on_push": true, "observed_state": "UPDATING", "generation": f.generation, "observed_generation": f.observed})
	case "POST " + base + "/upgrade":
		f.sku = f.bodies[key]["billing_catalog"].(map[string]any)["sku_code"].(string)
		w.WriteHeader(202)
		computeJSON(w, map[string]any{"operation_id": "op-1", "registry_id": testRegistryID, "kind": "UPGRADE", "status": "PENDING", "sku_code": f.sku})
	case "DELETE " + base:
		if req.URL.Query().Get("confirm_registry_name") != "acme-apps" {
			f.t.Errorf("delete must confirm the registry name: %s", req.URL.RawQuery)
		}
		f.state, f.generation = "DELETING", f.generation+1
		w.WriteHeader(202)
		computeJSON(w, f.registry())
	case "POST " + base + "/access-tokens":
		token := map[string]any{"token_id": "tok-1", "token_name": f.bodies[key]["token_name"], "harbor_username": "robot$acme+ci", "permission": "PULL_PUSH", "registry_ids": []string{testRegistryID}, "disabled": false, "expires_at": nil, "created_by_user_id": nil, "created_at": "2026-10-20T00:00:00Z", "revoked_at": nil}
		f.tokens = append(f.tokens, token)
		w.WriteHeader(201)
		computeJSON(w, map[string]any{"token_id": "tok-1", "token_name": token["token_name"], "username": "robot$acme+ci", "secret": "fixture-registry-secret-never-shown", "permission": "PULL_PUSH", "registry_ids": []string{testRegistryID}, "expires_at": nil, "created_at": "2026-10-20T00:00:00Z"})
	case "GET " + base + "/access-tokens":
		if !f.exists {
			w.WriteHeader(404)
			computeJSON(w, map[string]any{"detail": "Registry not found"})
			return
		}
		computeJSON(w, f.tokens)
	case "DELETE " + base + "/access-tokens/tok-1":
		f.tokens = []map[string]any{}
		computeJSON(w, map[string]any{"detail": "Token revoked"})
	case "GET /container-registry/registries":
		page := req.URL.Query().Get("page")
		if req.URL.Query().Get("page_size") != "100" {
			f.t.Errorf("list must ask for pages of 100: %s", req.URL.RawQuery)
		}
		items := []any{}
		if page == "1" || page == "2" {
			items = append(items, f.registry())
		}
		computeJSON(w, map[string]any{"items": items, "total": 2, "page": page, "page_size": 100})
	default:
		f.t.Errorf("unexpected request %s", key)
		w.WriteHeader(500)
	}
}

func registryTestModel() containerRegistryModel {
	return containerRegistryModel{ID: types.StringUnknown(), Name: types.StringValue("acme-apps"), SKUCode: types.StringValue("REGISTRY-STORAGE-250GB"), Visibility: types.StringUnknown(), AcknowledgePublicEgress: types.BoolNull(), VulnerabilityPullPolicy: types.StringUnknown(), ObservedState: types.StringUnknown()}
}

func TestContainerRegistryLifecycle(t *testing.T) {
	ctx := context.Background()
	fixture := newRegistryFixture(t)
	r := &containerRegistryResource{client: computeTestClient(t, fixture.ServeHTTP)}
	r.client.operationTimeout = 0 // the default: the fixture settles on the next read

	create := resource.CreateResponse{State: computeState(t, r, nil)}
	r.Create(ctx, resource.CreateRequest{Plan: computePlanState(t, r, registryTestModel())}, &create)
	computeNoErrors(t, create.Diagnostics)
	var created containerRegistryModel
	computeNoErrors(t, create.State.Get(ctx, &created))
	if created.ID.ValueString() != testRegistryID || created.ObservedState.ValueString() != "ACTIVE" || created.Visibility.ValueString() != "PRIVATE" || created.VulnerabilityPullPolicy.ValueString() != "ALLOW" {
		t.Fatalf("create must wait until ACTIVE: %+v", created)
	}
	want := map[string]any{"registry_name": "acme-apps", "confirm_registry_name": "acme-apps", "billing_catalog": map[string]any{"sku_code": "REGISTRY-STORAGE-250GB"}}
	if body := fixture.bodies["POST /container-registry/registries"]; !reflect.DeepEqual(body, want) {
		t.Fatalf("create must send only configured fields: %v", body)
	}

	read := resource.ReadResponse{State: create.State}
	r.Read(ctx, resource.ReadRequest{State: create.State}, &read)
	computeNoErrors(t, read.Diagnostics)
	if !read.State.Raw.Equal(create.State.Raw) {
		t.Fatal("refresh changed stable state")
	}

	plan := created
	plan.Visibility, plan.AcknowledgePublicEgress = types.StringValue("PUBLIC"), types.BoolValue(true)
	plan.VulnerabilityPullPolicy, plan.SKUCode = types.StringValue("BLOCK_CRITICAL"), types.StringValue("REGISTRY-STORAGE-500GB")
	update := resource.UpdateResponse{State: create.State}
	r.Update(ctx, resource.UpdateRequest{Plan: computePlanState(t, r, plan), State: create.State}, &update)
	computeNoErrors(t, update.Diagnostics)
	var updated containerRegistryModel
	computeNoErrors(t, update.State.Get(ctx, &updated))
	if updated.Visibility.ValueString() != "PUBLIC" || updated.VulnerabilityPullPolicy.ValueString() != "BLOCK_CRITICAL" || updated.SKUCode.ValueString() != "REGISTRY-STORAGE-500GB" || updated.ObservedState.ValueString() != "ACTIVE" {
		t.Fatalf("unexpected updated state: %+v", updated)
	}
	base := "/container-registry/registries/" + testRegistryID
	if body := fixture.bodies["PATCH "+base+"/visibility"]; !reflect.DeepEqual(body, map[string]any{"visibility": "PUBLIC", "acknowledge_public_egress_charges": true}) {
		t.Fatalf("visibility body: %v", body)
	}
	upgrade := fixture.headers["POST "+base+"/upgrade"]
	if !strings.HasPrefix(upgrade.Get("Idempotency-Key"), "tf-") || upgrade.Get("X-Idempotency-Key") != "" {
		t.Fatalf("upgrade must send Idempotency-Key (not X-Idempotency-Key): %v", upgrade)
	}

	del := resource.DeleteResponse{}
	r.Delete(ctx, resource.DeleteRequest{State: update.State}, &del)
	computeNoErrors(t, del.Diagnostics)
	if fixture.count("DELETE "+base) != 1 || fixture.deletedAt == nil {
		t.Fatal("destroy must delete and wait until the API reports the registry deleted")
	}

	gone := resource.ReadResponse{State: update.State}
	r.Read(ctx, resource.ReadRequest{State: update.State}, &gone)
	computeNoErrors(t, gone.Diagnostics)
	if !gone.State.Raw.IsNull() {
		t.Fatal("a deleted registry must leave state")
	}
}

func TestContainerRegistryImportReadsThePlanOnce(t *testing.T) {
	ctx := context.Background()
	fixture := newRegistryFixture(t)
	fixture.exists, fixture.state, fixture.generation, fixture.observed = true, "ACTIVE", 1, 1
	fixture.visibility, fixture.policy = "PRIVATE", "ALLOW"
	r := &containerRegistryResource{client: computeTestClient(t, fixture.ServeHTTP)}
	imp := resource.ImportStateResponse{State: computeState(t, r, nil)}
	r.ImportState(ctx, resource.ImportStateRequest{ID: testRegistryID}, &imp)
	computeNoErrors(t, imp.Diagnostics)
	read := resource.ReadResponse{State: imp.State}
	r.Read(ctx, resource.ReadRequest{State: imp.State}, &read)
	computeNoErrors(t, read.Diagnostics)
	var got containerRegistryModel
	computeNoErrors(t, read.State.Get(ctx, &got))
	if got.Name.ValueString() != "acme-apps" || got.SKUCode.ValueString() != "REGISTRY-STORAGE-250GB" {
		t.Fatalf("unexpected imported state: %+v", got)
	}
	again := resource.ReadResponse{State: read.State}
	r.Read(ctx, resource.ReadRequest{State: read.State}, &again)
	computeNoErrors(t, again.Diagnostics)
	if fixture.count("GET /container-registry/registries/"+testRegistryID+"/package") != 1 {
		t.Fatal("the plan is read only while sku_code is unknown")
	}
}

func TestContainerRegistryCreateRelaysRefusal(t *testing.T) {
	r := &containerRegistryResource{client: computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(403)
		computeJSON(w, map[string]any{"detail": map[string]any{"code": "billing_resource_not_eligible", "reason": "kyc_required"}})
	})}
	create := resource.CreateResponse{State: computeState(t, r, nil)}
	r.Create(context.Background(), resource.CreateRequest{Plan: computePlanState(t, r, registryTestModel())}, &create)
	if !create.Diagnostics.HasError() || !strings.Contains(fmt.Sprint(create.Diagnostics), "billing_resource_not_eligible") {
		t.Fatalf("the API's refusal must be relayed: %v", create.Diagnostics)
	}
	if !create.State.Raw.IsNull() {
		t.Fatal("a refused create must not write state")
	}
}

func TestContainerRegistryWaitStopsOnError(t *testing.T) {
	r := &containerRegistryResource{client: computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost {
			w.WriteHeader(202)
		}
		computeJSON(w, map[string]any{"registry_id": testRegistryID, "registry_name": "acme-apps", "visibility": "PRIVATE", "security": map[string]any{"vulnerability_pull_policy": "ALLOW"}, "observed_state": "ERROR", "generation": 1, "observed_generation": 0, "deleted_at": nil})
	})}
	create := resource.CreateResponse{State: computeState(t, r, nil)}
	r.Create(context.Background(), resource.CreateRequest{Plan: computePlanState(t, r, registryTestModel())}, &create)
	if !create.Diagnostics.HasError() || !strings.Contains(fmt.Sprint(create.Diagnostics), "ERROR") {
		t.Fatalf("observed_state ERROR must stop the wait: %v", create.Diagnostics)
	}
	var kept containerRegistryModel
	computeNoErrors(t, create.State.Get(context.Background(), &kept))
	if kept.ID.ValueString() != testRegistryID {
		t.Fatal("the created registry's ID must stay in state")
	}
}

func TestContainerRegistryAccessTokenLifecycle(t *testing.T) {
	ctx := context.Background()
	fixture := newRegistryFixture(t)
	fixture.exists, fixture.state, fixture.generation, fixture.observed = true, "ACTIVE", 1, 1
	r := &containerRegistryAccessTokenResource{client: computeTestClient(t, fixture.ServeHTTP)}
	plan := containerRegistryAccessTokenModel{ID: types.StringUnknown(), RegistryID: types.StringValue(testRegistryID), Name: types.StringValue("ci"), Permission: types.StringValue("PULL_PUSH"), ExpiresInDays: types.Int64Value(30), Description: types.StringNull(), RegistryIDs: types.SetNull(types.StringType), Username: types.StringUnknown(), Secret: types.StringUnknown(), ExpiresAt: types.StringUnknown()}
	create := resource.CreateResponse{State: computeState(t, r, nil)}
	r.Create(ctx, resource.CreateRequest{Plan: computePlanState(t, r, plan)}, &create)
	computeNoErrors(t, create.Diagnostics)
	var created containerRegistryAccessTokenModel
	computeNoErrors(t, create.State.Get(ctx, &created))
	if created.ID.ValueString() != "tok-1" || created.Secret.ValueString() != "fixture-registry-secret-never-shown" || created.Username.ValueString() != "robot$acme+ci" || !created.ExpiresAt.IsNull() {
		t.Fatalf("unexpected created token state: %+v", created)
	}
	base := "/container-registry/registries/" + testRegistryID + "/access-tokens"
	if body := fixture.bodies["POST "+base]; fmt.Sprint(body) != fmt.Sprint(map[string]any{"token_name": "ci", "permission": "PULL_PUSH", "expires_in_days": 30}) {
		t.Fatalf("create must send only configured fields: %v", body)
	}

	read := resource.ReadResponse{State: create.State}
	r.Read(ctx, resource.ReadRequest{State: create.State}, &read)
	computeNoErrors(t, read.Diagnostics)
	if !read.State.Raw.Equal(create.State.Raw) {
		t.Fatal("refresh must keep the secret and change nothing")
	}

	del := resource.DeleteResponse{}
	r.Delete(ctx, resource.DeleteRequest{State: read.State}, &del)
	computeNoErrors(t, del.Diagnostics)
	gone := resource.ReadResponse{State: read.State}
	r.Read(ctx, resource.ReadRequest{State: read.State}, &gone)
	computeNoErrors(t, gone.Diagnostics)
	if !gone.State.Raw.IsNull() || fixture.count("DELETE "+base+"/tok-1") != 1 {
		t.Fatal("destroy must revoke the token, and a token missing from the list leaves state")
	}

	imp := resource.ImportStateResponse{State: computeState(t, r, nil)}
	r.ImportState(ctx, resource.ImportStateRequest{ID: testRegistryID + "/tok-1"}, &imp)
	computeNoErrors(t, imp.Diagnostics)
	for _, id := range []string{"tok-1", testRegistryID + "/", "a/b/c"} {
		bad := resource.ImportStateResponse{State: computeState(t, r, nil)}
		r.ImportState(ctx, resource.ImportStateRequest{ID: id}, &bad)
		if !bad.Diagnostics.HasError() {
			t.Fatalf("import ID %q must be refused", id)
		}
	}
}

func TestContainerRegistriesDataSourceReadsEveryPage(t *testing.T) {
	fixture := newRegistryFixture(t)
	fixture.state, fixture.visibility, fixture.policy = "ACTIVE", "PRIVATE", "ALLOW"
	d := NewContainerRegistriesDataSource().(*containerRegistriesDataSource)
	d.client = computeTestClient(t, fixture.ServeHTTP)
	var sch datasource.SchemaResponse
	d.Schema(context.Background(), datasource.SchemaRequest{}, &sch)
	resp := datasource.ReadResponse{State: tfsdk.State{Schema: sch.Schema}}
	d.Read(context.Background(), datasource.ReadRequest{}, &resp)
	computeNoErrors(t, resp.Diagnostics)
	var got containerRegistriesModel
	computeNoErrors(t, resp.State.Get(context.Background(), &got))
	if len(got.Registries) != 2 || got.Registries[0].Name.ValueString() != "acme-apps" || got.Registries[0].CreatedAt.ValueString() == "" {
		t.Fatalf("unexpected registries: %+v", got)
	}
	if fixture.count("GET /container-registry/registries") != 2 {
		t.Fatal("the list must stop once total is reached")
	}
}
