package provider

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Each resource owns one bucket settings section of the public API:
// GET, PUT (replace every rule) and DELETE /object-storage/buckets/{bucket_name}/{section}.
type bucketConfigurationResource struct {
	*networkResource
	section, collection string
}

func newBucketConfiguration(section, collection, description string, fields map[string]schema.Attribute) *bucketConfigurationResource {
	return &bucketConfigurationResource{
		section: section, collection: collection,
		networkResource: &networkResource{
			name:         "bucket_" + section,
			description:  description + " Create and update replace every rule of the section (PUT); destroy deletes the section (DELETE). Import using the bucket name.",
			importFields: []string{"bucket_name"},
			attributes: map[string]schema.Attribute{
				"id":          networkIDAttribute(),
				"bucket_name": networkRequired(true),
				collection:    schema.ListNestedAttribute{Required: true, Description: "The rules, in order.", NestedObject: schema.NestedAttributeObject{Attributes: fields}},
			},
		},
	}
}

func NewBucketCORSResource() resource.Resource {
	return newBucketConfiguration("cors", "rules", "The CORS rules of a bucket.", map[string]schema.Attribute{
		"id":              schema.StringAttribute{Optional: true},
		"allowed_origins": schema.ListAttribute{Required: true, ElementType: types.StringType, Description: "Origins, such as https://example.com or *."},
		"allowed_methods": schema.ListAttribute{Required: true, ElementType: types.StringType, Description: "HTTP methods, such as GET or PUT."},
		"allowed_headers": schema.ListAttribute{Optional: true, ElementType: types.StringType},
		"expose_headers":  schema.ListAttribute{Optional: true, ElementType: types.StringType},
		"max_age_seconds": schema.Int64Attribute{Optional: true},
	})
}

func NewBucketLifecycleResource() resource.Resource {
	return newBucketConfiguration("lifecycle", "rules", "The lifecycle (object expiration) rules of a bucket. An enabled rule deletes matching objects after expiration_days or on expiration_date.", map[string]schema.Attribute{
		"status":          schema.StringAttribute{Required: true, Description: "Enabled or Disabled."},
		"prefix":          schema.StringAttribute{Optional: true},
		"expiration_days": schema.Int64Attribute{Optional: true, Description: "Days after an object is written. Give this or expiration_date."},
		"expiration_date": schema.StringAttribute{Optional: true, Description: "RFC 3339 UTC timestamp, for example 2030-01-01T00:00:00Z. Give this or expiration_days."},
	})
}

func NewBucketNotificationsResource() resource.Resource {
	return newBucketConfiguration("notifications", "configs", "The event notification (webhook) rules of a bucket.", map[string]schema.Attribute{
		"id":        schema.StringAttribute{Optional: true},
		"events":    schema.ListAttribute{Required: true, ElementType: types.StringType, Description: "Event categories, such as upload and delete."},
		"s3_events": schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "S3 event names the API derives from events."},
		"filter": schema.SingleNestedAttribute{Optional: true, Attributes: map[string]schema.Attribute{
			"prefix": schema.StringAttribute{Optional: true}, "suffix": schema.StringAttribute{Optional: true},
		}},
		"webhook_url": schema.StringAttribute{Optional: true, Description: "HTTPS endpoint that receives the events."},
	})
}

func (r *bucketConfigurationResource) endpoint(v networkValues) string {
	return bucketPath(v.str("bucket_name")) + "/" + r.section
}

func (r *bucketConfigurationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	v := networkValues{"id": types.StringValue(req.ID), "bucket_name": types.StringValue(req.ID)}
	v[r.collection], _ = networkValue(r.attributes[r.collection].GetType(), nil)
	resp.Diagnostics.Append(r.set(ctx, &resp.State, v)...)
}

// bucketCORSAliases maps the snake_case keys the API takes to the S3-cased
// keys its CORS read currently returns.
var bucketCORSAliases = map[string]string{"id": "ID", "allowed_origins": "AllowedOrigins", "allowed_methods": "AllowedMethods", "allowed_headers": "AllowedHeaders", "expose_headers": "ExposeHeaders", "max_age_seconds": "MaxAgeSeconds"}

// payload is the configured rules as the API's JSON body (snake_case keys).
func (r *bucketConfigurationResource) payload(v networkValues) (map[string]any, error) {
	items := []any{}
	if list, ok := v[r.collection].(types.List); ok && !list.IsNull() && !list.IsUnknown() {
		for _, item := range list.Elements() {
			object, ok := item.(types.Object)
			if !ok {
				return nil, fmt.Errorf("%s contains an unknown entry", r.collection)
			}
			raw, err := networkJSON(object)
			if err != nil {
				return nil, err
			}
			entry := raw.(map[string]any)
			delete(entry, "s3_events")
			items = append(items, entry)
		}
	}
	return map[string]any{r.collection: items}, nil
}

func (r *bucketConfigurationResource) fetchConfiguration(ctx context.Context, v networkValues) ([]any, error) {
	var out map[string]any
	if err := r.client.do(ctx, http.MethodGet, r.endpoint(v), nil, &out); err != nil {
		return nil, err
	}
	items, _ := out[r.collection].([]any)
	for i, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if r.section == "cors" {
			// The read returns S3-cased keys although the write takes snake_case.
			normalized := map[string]any{}
			for name, alias := range bucketCORSAliases {
				value, exists := entry[alias]
				if !exists {
					value = entry[name]
				}
				normalized[name] = value
			}
			items[i] = normalized
		}
		// The API answers with +00:00; record the Z spelling of the same instant.
		if r.section == "lifecycle" {
			if actual, ok := entry["expiration_date"].(string); ok {
				if parsed, err := time.Parse(time.RFC3339Nano, actual); err == nil {
					entry["expiration_date"] = parsed.UTC().Format(time.RFC3339Nano)
				}
			}
		}
	}
	return items, nil
}

func (r *bucketConfigurationResource) setConfiguration(v networkValues, items []any) error {
	value, err := networkValue(r.attributes[r.collection].GetType(), items)
	if err != nil {
		return err
	}
	v[r.collection] = value
	v["id"] = v["bucket_name"]
	return nil
}

func (r *bucketConfigurationResource) write(ctx context.Context, v networkValues) error {
	body, err := r.payload(v)
	if err != nil {
		return err
	}
	if err := r.client.do(ctx, http.MethodPut, r.endpoint(v), body, nil); err != nil {
		return err
	}
	items, err := r.fetchConfiguration(ctx, v)
	if err != nil {
		return err
	}
	return r.setConfiguration(v, items)
}

func (r *bucketConfigurationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var object types.Object
	resp.Diagnostics.Append(req.Plan.Get(ctx, &object)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v := networkObject(object)
	if err := r.write(ctx, v); err != nil {
		resp.Diagnostics.AddError("Failed to set bucket "+r.section, err.Error())
		return
	}
	resp.Diagnostics.Append(r.set(ctx, &resp.State, v)...)
}

func (r *bucketConfigurationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var object types.Object
	resp.Diagnostics.Append(req.State.Get(ctx, &object)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v := networkObject(object)
	items, err := r.fetchConfiguration(ctx, v)
	if IsNotFound(err) || (err == nil && len(items) == 0) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err == nil {
		err = r.setConfiguration(v, items)
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read bucket "+r.section, err.Error())
		return
	}
	resp.Diagnostics.Append(r.set(ctx, &resp.State, v)...)
}

func (r *bucketConfigurationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var object types.Object
	resp.Diagnostics.Append(req.Plan.Get(ctx, &object)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v := networkObject(object)
	if err := r.write(ctx, v); err != nil {
		resp.Diagnostics.AddError("Failed to update bucket "+r.section, err.Error())
		return
	}
	resp.Diagnostics.Append(r.set(ctx, &resp.State, v)...)
}

func (r *bucketConfigurationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var object types.Object
	resp.Diagnostics.Append(req.State.Get(ctx, &object)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.do(ctx, http.MethodDelete, r.endpoint(networkObject(object)), nil, nil); err != nil && !IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to delete bucket "+r.section, err.Error())
	}
}
