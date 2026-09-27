package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func retentionTestModel() bucketRetentionResourceModel {
	return bucketRetentionResourceModel{ID: types.StringValue("assets"), BucketName: types.StringValue("assets"), Mode: types.StringValue("GOVERNANCE"), Days: types.Int64Value(30), Years: types.Int64Null(), RetainOnDestroy: types.BoolValue(false)}
}

func TestBucketRetentionLifecycleAndExplicitRetain(t *testing.T) {
	var remoteRule *bucketRetentionRule
	writes := 0
	r := &bucketRetentionResource{client: storageTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/object-storage/buckets/assets" {
			fmt.Fprint(w, `{"name":"assets","bucket_lock_enabled":true}`)
			return
		}
		if req.URL.Path != "/object-storage/buckets/assets/object-lock-configuration" {
			t.Errorf("unexpected path: %s", req.URL.Path)
			http.Error(w, "unexpected", 500)
			return
		}
		if req.Method == http.MethodPut {
			writes++
			var body struct {
				Rule *bucketRetentionRule `json:"rule"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Rule == nil {
				t.Fatal("provider attempted unsupported retention clearing")
			}
			remoteRule = body.Rule
			fmt.Fprint(w, `{"detail":"Object Lock configuration set successfully"}`)
			return
		}
		if req.Method != http.MethodGet {
			t.Errorf("unexpected mutation: %s", req.Method)
		}
		json.NewEncoder(w).Encode(map[string]any{"object_lock_enabled": "Enabled", "rule": remoteRule})
	})}
	model := retentionTestModel()
	model.ID = types.StringUnknown()
	plan := storageTestState(t, r, model)
	create := resource.CreateResponse{State: plan}
	r.Create(storageTestContext, resource.CreateRequest{Plan: storageTestPlan(plan)}, &create)
	storageTestCheck(t, create.Diagnostics)
	if writes != 1 || remoteRule == nil || remoteRule.Days == nil || *remoteRule.Days != 30 {
		t.Fatal("default rule was not created")
	}
	var state bucketRetentionResourceModel
	storageTestCheck(t, create.State.Get(storageTestContext, &state))
	if state.ID.ValueString() != "assets" || state.Mode.ValueString() != "GOVERNANCE" {
		t.Fatalf("wrong state: %+v", state)
	}
	planModel := state
	planModel.Mode = types.StringValue("COMPLIANCE")
	planModel.Days = types.Int64Null()
	planModel.Years = types.Int64Value(1)
	update := resource.UpdateResponse{State: create.State}
	r.Update(storageTestContext, resource.UpdateRequest{State: create.State, Plan: storageTestPlan(storageTestState(t, r, planModel))}, &update)
	storageTestCheck(t, update.Diagnostics)
	if writes != 2 || remoteRule.Mode != "COMPLIANCE" || remoteRule.Days != nil || remoteRule.Years == nil || *remoteRule.Years != 1 {
		t.Fatal("duration was not switched correctly")
	}
	blocked := resource.DeleteResponse{State: update.State}
	r.Delete(storageTestContext, resource.DeleteRequest{State: update.State}, &blocked)
	if !blocked.Diagnostics.HasError() || blocked.State.Raw.IsNull() || writes != 2 {
		t.Fatal("default destroy silently relinquished or cleared retention")
	}
	storageTestCheck(t, update.State.Get(storageTestContext, &state))
	state.RetainOnDestroy = types.BoolValue(true)
	permit := resource.UpdateResponse{State: update.State}
	r.Update(storageTestContext, resource.UpdateRequest{State: update.State, Plan: storageTestPlan(storageTestState(t, r, state))}, &permit)
	storageTestCheck(t, permit.Diagnostics)
	if writes != 2 {
		t.Fatal("changing local retain flag rewrote remote WORM policy")
	}
	remove := resource.DeleteResponse{State: permit.State}
	r.Delete(storageTestContext, resource.DeleteRequest{State: permit.State}, &remove)
	storageTestCheck(t, remove.Diagnostics)
	if len(remove.Diagnostics) == 0 || writes != 2 || remoteRule.Mode != "COMPLIANCE" {
		t.Fatal("retained rule unexpectedly changed or no warning emitted")
	}
}

func TestBucketRetentionCreateDoesNotOverwriteExisting(t *testing.T) {
	writes := 0
	r := &bucketRetentionResource{client: storageTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			writes++
			t.Error("existing policy overwritten")
		}
		if req.URL.Path == "/object-storage/buckets/assets" {
			fmt.Fprint(w, `{"name":"assets","bucket_lock_enabled":true}`)
			return
		}
		fmt.Fprint(w, `{"object_lock_enabled":"Enabled","rule":{"mode":"COMPLIANCE","years":5}}`)
	})}
	state := storageTestState(t, r, retentionTestModel())
	response := resource.CreateResponse{State: state}
	r.Create(storageTestContext, resource.CreateRequest{Plan: storageTestPlan(state)}, &response)
	if !response.Diagnostics.HasError() || writes != 0 {
		t.Fatal("existing policy was not protected")
	}
}

func TestBucketRetention404DoesNotHideMissingEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		bucketCode             int
		lock                   bool
		wantRemoved, wantError bool
	}{
		{name: "missing endpoint", bucketCode: 200, lock: true, wantError: true},
		{name: "missing bucket", bucketCode: 404, wantRemoved: true},
		{name: "lock disabled", bucketCode: 200, lock: false, wantRemoved: true},
		{name: "bucket access denied", bucketCode: 403, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &bucketRetentionResource{client: storageTestClient(t, func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path == "/object-storage/buckets/assets" {
					w.WriteHeader(tc.bucketCode)
					fmt.Fprintf(w, `{"name":"assets","bucket_lock_enabled":%t}`, tc.lock)
					return
				}
				w.WriteHeader(404)
			})}
			state := storageTestState(t, r, retentionTestModel())
			response := resource.ReadResponse{State: state}
			r.Read(storageTestContext, resource.ReadRequest{State: state}, &response)
			if response.Diagnostics.HasError() != tc.wantError || response.State.Raw.IsNull() != tc.wantRemoved {
				t.Fatalf("unexpected read: %v", response.Diagnostics)
			}
		})
	}
}

func TestBucketRetentionValidation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		modify    func(*bucketRetentionResourceModel)
		wantError bool
	}{
		{"days", func(*bucketRetentionResourceModel) {}, false},
		{"years", func(m *bucketRetentionResourceModel) { m.Days = types.Int64Null(); m.Years = types.Int64Value(100) }, false},
		{"both", func(m *bucketRetentionResourceModel) { m.Years = types.Int64Value(1) }, true},
		{"neither", func(m *bucketRetentionResourceModel) { m.Days = types.Int64Null() }, true},
		{"invalid mode", func(m *bucketRetentionResourceModel) { m.Mode = types.StringValue("none") }, true},
		{"zero", func(m *bucketRetentionResourceModel) { m.Days = types.Int64Value(0) }, true},
		{"too many days", func(m *bucketRetentionResourceModel) { m.Days = types.Int64Value(36501) }, true},
		{"too many years", func(m *bucketRetentionResourceModel) { m.Days = types.Int64Null(); m.Years = types.Int64Value(101) }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &bucketRetentionResource{}
			model := retentionTestModel()
			tc.modify(&model)
			response := resource.ValidateConfigResponse{}
			r.ValidateConfig(storageTestContext, resource.ValidateConfigRequest{Config: storageTestConfig(storageTestState(t, r, model))}, &response)
			if response.Diagnostics.HasError() != tc.wantError {
				t.Fatalf("validation: %v", response.Diagnostics)
			}
		})
	}
}

func TestBucketRetentionMalformedReadPreservesState(t *testing.T) {
	for _, body := range []string{`{}`, `{"object_lock_enabled":"Enabled"}`, `{"object_lock_enabled":"Enabled","rule":{}}`, `{"object_lock_enabled":"Enabled","rule":{"mode":"GOVERNANCE","days":1,"years":1}}`, `{"object_lock_enabled":"Enabled","rule":{"mode":"GOVERNANCE","days":0}}`} {
		r := &bucketRetentionResource{client: storageTestClient(t, func(w http.ResponseWriter, req *http.Request) { fmt.Fprint(w, body) })}
		state := storageTestState(t, r, retentionTestModel())
		response := resource.ReadResponse{State: state}
		r.Read(storageTestContext, resource.ReadRequest{State: state}, &response)
		if !response.Diagnostics.HasError() || response.State.Raw.IsNull() {
			t.Fatalf("accepted malformed retention: %s", body)
		}
	}
}

func TestBucketRetentionImportDoesNotOptIntoRetainingOnDestroy(t *testing.T) {
	r := &bucketRetentionResource{}
	state := storageTestState(t, r, &bucketRetentionResourceModel{})
	response := resource.ImportStateResponse{State: state}
	r.ImportState(storageTestContext, resource.ImportStateRequest{ID: "assets"}, &response)
	storageTestCheck(t, response.Diagnostics)
	var model bucketRetentionResourceModel
	storageTestCheck(t, response.State.Get(storageTestContext, &model))
	if model.ID.ValueString() != "assets" || model.BucketName.ValueString() != "assets" || model.RetainOnDestroy.ValueBool() || model.RetainOnDestroy.IsNull() {
		t.Fatalf("unsafe imported state: %+v", model)
	}
}

func TestBucketForceDestroyPreservesStateWhenServerRejectsNonempty(t *testing.T) {
	r := &bucketResource{client: storageTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodDelete {
			t.Errorf("unexpected request: %s", req.Method)
		}
		w.WriteHeader(409)
		fmt.Fprint(w, `{"detail":"Bucket is not empty"}`)
	})}
	model := storageTestBucketModel()
	model.ForceDestroy = types.BoolValue(true)
	state := storageTestState(t, r, model)
	response := resource.DeleteResponse{State: state}
	r.Delete(storageTestContext, resource.DeleteRequest{State: state}, &response)
	if !response.Diagnostics.HasError() || response.State.Raw.IsNull() {
		t.Fatal("force_destroy hid server nonempty rejection")
	}
}
