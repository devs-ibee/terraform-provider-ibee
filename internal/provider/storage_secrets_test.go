package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	return bucketResourceModel{ID: types.StringValue("assets"), Name: types.StringValue("assets"), Region: types.StringValue("in-south-2"), IsPublic: types.BoolValue(false), ObjectLockEnabled: types.BoolValue(false), DefaultRetention: types.ObjectNull(bucketRetentionTypes), Status: types.StringValue("active"), ObjectCount: types.Int64Value(0), TotalSize: types.Int64Value(0)}
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
			if billingCalls != 0 {
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
	if !deleted || billingCalls != 0 {
		t.Fatalf("delete=%v billing calls=%d", deleted, billingCalls)
	}
}

func TestStorageBillingDenialPreventsCreates(t *testing.T) {
	client := storageTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/billing/resource-eligibility" {
			t.Errorf("automatic write queried billing: %s", req.URL)
		}
		w.WriteHeader(http.StatusPaymentRequired)
		fmt.Fprint(w, `{"error":"billing_denied","billing_reason":"insufficient_balance"}`)
	})
	for _, tc := range []struct {
		name     string
		resource resource.Resource
		model    any
	}{
		{"bucket", &bucketResource{client: client}, storageTestBucketModel()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := storageTestState(t, tc.resource, tc.model)
			config := state
			response := resource.CreateResponse{State: state}
			tc.resource.Create(storageTestContext, resource.CreateRequest{Plan: storageTestPlan(state), Config: storageTestConfig(config)}, &response)
			if !response.Diagnostics.HasError() {
				t.Fatal("billing rejection did not fail creation")
			}
		})
	}
}
