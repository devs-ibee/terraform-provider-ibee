package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
						if entry["allowed_origins"] == nil || entry["AllowedOrigins"] != nil {
							t.Errorf("CORS write must use the contract's snake_case keys: %v", entry)
						}
						// The API's CORS read answers with S3-cased keys.
						cased := map[string]any{}
						for name, value := range entry {
							cased[bucketCORSAliases[name]] = value
						}
						stored[0] = cased
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
