package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Each resource owns one complete bucket configuration section. Reusing only
// the typed conversion/schema helpers leaves singleton lifecycle semantics here.
type bucketConfigurationResource struct {
	*networkResource
	section, collection string
}

var _ resource.ResourceWithValidateConfig = (*bucketConfigurationResource)(nil)

func newBucketConfiguration(section, collection, description string, fields map[string]schema.Attribute) *bucketConfigurationResource {
	return &bucketConfigurationResource{
		section: section, collection: collection,
		networkResource: &networkResource{
			name:         "bucket_" + section,
			description:  description + " Owns the complete configuration section. Import using the bucket name before managing existing nonempty configuration. Creation refuses to overwrite it; the preflight is not atomic, so use one writer per bucket section. Destroy clears this section and confirms an empty API response.",
			importFields: []string{"bucket_name"},
			attributes: map[string]schema.Attribute{
				"id":          networkIDAttribute(),
				"bucket_name": networkRequired(true),
				collection:    schema.ListNestedAttribute{Required: true, Description: "At least one entry. Order is preserved. Remove this Terraform resource to clear the complete section.", NestedObject: schema.NestedAttributeObject{Attributes: fields}},
			},
		},
	}
}

func NewBucketCORSResource() resource.Resource {
	return newBucketConfiguration("cors", "rules", "Stored bucket CORS rules. The service cannot apply CORS directly to MinIO. It stores metadata and attempts public-URL/CDN policy synchronization; an API success does not prove browser enforcement.", map[string]schema.Attribute{
		"id":              schema.StringAttribute{Optional: true},
		"allowed_origins": schema.ListAttribute{Required: true, ElementType: types.StringType, Description: "HTTP(S) origins or *."},
		"allowed_methods": schema.ListAttribute{Required: true, ElementType: types.StringType, Description: "GET, PUT, POST, DELETE, or HEAD."},
		"allowed_headers": schema.ListAttribute{Optional: true, ElementType: types.StringType},
		"expose_headers":  schema.ListAttribute{Optional: true, ElementType: types.StringType},
		"max_age_seconds": schema.Int64Attribute{Optional: true},
	})
}

func NewBucketLifecycleResource() resource.Resource {
	return newBucketConfiguration("lifecycle", "rules", "Bucket object-expiration rules. Enabled rules can permanently delete matching objects after the configured age or date. Readback confirms stored configuration, not that expiration has run.", map[string]schema.Attribute{
		"status":          schema.StringAttribute{Required: true, Description: "Enabled or Disabled."},
		"prefix":          schema.StringAttribute{Optional: true},
		"expiration_days": schema.Int64Attribute{Optional: true, Description: "Positive age in days; exactly one of expiration_days or expiration_date is required."},
		"expiration_date": schema.StringAttribute{Optional: true, Description: "Canonical RFC3339 UTC timestamp ending in Z (for example 2030-01-01T00:00:00Z); mutually exclusive with expiration_days."},
	})
}

func NewBucketNotificationsResource() resource.Resource {
	return newBucketConfiguration("notifications", "configs", "Bucket event notification configuration. Readback verifies configuration only; delivery to a webhook and the configured regional notification destination must be tested separately.", map[string]schema.Attribute{
		"id":        schema.StringAttribute{Optional: true},
		"events":    schema.ListAttribute{Required: true, ElementType: types.StringType, Description: "Event categories: upload and/or delete."},
		"s3_events": schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "S3 event names expanded by the API."},
		"filter": schema.SingleNestedAttribute{Optional: true, Attributes: map[string]schema.Attribute{
			"prefix": schema.StringAttribute{Optional: true}, "suffix": schema.StringAttribute{Optional: true},
		}},
		"webhook_url": schema.StringAttribute{Optional: true, Description: "Optional HTTP(S) notification destination."},
	})
}

func (r *bucketConfigurationResource) endpoint(v networkValues) string {
	return bucketPath(v.str("bucket_name")) + "/" + r.section
}

func (r *bucketConfigurationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if err := validateBucketConfigurationName(req.ID); err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	v := networkValues{"id": types.StringValue(req.ID), "bucket_name": types.StringValue(req.ID)}
	v[r.collection], _ = networkValue(r.attributes[r.collection].GetType(), nil)
	resp.Diagnostics.Append(r.set(ctx, &resp.State, v)...)
}

func (r *bucketConfigurationResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var object types.Object
	resp.Diagnostics.Append(req.Config.Get(ctx, &object)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v := networkObject(object)
	if s := v["bucket_name"]; s != nil && !s.IsNull() && !s.IsUnknown() {
		if err := validateBucketConfigurationName(v.str("bucket_name")); err != nil {
			resp.Diagnostics.AddError("Invalid bucket name", err.Error())
		}
	}
	if bucketConfigurationUnknown(v[r.collection]) {
		return
	}
	_, err := r.payload(v)
	if err != nil {
		resp.Diagnostics.AddError("Invalid bucket configuration", err.Error())
	}
}

func validateBucketConfigurationName(name string) error {
	if len(name) < 3 || len(name) > 63 || strings.ContainsAny(name, "/\\") || strings.TrimSpace(name) != name {
		return fmt.Errorf("use a bucket name between 3 and 63 characters without path separators or surrounding whitespace")
	}
	return nil
}

func bucketConfigurationUnknown(v attr.Value) bool {
	if v == nil || v.IsUnknown() {
		return true
	}
	if v.IsNull() {
		return false
	}
	switch x := v.(type) {
	case types.List:
		for _, item := range x.Elements() {
			if bucketConfigurationUnknown(item) {
				return true
			}
		}
	case types.Object:
		for k, item := range x.Attributes() {
			if k != "s3_events" && bucketConfigurationUnknown(item) {
				return true
			}
		}
	}
	return false
}

var bucketCORSAliases = map[string]string{"id": "ID", "allowed_origins": "AllowedOrigins", "allowed_methods": "AllowedMethods", "allowed_headers": "AllowedHeaders", "expose_headers": "ExposeHeaders", "max_age_seconds": "MaxAgeSeconds"}

func (r *bucketConfigurationResource) payload(v networkValues) (map[string]any, error) {
	list, ok := v[r.collection].(types.List)
	if !ok || list.IsUnknown() || list.IsNull() || len(list.Elements()) == 0 {
		return nil, fmt.Errorf("%s must contain at least one entry", r.collection)
	}
	items := []any{}
	for _, item := range list.Elements() {
		object, ok := item.(types.Object)
		if !ok || object.IsUnknown() || object.IsNull() {
			return nil, fmt.Errorf("%s contains an unknown or null entry", r.collection)
		}
		values := object.Attributes()
		// s3_events is output-only and can be unknown during planning.
		delete(values, "s3_events")
		entry := map[string]any{}
		for k, value := range values {
			if value.IsNull() {
				continue
			}
			raw, err := networkJSON(value)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			entry[k] = raw
		}
		if err := r.validateEntry(entry); err != nil {
			return nil, err
		}
		if r.section == "lifecycle" && entry["expiration_date"] != nil {
			timestamp := entry["expiration_date"].(string)
			parsed, _ := time.Parse(time.RFC3339Nano, timestamp)
			if parsed.UTC().Format(time.RFC3339Nano) != timestamp {
				return nil, fmt.Errorf("expiration_date must use canonical UTC RFC3339 format ending in Z, without redundant fractional zeros")
			}
		}
		if r.section == "cors" {
			aliased := map[string]any{}
			for k, value := range entry {
				aliased[bucketCORSAliases[k]] = value
			}
			entry = aliased
		}
		items = append(items, entry)
	}
	return map[string]any{r.collection: items}, nil
}

func bucketConfigStrings(raw any, name string) ([]string, error) {
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return nil, fmt.Errorf("%s must be a nonempty string list", name)
	}
	out := make([]string, len(list))
	for i, item := range list {
		s, ok := item.(string)
		if !ok || s == "" {
			return nil, fmt.Errorf("%s contains an empty or invalid string", name)
		}
		out[i] = s
	}
	return out, nil
}

func bucketConfigurationHTTPURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func (r *bucketConfigurationResource) validateEntry(entry map[string]any) error {
	switch r.section {
	case "cors":
		origins, err := bucketConfigStrings(entry["allowed_origins"], "allowed_origins")
		if err != nil {
			return err
		}
		for _, origin := range origins {
			if origin != "*" && !bucketConfigurationHTTPURL(origin) {
				return fmt.Errorf("allowed_origins must contain HTTP(S) origins or *")
			}
		}
		methods, err := bucketConfigStrings(entry["allowed_methods"], "allowed_methods")
		if err != nil {
			return err
		}
		for _, method := range methods {
			if !containsNetwork([]string{"GET", "PUT", "POST", "DELETE", "HEAD"}, method) {
				return fmt.Errorf("unsupported CORS method %q", method)
			}
		}
		if age := entry["max_age_seconds"]; age != nil {
			n, ok := bucketConfigurationInteger(age)
			if !ok || n < 0 {
				return fmt.Errorf("max_age_seconds must be a nonnegative integer")
			}
		}
	case "lifecycle":
		if entry["status"] != "Enabled" && entry["status"] != "Disabled" {
			return fmt.Errorf("lifecycle status must be Enabled or Disabled")
		}
		days, date := entry["expiration_days"], entry["expiration_date"]
		if (days == nil) == (date == nil) {
			return fmt.Errorf("specify exactly one of expiration_days or expiration_date")
		}
		if days != nil {
			n, ok := bucketConfigurationInteger(days)
			if !ok || n < 1 {
				return fmt.Errorf("expiration_days must be a positive integer")
			}
		}
		if date != nil {
			s, ok := date.(string)
			if !ok {
				return fmt.Errorf("expiration_date must be an RFC3339 timestamp")
			}
			if _, err := time.Parse(time.RFC3339Nano, s); err != nil {
				return fmt.Errorf("expiration_date must be an RFC3339 timestamp")
			}
		}
	case "notifications":
		events, err := bucketConfigStrings(entry["events"], "events")
		if err != nil {
			return err
		}
		for _, event := range events {
			if event != "upload" && event != "delete" {
				return fmt.Errorf("event category must be upload or delete")
			}
		}
		if webhook := entry["webhook_url"]; webhook != nil {
			s, ok := webhook.(string)
			if !ok || !bucketConfigurationHTTPURL(s) {
				return fmt.Errorf("webhook_url must be an HTTP(S) URL")
			}
		}
	}
	return nil
}

func bucketConfigurationInteger(raw any) (int64, bool) {
	switch n := raw.(type) {
	case int64:
		return n, true
	case float64:
		return int64(n), float64(int64(n)) == n
	default:
		return 0, false
	}
}

func (r *bucketConfigurationResource) fetchConfiguration(ctx context.Context, v networkValues) ([]any, error) {
	var out map[string]any
	if err := r.client.do(ctx, http.MethodGet, r.endpoint(v), nil, &out); err != nil {
		return nil, err
	}
	items, ok := out[r.collection].([]any)
	if !ok {
		return nil, fmt.Errorf("response omitted a non-null %s array; state was preserved", r.collection)
	}
	for i, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid %s entry %d", r.collection, i)
		}
		if r.section == "cors" {
			normalized := map[string]any{}
			for name, alias := range bucketCORSAliases {
				value, exists := entry[alias]
				if !exists {
					value = entry[name]
				}
				normalized[name] = value
			}
			entry, items[i] = normalized, normalized
		}
		if err := r.validateEntry(entry); err != nil {
			return nil, fmt.Errorf("invalid %s response: %w", r.section, err)
		}
		if r.section == "notifications" {
			if _, err := bucketConfigStrings(entry["s3_events"], "s3_events"); err != nil {
				return nil, err
			}
		}
		// Pydantic emits +00:00. Canonical UTC spelling also makes imports
		// converge without needing a write solely to change timestamp spelling.
		if r.section == "lifecycle" {
			if actual, ok := entry["expiration_date"].(string); ok {
				parsed, _ := time.Parse(time.RFC3339Nano, actual)
				entry["expiration_date"] = parsed.UTC().Format(time.RFC3339Nano)
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

func (r *bucketConfigurationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var object types.Object
	resp.Diagnostics.Append(req.Plan.Get(ctx, &object)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v := networkObject(object)
	if err := validateBucketConfigurationName(v.str("bucket_name")); err != nil {
		resp.Diagnostics.AddError("Invalid bucket name", err.Error())
		return
	}
	body, err := r.payload(v)
	if err != nil {
		resp.Diagnostics.AddError("Invalid bucket configuration", err.Error())
		return
	}
	existing, err := r.fetchConfiguration(ctx, v)
	if err != nil {
		resp.Diagnostics.AddError("Failed to inspect existing bucket configuration", err.Error())
		return
	}
	if len(existing) > 0 {
		resp.Diagnostics.AddError("Bucket configuration already exists", "Import this resource using the bucket name before managing the existing "+r.section+" section. Creation will not overwrite it.")
		return
	}
	if err = r.client.do(ctx, http.MethodPut, r.endpoint(v), body, nil); err != nil {
		resp.Diagnostics.AddError("Failed to set bucket configuration", err.Error())
		return
	}
	// Save stable identity even when read-after-write fails. Output-only nested
	// values are resolved to null until a successful read supplies them.
	if r.section == "notifications" {
		if err = r.setConfiguration(v, body[r.collection].([]any)); err != nil {
			resp.Diagnostics.AddError("Failed to save configuration identity", err.Error())
			return
		}
	}
	v["id"] = v["bucket_name"]
	resp.Diagnostics.Append(r.set(ctx, &resp.State, v)...)
	items, err := r.fetchConfiguration(ctx, v)
	if err == nil && len(items) == 0 {
		err = fmt.Errorf("API returned an empty configuration after a successful write")
	}
	if err == nil {
		err = r.setConfiguration(v, items)
	}
	if err != nil {
		resp.Diagnostics.AddError("Bucket configuration written but refresh failed", err.Error())
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
		resp.Diagnostics.AddError("Failed to read bucket configuration", err.Error())
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
	body, err := r.payload(v)
	if err != nil {
		resp.Diagnostics.AddError("Invalid bucket configuration", err.Error())
		return
	}
	if err = r.client.do(ctx, http.MethodPut, r.endpoint(v), body, nil); err != nil {
		resp.Diagnostics.AddError("Failed to update bucket configuration", err.Error())
		return
	}
	items, err := r.fetchConfiguration(ctx, v)
	if err == nil && len(items) == 0 {
		err = fmt.Errorf("API returned an empty configuration after a successful write")
	}
	if err == nil {
		err = r.setConfiguration(v, items)
	}
	if err != nil {
		resp.Diagnostics.AddError("Bucket configuration updated but refresh failed", err.Error())
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
	v := networkObject(object)
	if err := r.client.do(ctx, http.MethodDelete, r.endpoint(v), nil, nil); err != nil && !IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to delete bucket configuration", err.Error())
		return
	}
	items, err := r.fetchConfiguration(ctx, v)
	if IsNotFound(err) {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to verify configuration deletion", err.Error())
		return
	}
	if len(items) != 0 {
		resp.Diagnostics.AddError("Bucket configuration remains", "The API still returned entries after deletion. State was preserved for retry.")
	}
}
