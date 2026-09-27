package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var storageTestContext = context.Background()

func storageTestSchema(r resource.Resource) schema.Schema {
	var response resource.SchemaResponse
	r.Schema(storageTestContext, resource.SchemaRequest{}, &response)
	return response.Schema
}

func storageTestState(t *testing.T, r resource.Resource, model any) tfsdk.State {
	t.Helper()
	state := tfsdk.State{Schema: storageTestSchema(r)}
	if diagnostics := state.Set(storageTestContext, model); diagnostics.HasError() {
		t.Fatalf("construct state: %v", diagnostics)
	}
	return state
}

func storageTestPlan(state tfsdk.State) tfsdk.Plan {
	return tfsdk.Plan{Raw: state.Raw, Schema: state.Schema}
}
func storageTestConfig(state tfsdk.State) tfsdk.Config {
	return tfsdk.Config{Raw: state.Raw, Schema: state.Schema}
}

func storageTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Query().Get("workspace_id") != "workspace-test" {
			t.Error("missing workspace scope")
		}
		if req.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing token")
		}
		w.Header().Set("Content-Type", "application/json")
		handler(w, req)
	}))
	t.Cleanup(server.Close)
	return NewClient(server.URL, "test-token", "workspace-test")
}

func storageTestBilling(w http.ResponseWriter, allowed bool) {
	fmt.Fprintf(w, `{"organization_id":"org-test","allowed":%t,"reason":"%s","billing_mode":"PREPAID","billing_state":"CURRENT","currency":"INR","evaluated_at":"2026-09-27T00:00:00Z"}`, allowed, map[bool]string{true: "eligible", false: "insufficient_balance"}[allowed])
}

func storageTestCheck(t *testing.T, diagnostics diag.Diagnostics) {
	t.Helper()
	if diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diagnostics)
	}
}

func storageTestBucketModel() bucketResourceModel {
	return bucketResourceModel{ID: types.StringValue("assets"), Name: types.StringValue("assets"), Region: types.StringValue("in-south-2"), IsPublic: types.BoolValue(false), ObjectLockEnabled: types.BoolValue(false), ForceDestroy: types.BoolValue(false), Status: types.StringValue("active"), ObjectCount: types.Int64Value(0), TotalSize: types.Int64Value(0)}
}

func storageTestBucketJSON(public bool, counters string) string {
	return fmt.Sprintf(`{"name":"assets","region":"in-south-2","is_public":%t,"bucket_lock_enabled":false,"status":"active","plan":"standard","site_id":"3"%s}`, public, counters)
}

func TestBucketLifecycle(t *testing.T) {
	public := false
	created, deleted, billingCalls := false, false, 0
	r := &bucketResource{client: storageTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.URL.Path == "/billing/resource-eligibility":
			billingCalls++
			storageTestBilling(w, true)
		case req.Method == http.MethodPost && req.URL.Path == "/object-storage/buckets":
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["name"] != "assets" || body["region"] != "in-south-2" || body["is_public"] != false {
				t.Errorf("unexpected create: %v", body)
			}
			if billingCalls != 1 {
				t.Error("create did not follow billing admission")
			}
			created = true
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, storageTestBucketJSON(false, ""))
		case req.Method == http.MethodGet && req.URL.Path == "/object-storage/buckets/assets":
			fmt.Fprint(w, storageTestBucketJSON(public, `,"object_count":0,"total_size":0`))
		case req.Method == http.MethodPatch && req.URL.Path == "/object-storage/buckets/assets":
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if len(body) != 1 || body["is_public"] != true {
				t.Errorf("unexpected patch: %v", body)
			}
			public = true
			fmt.Fprint(w, storageTestBucketJSON(true, `,"object_count":0,"total_size":0`))
		case req.Method == http.MethodDelete && req.URL.Path == "/object-storage/buckets/assets":
			deleted = true
			fmt.Fprint(w, `{"detail":"Bucket deleted"}`)
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL)
			http.Error(w, "unexpected", 500)
		}
	})}
	model := storageTestBucketModel()
	model.ID = types.StringUnknown()
	plan := storageTestState(t, r, model)
	create := resource.CreateResponse{State: plan}
	r.Create(storageTestContext, resource.CreateRequest{Plan: storageTestPlan(plan)}, &create)
	storageTestCheck(t, create.Diagnostics)
	if !created {
		t.Fatal("bucket not created")
	}
	var state bucketResourceModel
	storageTestCheck(t, create.State.Get(storageTestContext, &state))
	if state.ID.ValueString() != "assets" || state.ObjectCount.ValueInt64() != 0 || state.ObjectCount.IsNull() {
		t.Fatalf("invalid created state: %+v", state)
	}
	updated := state
	updated.IsPublic = types.BoolValue(true)
	update := resource.UpdateResponse{State: create.State}
	r.Update(storageTestContext, resource.UpdateRequest{Plan: storageTestPlan(storageTestState(t, r, updated)), State: create.State}, &update)
	storageTestCheck(t, update.Diagnostics)
	read := resource.ReadResponse{State: update.State}
	r.Read(storageTestContext, resource.ReadRequest{State: update.State}, &read)
	storageTestCheck(t, read.Diagnostics)
	storageTestCheck(t, read.State.Get(storageTestContext, &state))
	if !state.IsPublic.ValueBool() {
		t.Fatal("canonical public setting was not refreshed")
	}
	remove := resource.DeleteResponse{State: read.State}
	r.Delete(storageTestContext, resource.DeleteRequest{State: read.State}, &remove)
	storageTestCheck(t, remove.Diagnostics)
	if !deleted || billingCalls != 1 {
		t.Fatalf("delete=%v billing calls=%d", deleted, billingCalls)
	}
}

func TestBucketDeleteFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, counters               string
		status                       int
		force, wantDelete, wantError bool
	}{
		{name: "objects", counters: `,"object_count":1,"total_size":0`, status: 200, wantError: true},
		{name: "bytes", counters: `,"object_count":0,"total_size":1`, status: 200, wantError: true},
		{name: "missing counters", status: 200, wantError: true},
		{name: "negative counters", counters: `,"object_count":-1,"total_size":0`, status: 200, wantError: true},
		{name: "forbidden", status: 403, wantError: true},
		{name: "gone", status: 404},
		{name: "empty", counters: `,"object_count":0,"total_size":0`, status: 200, wantDelete: true},
		{name: "explicit force", force: true, wantDelete: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deleted := false
			r := &bucketResource{client: storageTestClient(t, func(w http.ResponseWriter, req *http.Request) {
				if req.Method == http.MethodGet {
					w.WriteHeader(tc.status)
					fmt.Fprint(w, storageTestBucketJSON(false, tc.counters))
					return
				}
				if req.Method != http.MethodDelete || req.URL.Path != "/object-storage/buckets/assets" {
					t.Errorf("unexpected request %s %s", req.Method, req.URL)
				}
				deleted = true
				fmt.Fprint(w, `{"detail":"deleted"}`)
			})}
			model := storageTestBucketModel()
			model.ForceDestroy = types.BoolValue(tc.force)
			state := storageTestState(t, r, model)
			response := resource.DeleteResponse{State: state}
			r.Delete(storageTestContext, resource.DeleteRequest{State: state}, &response)
			if response.Diagnostics.HasError() != tc.wantError || deleted != tc.wantDelete {
				t.Fatalf("deleted=%v diagnostics=%v", deleted, response.Diagnostics)
			}
		})
	}
}

func TestBucketReadDoesNotForgetOnForbiddenOrMalformed(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		removed    bool
	}{
		{"forbidden", `{"detail":"blocked"}`, 403, false},
		{"malformed", `{}`, 200, false},
		{"gone", `{"detail":"not found"}`, 404, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &bucketResource{client: storageTestClient(t, func(w http.ResponseWriter, req *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) })}
			state := storageTestState(t, r, storageTestBucketModel())
			response := resource.ReadResponse{State: state}
			r.Read(storageTestContext, resource.ReadRequest{State: state}, &response)
			if response.State.Raw.IsNull() != tc.removed {
				t.Fatalf("wrong removal behavior: %v", response.Diagnostics)
			}
			if !tc.removed && !response.Diagnostics.HasError() {
				t.Fatal("expected error preserving state")
			}
		})
	}
}

func TestStorageBillingDenialPreventsCreates(t *testing.T) {
	client := storageTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/billing/resource-eligibility" {
			t.Errorf("purchase despite denied billing: %s", req.URL)
			http.Error(w, "unexpected", 500)
			return
		}
		storageTestBilling(w, false)
	})
	for _, tc := range []struct {
		name     string
		resource resource.Resource
		model    any
	}{
		{"bucket", &bucketResource{client: client}, storageTestBucketModel()},
		{"store", &secretStoreResource{client: client}, storageTestStoreModel()},
		{"secret", &secretResource{client: client}, storageTestSecretModel()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := storageTestState(t, tc.resource, tc.model)
			config := state
			if tc.name == "secret" {
				cfg := storageTestSecretModel()
				cfg.ValueWO = types.StringValue(`{"password":"test-secret"}`)
				config = storageTestState(t, tc.resource, cfg)
			}
			response := resource.CreateResponse{State: state}
			tc.resource.Create(storageTestContext, resource.CreateRequest{Plan: storageTestPlan(state), Config: storageTestConfig(config)}, &response)
			if !response.Diagnostics.HasError() {
				t.Fatal("billing rejection did not fail creation")
			}
		})
	}
}

func storageTestStoreModel() secretStoreResourceModel {
	return secretStoreResourceModel{ID: types.StringValue("store-1"), Name: types.StringValue("production"), Description: types.StringValue(""), StoreKey: types.StringValue("production"), Status: types.StringValue("active"), ForceArchive: types.BoolValue(false)}
}

func storageTestSecretModel() secretResourceModel {
	return secretResourceModel{ID: types.StringValue("secret-1"), StoreID: types.StringValue("store-1"), SecretName: types.StringValue("database-url"), ValueWO: types.StringNull(), ValueWOVersion: types.Int64Value(0), CurrentVersion: types.Int64Value(1), StoreKey: types.StringValue("production"), Status: types.StringValue("active")}
}

func TestSecretStoreArchiveChecksAllPages(t *testing.T) {
	for _, activeSecondPage := range []bool{false, true} {
		t.Run(fmt.Sprintf("active_second_page_%t", activeSecondPage), func(t *testing.T) {
			pages, archived := 0, false
			r := &secretStoreResource{client: storageTestClient(t, func(w http.ResponseWriter, req *http.Request) {
				switch {
				case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/archive"):
					archived = true
					fmt.Fprint(w, `{"id":"store-1","status":"archived"}`)
				case strings.HasSuffix(req.URL.Path, "/secrets"):
					pages++
					if req.URL.Query().Get("page") == "1" {
						fmt.Fprint(w, `{"total":2,"secrets":[{"id":"secret-1","status":"soft_deleted"}]}`)
					} else if activeSecondPage {
						fmt.Fprint(w, `{"total":2,"secrets":[{"id":"secret-2","status":"active"}]}`)
					} else {
						fmt.Fprint(w, `{"total":2,"secrets":[{"id":"secret-2","status":"deleted"}]}`)
					}
				default:
					fmt.Fprint(w, `{"id":"store-1","name":"production","description":"","store_key":"production","status":"active"}`)
				}
			})}
			state := storageTestState(t, r, storageTestStoreModel())
			response := resource.DeleteResponse{State: state}
			r.Delete(storageTestContext, resource.DeleteRequest{State: state}, &response)
			if pages != 2 || archived == activeSecondPage || response.Diagnostics.HasError() != activeSecondPage {
				t.Fatalf("pages=%d archive=%v diagnostics=%v", pages, archived, response.Diagnostics)
			}
		})
	}
}

func TestSecretStoreMalformedListPreventsArchive(t *testing.T) {
	for _, body := range []string{`{}`, `{"total":0}`, `{"secrets":[],"total":1}`, `{"secrets":[{"id":"secret-1"}],"total":1}`} {
		r := &secretStoreResource{client: storageTestClient(t, func(w http.ResponseWriter, req *http.Request) { fmt.Fprint(w, body) })}
		if err := r.verifyNoActiveSecrets(storageTestContext, "store-1"); err == nil {
			t.Errorf("accepted invalid secret list %s", body)
		}
	}
}

func TestSecretWriteOnlyLifecycleAndCAS(t *testing.T) {
	currentVersion := int64(1)
	created, putCalls, deleteCalls := false, 0, 0
	secretValue := "very-private-never-in-state"
	r := &secretResource{client: storageTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.URL.Path == "/billing/resource-eligibility":
			storageTestBilling(w, true)
		case req.Method == http.MethodPost:
			var body struct {
				Value map[string]string `json:"value"`
				Name  string            `json:"secret_name"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Value["password"] != secretValue || body.Name != "database-url" {
				t.Errorf("unexpected create body %v", body)
			}
			created = true
			fmt.Fprint(w, `{"id":"secret-1","store_id":"store-1","secret_name":"database-url","store_key":"production","status":"active"}`)
		case req.Method == http.MethodPut:
			putCalls++
			var body struct {
				Value map[string]string `json:"value"`
				CAS   int64             `json:"cas"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.CAS != currentVersion || body.Value["password"] != secretValue+"-rotated" {
				t.Errorf("unexpected CAS rotation: %v", body)
			}
			currentVersion++
			fmt.Fprintf(w, `{"id":"secret-1","data":{"password":"%s"},"metadata":{"version":%d}}`, secretValue+"-rotated", currentVersion)
		case req.Method == http.MethodDelete:
			deleteCalls++
			fmt.Fprint(w, `{"id":"secret-1","store_id":"store-1","secret_name":"database-url","store_key":"production","status":"soft_deleted"}`)
		case strings.HasSuffix(req.URL.Path, "/value"):
			fmt.Fprintf(w, `{"id":"secret-1","data":{"password":"%s"},"metadata":{"version":%d}}`, secretValue, currentVersion)
		default:
			fmt.Fprint(w, `{"id":"secret-1","store_id":"store-1","secret_name":"database-url","store_key":"production","status":"active"}`)
		}
	})}
	model := storageTestSecretModel()
	model.ID = types.StringUnknown()
	plan := storageTestState(t, r, model)
	cfg := model
	cfg.ValueWO = types.StringValue(`{"password":"` + secretValue + `"}`)
	create := resource.CreateResponse{State: plan}
	r.Create(storageTestContext, resource.CreateRequest{Plan: storageTestPlan(plan), Config: storageTestConfig(storageTestState(t, r, cfg))}, &create)
	storageTestCheck(t, create.Diagnostics)
	if !created {
		t.Fatal("secret not created")
	}
	if strings.Contains(create.State.Raw.String(), secretValue) {
		t.Fatal("plaintext leaked into created state")
	}
	var state secretResourceModel
	storageTestCheck(t, create.State.Get(storageTestContext, &state))
	if !state.ValueWO.IsNull() || state.CurrentVersion.ValueInt64() != 1 {
		t.Fatalf("unexpected created state: %+v", state)
	}
	rotate := state
	rotate.ValueWOVersion = types.Int64Value(1)
	cfg = rotate
	cfg.ValueWO = types.StringValue(`{"password":"` + secretValue + `-rotated"}`)
	update := resource.UpdateResponse{State: create.State}
	r.Update(storageTestContext, resource.UpdateRequest{Plan: storageTestPlan(storageTestState(t, r, rotate)), State: create.State, Config: storageTestConfig(storageTestState(t, r, cfg))}, &update)
	storageTestCheck(t, update.Diagnostics)
	if strings.Contains(update.State.Raw.String(), secretValue) {
		t.Fatal("plaintext leaked into updated state")
	}
	storageTestCheck(t, update.State.Get(storageTestContext, &state))
	if state.CurrentVersion.ValueInt64() != 2 || putCalls != 1 {
		t.Fatalf("wrong rotation: %+v", state)
	}
	read := resource.ReadResponse{State: update.State}
	r.Read(storageTestContext, resource.ReadRequest{State: update.State}, &read)
	storageTestCheck(t, read.Diagnostics)
	if strings.Contains(read.State.Raw.String(), secretValue) {
		t.Fatal("plaintext leaked into refreshed state")
	}
	remove := resource.DeleteResponse{State: read.State}
	r.Delete(storageTestContext, resource.DeleteRequest{State: read.State}, &remove)
	storageTestCheck(t, remove.Diagnostics)
	if deleteCalls != 1 {
		t.Fatal("secret not soft-deleted")
	}
}

func TestSecretRotationConflictDoesNotLeakOrAdvanceTrigger(t *testing.T) {
	const plaintext = "should-never-be-visible"
	r := &secretResource{client: storageTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(409)
		fmt.Fprint(w, `{"detail":"`+plaintext+`"}`)
	})}
	model := storageTestSecretModel()
	state := storageTestState(t, r, model)
	model.ValueWOVersion = types.Int64Value(1)
	plan := storageTestState(t, r, model)
	model.ValueWO = types.StringValue(`{"password":"` + plaintext + `"}`)
	config := storageTestState(t, r, model)
	response := resource.UpdateResponse{State: state}
	r.Update(storageTestContext, resource.UpdateRequest{Plan: storageTestPlan(plan), Config: storageTestConfig(config), State: state}, &response)
	if !response.Diagnostics.HasError() {
		t.Fatal("expected CAS conflict")
	}
	if strings.Contains(fmt.Sprint(response.Diagnostics), plaintext) || strings.Contains(response.State.Raw.String(), plaintext) {
		t.Fatal("secret leaked")
	}
	var after secretResourceModel
	storageTestCheck(t, response.State.Get(storageTestContext, &after))
	if after.ValueWOVersion.ValueInt64() != 0 {
		t.Fatal("failed rotation advanced trigger")
	}
}

func TestSecretTombstoneReadRetainsIdentity(t *testing.T) {
	for _, status := range []string{"deleted", "soft_deleted"} {
		r := &secretResource{client: storageTestClient(t, func(w http.ResponseWriter, req *http.Request) {
			if strings.HasSuffix(req.URL.Path, "/value") {
				t.Error("requested deleted value")
			}
			fmt.Fprintf(w, `{"id":"secret-1","store_id":"store-1","secret_name":"database-url","store_key":"production","status":"%s"}`, status)
		})}
		state := storageTestState(t, r, storageTestSecretModel())
		response := resource.ReadResponse{State: state}
		r.Read(storageTestContext, resource.ReadRequest{State: state}, &response)
		storageTestCheck(t, response.Diagnostics)
		if response.State.Raw.IsNull() {
			t.Fatal("discarded retained tombstone identity")
		}
		var after secretResourceModel
		storageTestCheck(t, response.State.Get(storageTestContext, &after))
		if after.Status.ValueString() != status || !after.CurrentVersion.IsNull() {
			t.Fatalf("invalid tombstone state: %+v", after)
		}
	}
}

func TestSecretValueValidationDoesNotLeak(t *testing.T) {
	for _, raw := range []string{`["private"]`, `null`, `"private"`, `{"private":`, `{"private":1} {"private":2}`} {
		_, err := decodeSecretValue(types.StringValue(raw))
		if err == nil {
			t.Errorf("invalid object accepted: %s", raw)
		} else if strings.Contains(err.Error(), "private") {
			t.Fatal("plaintext in validation diagnostic")
		}
	}
	value, err := decodeSecretValue(types.StringValue(`{"count":9007199254740993,"nested":{"list":[true,1]}}`))
	if err != nil || value["count"].(json.Number).String() != "9007199254740993" {
		t.Fatal("large numeric secret was not preserved")
	}
}

func TestStorageImportsHaveSafeLocalDefaults(t *testing.T) {
	for _, tc := range []struct {
		r     resource.ResourceWithImportState
		id    string
		model any
	}{
		{&bucketResource{}, "assets", &bucketResourceModel{}},
		{&secretStoreResource{}, "store-1", &secretStoreResourceModel{}},
		{&secretResource{}, "secret-1", &secretResourceModel{}},
	} {
		t.Run(tc.id, func(t *testing.T) {
			state := storageTestState(t, tc.r, tc.model)
			response := resource.ImportStateResponse{State: state}
			tc.r.ImportState(storageTestContext, resource.ImportStateRequest{ID: tc.id}, &response)
			storageTestCheck(t, response.Diagnostics)
			storageTestCheck(t, response.State.Get(storageTestContext, tc.model))
			switch model := tc.model.(type) {
			case *bucketResourceModel:
				if model.ID.ValueString() != tc.id || model.ForceDestroy.ValueBool() || model.ForceDestroy.IsNull() {
					t.Fatalf("bad bucket import: %+v", model)
				}
			case *secretStoreResourceModel:
				if model.ID.ValueString() != tc.id || model.ForceArchive.ValueBool() || model.ForceArchive.IsNull() {
					t.Fatalf("bad store import: %+v", model)
				}
			case *secretResourceModel:
				if model.ID.ValueString() != tc.id || model.ValueWOVersion.ValueInt64() != 0 || !model.ValueWO.IsNull() {
					t.Fatalf("bad secret import: %+v", model)
				}
			}
		})
	}
}
