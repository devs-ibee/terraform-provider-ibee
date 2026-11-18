package provider

// Resources added for contract 0.13.0: Secret Manager, the regional load balancer,
// bucket object lock and bucket custom domains.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestSecretManagerEnableWaitsAndDestroyLeavesItEnabled(t *testing.T) {
	var mu sync.Mutex
	polls, enabled := 0, false
	client := computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch req.Method + " " + req.URL.Path {
		case "POST /secret-manager/workspace/enable":
			if req.Header.Get("Idempotency-Key") == "" {
				t.Error("enable without Idempotency-Key")
			}
			if body := decodeBody(t, req); body["namespace"] != "team-a" {
				t.Errorf("enable body %v", body)
			}
			w.WriteHeader(http.StatusAccepted)
			computeJSON(w, map[string]any{"id": "op-1", "organization_id": "o", "workspace_id": "ws-1", "kind": "enable", "status": "queued", "attempts": 0})
		case "GET /secret-manager/operations/op-1":
			polls++
			status := "running"
			if polls > 1 {
				status, enabled = "completed", true
			}
			computeJSON(w, map[string]any{"id": "op-1", "status": status})
		case "GET /secret-manager/workspace":
			if !enabled {
				w.WriteHeader(http.StatusNotFound)
				computeJSON(w, map[string]any{"error": "not_found"})
				return
			}
			computeJSON(w, map[string]any{"organization_id": "o", "workspace_id": "ws-1", "namespace": "team-a", "state": "active", "desired_state": "active", "version": 1, "max_versions": 10, "endpoint": nil})
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
		}
	})
	r := &secretManagerResource{client: client}
	plan := secretManagerModel{ID: types.StringUnknown(), Namespace: types.StringValue("team-a"), MaxVersions: types.Int64Unknown(), State: types.StringUnknown(), Endpoint: types.StringUnknown()}
	resp := resource.CreateResponse{State: computeState(t, r, nil)}
	r.Create(context.Background(), resource.CreateRequest{Plan: computePlanState(t, r, plan)}, &resp)
	computeNoErrors(t, resp.Diagnostics)
	var got secretManagerModel
	computeNoErrors(t, resp.State.Get(context.Background(), &got))
	if got.ID.ValueString() != "ws-1" || got.State.ValueString() != "active" || got.MaxVersions.ValueInt64() != 10 || !got.Endpoint.IsNull() || polls < 2 {
		t.Fatalf("state %+v after %d polls", got, polls)
	}
	del := resource.DeleteResponse{}
	r.Delete(context.Background(), resource.DeleteRequest{State: resp.State}, &del)
	if del.Diagnostics.HasError() || del.Diagnostics.WarningsCount() != 1 {
		t.Fatalf("destroy must only warn: %v", del.Diagnostics)
	}
	enabled = false
	read := resource.ReadResponse{State: resp.State}
	r.Read(context.Background(), resource.ReadRequest{State: resp.State}, &read)
	computeNoErrors(t, read.Diagnostics)
	if !read.State.Raw.IsNull() {
		t.Fatal("a workspace without Secret Manager must leave state")
	}
}

func TestSecretManagerSecretMetadataNeverCarriesAValue(t *testing.T) {
	var puts []map[string]any
	deleted := false
	client := computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.EscapedPath() != "/secret-manager/secret-metadata/apps/billing/db" {
			t.Errorf("unexpected path %s", req.URL.EscapedPath())
		}
		switch req.Method {
		case http.MethodPut:
			body := decodeBody(t, req)
			for key := range body {
				if strings.Contains(key, "value") || key == "data" {
					t.Errorf("metadata write carried %q", key)
				}
			}
			puts = append(puts, body)
			computeJSON(w, map[string]any{"detail": "ok"})
		case http.MethodGet:
			if deleted {
				w.WriteHeader(http.StatusNotFound)
				computeJSON(w, map[string]any{"error": "not_found"})
				return
			}
			last := puts[len(puts)-1]
			computeJSON(w, map[string]any{"custom_metadata": last["custom_metadata"], "max_versions": last["max_versions"], "cas_required": false, "delete_version_after": "0s", "current_version": nil})
		case http.MethodDelete:
			deleted = true
			computeJSON(w, map[string]any{"detail": "destroyed"})
		}
	})
	r := &secretManagerSecretResource{client: client}
	plan := secretManagerSecretModel{ID: types.StringUnknown(), Path: types.StringValue("apps/billing/db"), CustomMetadata: types.MapValueMust(types.StringType, map[string]attr.Value{"owner": types.StringValue("billing")}), MaxVersions: types.Int64Value(5), CasRequired: types.BoolUnknown(), DeleteVersionAfter: types.StringUnknown(), CurrentVersion: types.Int64Unknown()}
	resp := resource.CreateResponse{State: computeState(t, r, nil)}
	r.Create(context.Background(), resource.CreateRequest{Plan: computePlanState(t, r, plan)}, &resp)
	computeNoErrors(t, resp.Diagnostics)
	var got secretManagerSecretModel
	computeNoErrors(t, resp.State.Get(context.Background(), &got))
	if got.ID.ValueString() != "apps/billing/db" || got.MaxVersions.ValueInt64() != 5 || got.CasRequired.ValueBool() || !got.CurrentVersion.IsNull() {
		t.Fatalf("state %+v", got)
	}
	if len(puts) != 1 || puts[0]["custom_metadata"].(map[string]any)["owner"] != "billing" {
		t.Fatalf("put %v", puts)
	}
	plan.ID, plan.CasRequired, plan.DeleteVersionAfter, plan.CurrentVersion = got.ID, types.BoolValue(true), got.DeleteVersionAfter, got.CurrentVersion
	update := resource.UpdateResponse{State: resp.State}
	r.Update(context.Background(), resource.UpdateRequest{Plan: computePlanState(t, r, plan), State: resp.State}, &update)
	computeNoErrors(t, update.Diagnostics)
	if len(puts) != 2 || puts[1]["cas_required"] != true {
		t.Fatalf("update put %v", puts)
	}
	del := resource.DeleteResponse{}
	r.Delete(context.Background(), resource.DeleteRequest{State: update.State}, &del)
	computeNoErrors(t, del.Diagnostics)
	read := resource.ReadResponse{State: update.State}
	r.Read(context.Background(), resource.ReadRequest{State: update.State}, &read)
	computeNoErrors(t, read.Diagnostics)
	if !deleted || !read.State.Raw.IsNull() {
		t.Fatal("a deleted secret must leave state")
	}
}

func TestSecretManagerAppCredentialIssuesSecretIDOnce(t *testing.T) {
	gets, issued, revoked := 0, 0, false
	client := computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		switch req.Method + " " + req.URL.Path {
		case "POST /secret-manager/app-credentials":
			if req.Header.Get("Idempotency-Key") == "" {
				t.Error("create without Idempotency-Key")
			}
			if body := decodeBody(t, req); body["name"] != "ci" || body["access"] != "read" || body["folder"] != "apps" {
				t.Errorf("create body %v", body)
			}
			w.WriteHeader(http.StatusAccepted)
			computeJSON(w, map[string]any{"id": "op-9", "kind": "app_credential_create", "status": "queued", "payload": map[string]any{"credential_id": "cred-1"}})
		case "GET /secret-manager/app-credentials/cred-1":
			gets++
			state := "pending"
			if gets > 1 {
				state = "active"
			}
			if revoked {
				state = "revoked"
			}
			computeJSON(w, map[string]any{"id": "cred-1", "name": "ci", "access": "read", "folder": "apps", "state": state, "has_secret_id": issued > 0})
		case "POST /secret-manager/app-credentials/cred-1/secret-id":
			issued++
			computeJSON(w, map[string]any{"role_id": "role-1", "secret_id": "secret-1", "namespace": "team-a", "mount": "approle", "endpoint": "https://vault.test"})
		case "DELETE /secret-manager/app-credentials/cred-1":
			revoked = true
			w.WriteHeader(http.StatusAccepted)
			computeJSON(w, map[string]any{"id": "op-10", "status": "queued"})
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
		}
	})
	r := &secretManagerAppCredentialResource{client: client}
	unknown := types.StringUnknown()
	plan := secretManagerAppCredentialModel{ID: unknown, Name: types.StringValue("ci"), Access: types.StringValue("read"), Folder: types.StringValue("apps"), State: unknown, RoleID: unknown, SecretID: unknown, Namespace: unknown, Mount: unknown, Endpoint: unknown}
	resp := resource.CreateResponse{State: computeState(t, r, nil)}
	r.Create(context.Background(), resource.CreateRequest{Plan: computePlanState(t, r, plan)}, &resp)
	computeNoErrors(t, resp.Diagnostics)
	var got secretManagerAppCredentialModel
	computeNoErrors(t, resp.State.Get(context.Background(), &got))
	if got.ID.ValueString() != "cred-1" || got.State.ValueString() != "active" || got.RoleID.ValueString() != "role-1" || got.SecretID.ValueString() != "secret-1" || got.Mount.ValueString() != "approle" || issued != 1 {
		t.Fatalf("state %+v issued %d", got, issued)
	}
	read := resource.ReadResponse{State: resp.State}
	r.Read(context.Background(), resource.ReadRequest{State: resp.State}, &read)
	computeNoErrors(t, read.Diagnostics)
	var again secretManagerAppCredentialModel
	computeNoErrors(t, read.State.Get(context.Background(), &again))
	if again.SecretID.ValueString() != "secret-1" || issued != 1 {
		t.Fatal("a read must keep the secret ID and never issue another")
	}
	del := resource.DeleteResponse{}
	r.Delete(context.Background(), resource.DeleteRequest{State: read.State}, &del)
	computeNoErrors(t, del.Diagnostics)
	gone := resource.ReadResponse{State: read.State}
	r.Read(context.Background(), resource.ReadRequest{State: read.State}, &gone)
	computeNoErrors(t, gone.Diagnostics)
	if !revoked || !gone.State.Raw.IsNull() {
		t.Fatal("a revoked credential must leave state")
	}
}

func loadBalancerTestObject(t *testing.T, r *loadBalancerResource, raw map[string]any) types.Object {
	t.Helper()
	value, err := networkValue(types.ObjectType{AttrTypes: r.attributeTypes()}, raw)
	if err != nil {
		t.Fatal(err)
	}
	return value.(types.Object)
}

func TestLoadBalancerSpecificationLifecycle(t *testing.T) {
	spec := map[string]any{
		"name": "web", "region_id": "in-south", "traffic_type": "network",
		"listeners":     []any{map[string]any{"name": "tcp-80", "protocol": "TCP", "port": float64(80), "target_group": "web"}},
		"target_groups": []any{map[string]any{"name": "web", "protocol": "TCP", "targets": []any{map[string]any{"kind": "ip", "address": "10.0.0.10", "port": float64(80)}}}},
	}
	var mu sync.Mutex
	var sent []map[string]any
	version, deleted := float64(1), false
	lb := func() map[string]any {
		out := map[string]any{"id": "lb-1", "name": spec["name"], "region_id": "in-south", "vpc_id": nil, "scope": "regional", "traffic_type": "network", "capacity_mode": "fixed", "visibility": "external", "node_count": float64(1), "managed_by": "customer", "desired_state": "active", "observed_state": "active", "conditions": map[string]any{}, "generation": version, "observed_generation": version, "version": version, "created_at": "now", "updated_at": "now"}
		applied := map[string]any{}
		for k, v := range spec {
			applied[k] = v
		}
		groups := []any{}
		for _, g := range spec["target_groups"].([]any) {
			group := map[string]any{}
			for k, v := range g.(map[string]any) {
				group[k] = v
			}
			group["algorithm"] = "round_robin"
			groups = append(groups, group)
		}
		applied["target_groups"] = groups
		out["spec"] = applied
		return out
	}
	client := computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		key := req.Method + " " + req.URL.Path
		if req.Method != http.MethodGet && req.Header.Get("Idempotency-Key") == "" {
			t.Errorf("%s without Idempotency-Key", key)
		}
		switch key {
		case "POST /networking/load-balancers", "PUT /networking/load-balancers/lb-1", "DELETE /networking/load-balancers/lb-1":
			if req.Method != http.MethodDelete {
				body := decodeBody(t, req)
				sent = append(sent, body)
				if req.Method == http.MethodPut {
					var next map[string]any
					raw, _ := json.Marshal(body["specification"])
					_ = json.Unmarshal(raw, &next)
					spec = next
					version++
				}
			} else {
				deleted = true
			}
			w.WriteHeader(http.StatusAccepted)
			computeJSON(w, map[string]any{"id": "op-" + strings.ToLower(req.Method), "lb_id": "lb-1", "kind": req.Method, "status": "queued"})
		case "GET /networking/load-balancers/operations/op-post", "GET /networking/load-balancers/operations/op-put", "GET /networking/load-balancers/operations/op-delete":
			computeJSON(w, map[string]any{"id": "op", "lb_id": "lb-1", "status": "succeeded"})
		case "GET /networking/load-balancers/lb-1":
			if deleted {
				w.WriteHeader(http.StatusNotFound)
				computeJSON(w, map[string]any{"error": "not_found"})
				return
			}
			computeJSON(w, lb())
		default:
			t.Errorf("unexpected request %s", key)
		}
	})
	r := &loadBalancerResource{client: client}
	plan := loadBalancerTestObject(t, r, spec)
	resp := resource.CreateResponse{State: computeState(t, r, nil)}
	r.Create(context.Background(), resource.CreateRequest{Plan: computePlanState(t, r, plan)}, &resp)
	computeNoErrors(t, resp.Diagnostics)
	if _, ok := sent[0]["vpc_id"]; ok || sent[0]["name"] != "web" {
		t.Fatalf("create sent unset fields or lost the name: %v", sent[0])
	}
	var algorithm types.String
	computeNoErrors(t, resp.State.GetAttribute(context.Background(), path.Root("target_groups").AtListIndex(0).AtName("algorithm"), &algorithm))
	var gotVersion types.Int64
	computeNoErrors(t, resp.State.GetAttribute(context.Background(), path.Root("version"), &gotVersion))
	if algorithm.ValueString() != "round_robin" || gotVersion.ValueInt64() != 1 {
		t.Fatalf("API defaults not read back: %s v%d", algorithm, gotVersion.ValueInt64())
	}

	renamed := map[string]any{}
	for k, v := range spec {
		renamed[k] = v
	}
	renamed["name"] = "web-2"
	update := resource.UpdateResponse{State: resp.State}
	r.Update(context.Background(), resource.UpdateRequest{Plan: computePlanState(t, r, loadBalancerTestObject(t, r, renamed)), State: resp.State}, &update)
	computeNoErrors(t, update.Diagnostics)
	if sent[1]["version"] != json.Number("1") || sent[1]["specification"].(map[string]any)["name"] != "web-2" {
		t.Fatalf("update must send the current version and the whole specification: %v", sent[1])
	}

	del := resource.DeleteResponse{}
	r.Delete(context.Background(), resource.DeleteRequest{State: update.State}, &del)
	computeNoErrors(t, del.Diagnostics)
	read := resource.ReadResponse{State: update.State}
	r.Read(context.Background(), resource.ReadRequest{State: update.State}, &read)
	computeNoErrors(t, read.Diagnostics)
	if !deleted || !read.State.Raw.IsNull() {
		t.Fatal("a deleted load balancer must leave state")
	}
}

func TestLoadBalancerRegionsDataSource(t *testing.T) {
	d := &loadBalancerRegionsDataSource{client: computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Method+" "+req.URL.Path != "GET /networking/load-balancers/regions" {
			t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
		}
		computeJSON(w, []any{map[string]any{"id": "in-south", "name": "Chennai", "nodes": 3}})
	})}
	resp := dataSourceRead(t, d, map[string]attr.Value{})
	computeNoErrors(t, resp.Diagnostics)
	var ids []string
	computeNoErrors(t, resp.State.GetAttribute(context.Background(), path.Root("ids"), &ids))
	var regions []map[string]string
	computeNoErrors(t, resp.State.GetAttribute(context.Background(), path.Root("regions"), &regions))
	if len(ids) != 1 || ids[0] != "in-south" || regions[0]["name"] != "Chennai" || regions[0]["nodes"] != "3" {
		t.Fatalf("ids %v regions %v", ids, regions)
	}
}

func TestBucketObjectLockSetsAndRemovesDefaultRetention(t *testing.T) {
	var rules []any
	client := computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/object-storage/buckets/vault/object-lock" {
			t.Errorf("unexpected path %s", req.URL.Path)
		}
		switch req.Method {
		case http.MethodPut:
			rules = append(rules, decodeBody(t, req)["rule"])
			computeJSON(w, map[string]any{"detail": "ok"})
		case http.MethodGet:
			computeJSON(w, map[string]any{"object_lock_enabled": "Enabled", "rule": rules[len(rules)-1]})
		}
	})
	r := &bucketObjectLockResource{client: client}
	plan := bucketObjectLockModel{ID: types.StringUnknown(), BucketName: types.StringValue("vault"), Mode: types.StringValue("GOVERNANCE"), Days: types.Int64Value(30), Years: types.Int64Null(), ObjectLockEnabled: types.StringUnknown()}
	resp := resource.CreateResponse{State: computeState(t, r, nil)}
	r.Create(context.Background(), resource.CreateRequest{Plan: computePlanState(t, r, plan)}, &resp)
	computeNoErrors(t, resp.Diagnostics)
	var got bucketObjectLockModel
	computeNoErrors(t, resp.State.Get(context.Background(), &got))
	if got.Days.ValueInt64() != 30 || got.ObjectLockEnabled.ValueString() != "Enabled" || !got.Years.IsNull() {
		t.Fatalf("state %+v", got)
	}
	del := resource.DeleteResponse{}
	r.Delete(context.Background(), resource.DeleteRequest{State: resp.State}, &del)
	computeNoErrors(t, del.Diagnostics)
	if len(rules) != 2 || rules[1] != nil {
		t.Fatalf("destroy must send rule null: %v", rules)
	}
}

func TestBucketCustomDomainLifecycle(t *testing.T) {
	added, removed := false, false
	client := computeTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		switch req.Method + " " + req.URL.Path {
		case "POST /object-storage/buckets/assets/custom-domains":
			added = true
			w.WriteHeader(http.StatusCreated)
			computeJSON(w, map[string]any{"domain": "cdn.example.com", "cname_target": "assets.blob.test", "status": "pending_dns"})
		case "GET /object-storage/buckets/assets/custom-domains":
			domains := []any{}
			if added && !removed {
				domains = append(domains, map[string]any{"domain": "cdn.example.com", "status": "active"})
			}
			computeJSON(w, map[string]any{"cname_target": "assets.blob.test", "custom_domains": domains})
		case "DELETE /object-storage/buckets/assets/custom-domains/cdn.example.com":
			removed = true
			computeJSON(w, map[string]any{"domain": "cdn.example.com"})
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
		}
	})
	r := &bucketCustomDomainResource{client: client}
	plan := bucketCustomDomainModel{ID: types.StringUnknown(), BucketName: types.StringValue("assets"), Domain: types.StringValue("cdn.example.com"), CnameTarget: types.StringUnknown(), Status: types.StringUnknown()}
	resp := resource.CreateResponse{State: computeState(t, r, nil)}
	r.Create(context.Background(), resource.CreateRequest{Plan: computePlanState(t, r, plan)}, &resp)
	computeNoErrors(t, resp.Diagnostics)
	var got bucketCustomDomainModel
	computeNoErrors(t, resp.State.Get(context.Background(), &got))
	if got.ID.ValueString() != "assets/cdn.example.com" || got.CnameTarget.ValueString() != "assets.blob.test" || got.Status.ValueString() != "active" {
		t.Fatalf("state %+v", got)
	}
	del := resource.DeleteResponse{}
	r.Delete(context.Background(), resource.DeleteRequest{State: resp.State}, &del)
	computeNoErrors(t, del.Diagnostics)
	read := resource.ReadResponse{State: resp.State}
	r.Read(context.Background(), resource.ReadRequest{State: resp.State}, &read)
	computeNoErrors(t, read.Diagnostics)
	if !removed || !read.State.Raw.IsNull() {
		t.Fatal("a removed domain must leave state")
	}
}
