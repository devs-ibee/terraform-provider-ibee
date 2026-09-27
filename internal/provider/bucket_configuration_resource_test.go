package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func bucketConfigurationCases() []struct {
	name        string
	constructor func() resource.Resource
	entry       map[string]any
} {
	return []struct {
		name        string
		constructor func() resource.Resource
		entry       map[string]any
	}{
		{"cors", NewBucketCORSResource, map[string]any{"id": "web", "allowed_origins": []any{"https://example.test"}, "allowed_methods": []any{"GET", "HEAD"}, "allowed_headers": []any{"Authorization"}, "expose_headers": []any{"ETag"}, "max_age_seconds": float64(300)}},
		{"lifecycle", NewBucketLifecycleResource, map[string]any{"status": "Disabled", "prefix": "archive/", "expiration_days": float64(30)}},
		{"notifications", NewBucketNotificationsResource, map[string]any{"id": "events", "events": []any{"upload", "delete"}, "filter": map[string]any{"prefix": "images/", "suffix": ".png"}, "webhook_url": "https://example.test/events"}},
	}
}

func TestBucketConfigurationLifecycle(t *testing.T) {
	for _, tc := range bucketConfigurationCases() {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.constructor().(*bucketConfigurationResource)
			stored := []any{}
			puts, deletes := 0, 0
			r.client = networkTestClient(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path != "/object-storage/buckets/test-bucket/"+tc.name || req.URL.Query().Get("workspace_id") != "workspace" {
					t.Fatalf("unexpected route %s", req.URL)
				}
				switch req.Method {
				case http.MethodGet:
					json.NewEncoder(w).Encode(map[string]any{r.collection: stored})
				case http.MethodPut:
					puts++
					var body map[string]any
					if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					stored = body[r.collection].([]any)
					entry := stored[0].(map[string]any)
					if tc.name == "cors" {
						if entry["AllowedOrigins"] == nil || entry["allowed_origins"] != nil {
							t.Errorf("CORS aliases missing: %v", entry)
						}
					}
					if tc.name == "notifications" {
						if _, exists := entry["s3_events"]; exists {
							t.Error("output-only s3_events sent")
						}
						entry["s3_events"] = []any{"s3:ObjectCreated:*", "s3:ObjectRemoved:*"}
					}
					fmt.Fprint(w, `{"detail":"stored"}`)
				case http.MethodDelete:
					deletes++
					stored = []any{}
					fmt.Fprint(w, `{"detail":"deleted"}`)
				default:
					t.Fatalf("unexpected method %s", req.Method)
				}
			})
			values := networkTestValues(t, r.networkResource, map[string]any{"bucket_name": "test-bucket", r.collection: []any{tc.entry}})
			s := networkTestSchema(r)
			plan := networkTestPlan(t, s, types.ObjectValueMust(r.attributeTypes(), values))
			created := resource.CreateResponse{State: tfsdk.State{Schema: s}}
			r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &created)
			if created.Diagnostics.HasError() {
				t.Fatal(created.Diagnostics)
			}
			var result types.Object
			if d := created.State.Get(context.Background(), &result); d.HasError() {
				t.Fatal(d)
			}
			if networkObject(result).str("id") != "test-bucket" {
				t.Fatal("incorrect ID")
			}
			if tc.name == "notifications" {
				out := networkObject(result)["configs"].(types.List).Elements()[0].(types.Object).Attributes()["s3_events"].(types.List)
				if len(out.Elements()) != 2 {
					t.Fatal("missing computed expanded events")
				}
			}
			read := resource.ReadResponse{State: created.State}
			r.Read(context.Background(), resource.ReadRequest{State: created.State}, &read)
			if read.Diagnostics.HasError() || !read.State.Raw.Equal(created.State.Raw) {
				t.Fatalf("read changed state: %v", read.Diagnostics)
			}
			imported := resource.ImportStateResponse{State: tfsdk.State{Schema: s}}
			r.ImportState(context.Background(), resource.ImportStateRequest{ID: "test-bucket"}, &imported)
			if imported.Diagnostics.HasError() {
				t.Fatal(imported.Diagnostics)
			}
			importRead := resource.ReadResponse{State: imported.State}
			r.Read(context.Background(), resource.ReadRequest{State: imported.State}, &importRead)
			if importRead.Diagnostics.HasError() || !importRead.State.Raw.Equal(created.State.Raw) {
				t.Fatalf("import differs: %v", importRead.Diagnostics)
			}
			updated := resource.UpdateResponse{State: created.State}
			r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: created.State}, &updated)
			if updated.Diagnostics.HasError() || puts != 2 {
				t.Fatalf("puts=%d %v", puts, updated.Diagnostics)
			}
			deleted := resource.DeleteResponse{State: updated.State}
			r.Delete(context.Background(), resource.DeleteRequest{State: updated.State}, &deleted)
			if deleted.Diagnostics.HasError() || deletes != 1 || len(stored) != 0 {
				t.Fatalf("deletes=%d %v", deletes, deleted.Diagnostics)
			}
			gone := resource.ReadResponse{State: updated.State}
			r.Read(context.Background(), resource.ReadRequest{State: updated.State}, &gone)
			if gone.Diagnostics.HasError() || !gone.State.Raw.IsNull() {
				t.Fatal("empty section not removed from state")
			}
		})
	}
}

func TestBucketConfigurationCreatePreservesExistingAndMalformed(t *testing.T) {
	for _, tc := range bucketConfigurationCases() {
		for _, malformed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/malformed=%t", tc.name, malformed), func(t *testing.T) {
				r := tc.constructor().(*bucketConfigurationResource)
				entry := tc.entry
				if tc.name == "notifications" {
					entry["s3_events"] = []any{"s3:ObjectCreated:*", "s3:ObjectRemoved:*"}
				}
				mutations := 0
				r.client = networkTestClient(func(w http.ResponseWriter, req *http.Request) {
					if req.Method != http.MethodGet {
						mutations++
					}
					if malformed {
						fmt.Fprint(w, `{}`)
						return
					}
					json.NewEncoder(w).Encode(map[string]any{r.collection: []any{entry}})
				})
				s := networkTestSchema(r)
				v := networkTestValues(t, r.networkResource, map[string]any{"bucket_name": "test-bucket", r.collection: []any{tc.entry}})
				resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
				r.Create(context.Background(), resource.CreateRequest{Plan: networkTestPlan(t, s, types.ObjectValueMust(r.attributeTypes(), v))}, &resp)
				if !resp.Diagnostics.HasError() || mutations != 0 || !resp.State.Raw.IsNull() {
					t.Fatalf("existing config adopted or overwritten: mutations=%d diagnostics=%v", mutations, resp.Diagnostics)
				}
			})
		}
	}
}

func TestBucketConfigurationFailedReadAndDeletePreserveState(t *testing.T) {
	for _, tc := range bucketConfigurationCases() {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.constructor().(*bucketConfigurationResource)
			if tc.name == "notifications" {
				tc.entry["s3_events"] = []any{"s3:ObjectCreated:*", "s3:ObjectRemoved:*"}
			}
			v := networkTestValues(t, r.networkResource, map[string]any{"id": "test-bucket", "bucket_name": "test-bucket", r.collection: []any{tc.entry}})
			state := networkTestState(t, networkTestSchema(r), types.ObjectValueMust(r.attributeTypes(), v))
			for _, body := range []string{`{}`, `null`, fmt.Sprintf(`{"%s":null}`, r.collection), fmt.Sprintf(`{"%s":[{}]}`, r.collection)} {
				r.client = networkTestClient(func(w http.ResponseWriter, req *http.Request) { fmt.Fprint(w, body) })
				read := resource.ReadResponse{State: state}
				r.Read(context.Background(), resource.ReadRequest{State: state}, &read)
				if !read.Diagnostics.HasError() || !read.State.Raw.Equal(state.Raw) {
					t.Fatalf("malformed response changed state %s: %v", body, read.Diagnostics)
				}
			}
			r.client = networkTestClient(func(w http.ResponseWriter, req *http.Request) {
				if req.Method == http.MethodDelete {
					fmt.Fprint(w, `{"detail":"deleted"}`)
					return
				}
				json.NewEncoder(w).Encode(map[string]any{r.collection: []any{tc.entry}})
			})
			deleted := resource.DeleteResponse{State: state}
			r.Delete(context.Background(), resource.DeleteRequest{State: state}, &deleted)
			if !deleted.Diagnostics.HasError() || !deleted.State.Raw.Equal(state.Raw) {
				t.Fatal("nonempty config accepted as deleted")
			}
		})
	}
}

func TestBucketConfigurationWriteFailureRetainsIdentity(t *testing.T) {
	r := NewBucketNotificationsResource().(*bucketConfigurationResource)
	written := false
	r.client = networkTestClient(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPut {
			written = true
			fmt.Fprint(w, `{}`)
			return
		}
		if written {
			fmt.Fprint(w, `{}`)
		} else {
			fmt.Fprint(w, `{"configs":[]}`)
		}
	})
	v := networkTestValues(t, r.networkResource, map[string]any{"bucket_name": "test-bucket", "configs": []any{map[string]any{"events": []any{"upload"}}}})
	s := networkTestSchema(r)
	resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{Plan: networkTestPlan(t, s, types.ObjectValueMust(r.attributeTypes(), v))}, &resp)
	if !resp.Diagnostics.HasError() || resp.State.Raw.IsNull() {
		t.Fatal("successful write lost its identity after malformed refresh")
	}
	var out types.Object
	resp.State.Get(context.Background(), &out)
	if networkObject(out).str("id") != "test-bucket" {
		t.Fatal("missing recoverable bucket identity")
	}
}

func TestBucketConfigurationValidationAndTimestampNormalization(t *testing.T) {
	cases := []struct {
		resource      resource.Resource
		entry         map[string]any
		errorContains string
	}{
		{NewBucketCORSResource(), map[string]any{"allowed_origins": []any{"example.test"}, "allowed_methods": []any{"GET"}}, "HTTP(S)"},
		{NewBucketCORSResource(), map[string]any{"allowed_origins": []any{"*"}, "allowed_methods": []any{"PATCH"}}, "method"},
		{NewBucketCORSResource(), map[string]any{"allowed_origins": []any{"*"}, "allowed_methods": []any{"GET"}, "max_age_seconds": float64(-1)}, "nonnegative"},
		{NewBucketLifecycleResource(), map[string]any{"status": "Enabled"}, "exactly one"},
		{NewBucketLifecycleResource(), map[string]any{"status": "Enabled", "expiration_days": float64(1), "expiration_date": "2100-01-01T00:00:00Z"}, "exactly one"},
		{NewBucketLifecycleResource(), map[string]any{"status": "Enabled", "expiration_days": float64(0)}, "positive"},
		{NewBucketLifecycleResource(), map[string]any{"status": "Enabled", "expiration_date": "tomorrow"}, "RFC3339"},
		{NewBucketNotificationsResource(), map[string]any{"events": []any{"s3:ObjectCreated:*"}}, "upload or delete"},
		{NewBucketNotificationsResource(), map[string]any{"events": []any{"upload"}, "webhook_url": "ftp://example.test"}, "HTTP(S)"},
	}
	for _, tc := range cases {
		r := tc.resource.(*bucketConfigurationResource)
		v := networkTestValues(t, r.networkResource, map[string]any{"bucket_name": "test-bucket", r.collection: []any{tc.entry}})
		_, err := r.payload(v)
		if err == nil || !strings.Contains(err.Error(), tc.errorContains) {
			t.Fatalf("entry=%v error=%v", tc.entry, err)
		}
	}
	r := NewBucketLifecycleResource().(*bucketConfigurationResource)
	v := networkTestValues(t, r.networkResource, map[string]any{"id": "test-bucket", "bucket_name": "test-bucket", "rules": []any{map[string]any{"status": "Disabled", "expiration_date": "2100-01-01T00:00:00Z"}}})
	r.client = networkTestClient(func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprint(w, `{"rules":[{"status":"Disabled","expiration_date":"2100-01-01T00:00:00+00:00"}]}`)
	})
	items, err := r.fetchConfiguration(context.Background(), v)
	if err != nil || !reflect.DeepEqual(items[0].(map[string]any)["expiration_date"], "2100-01-01T00:00:00Z") {
		t.Fatalf("items=%v err=%v", items, err)
	}
}
