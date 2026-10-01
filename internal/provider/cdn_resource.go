package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// CDN uses the public gateway, with the same canonical tenant and billing client
// as the other product resources. No portal session credentials are involved.
type cdnDistributionResource struct{ *networkResource }

func NewCDNDistributionResource() resource.Resource {
	base := "/cdn/distributions"
	r := &networkResource{name: "cdn_distribution", description: "An IBEE CDN distribution backed by a bucket or custom origin. Origin changes replace the distribution. Deletion disables delivery and soft-deletes the distribution; it never deletes the origin. Custom domains and website settings have separate ownership." + networkBillingDescription,
		attributes: map[string]schema.Attribute{
			"id": networkIDAttribute(), "name": networkRequired(false), "origin_type": networkOptionalString("bucket", true), "origin_id": networkRequired(true), "cache_policy": networkOptionalString("static-assets", false),
			"enabled":             schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), Description: "Whether delivery is enabled. The API creates enabled distributions; false is applied in a subsequent PATCH, so creation can briefly enable delivery."},
			"canonical_origin_id": schema.StringAttribute{Computed: true, Description: "Canonical origin ID. For bucket origins, origin_id may be the bucket name; the API resolves it to this ID."},
			"status":              schema.StringAttribute{Computed: true}, "default_domain": schema.StringAttribute{Computed: true}, "default_url": schema.StringAttribute{Computed: true},
		},
		requestFields: networkIdentityFields("name", "origin_type", "origin_id", "cache_policy", "enabled"), responseFields: networkIdentityFields("name", "origin_type", "origin_id", "cache_policy", "enabled", "status", "default_domain", "default_url"),
		createOnlyFields: map[string]bool{"origin_type": true, "origin_id": true},
		createPath:       networkPath(base), readPath: func(v networkValues) string { return base + "/" + v.segment("id") }, updatePath: func(v networkValues) string { return base + "/" + v.segment("id") }, deletePath: func(v networkValues) string { return base + "/" + v.segment("id") },
		idField: "id", importFields: []string{"id"}, waitDelete: true,
	}
	r.responseFields["canonical_origin_id"] = "canonical_origin_id"
	r.validate = func(v networkValues) error {
		if strings.TrimSpace(v.str("name")) != v.str("name") || len(v.str("name")) < 1 || len(v.str("name")) > 128 {
			return fmt.Errorf("name must be 1–128 characters without surrounding whitespace")
		}
		if v.str("origin_type") != "bucket" && v.str("origin_type") != "custom" {
			return fmt.Errorf("origin_type must be bucket or custom")
		}
		if !containsNetwork([]string{"static-assets", "media", "short", "no-cache"}, v.str("cache_policy")) {
			return fmt.Errorf("unsupported cache_policy")
		}
		return nil
	}
	r.readTransform = func(v networkValues, out map[string]any) (map[string]any, error) {
		if out["status"] == "deleted" {
			return nil, &apiError{Status: 404, Body: "distribution deleted"}
		}
		for _, key := range []string{"name", "origin_type", "origin_id", "cache_policy", "enabled", "status", "default_domain", "default_url"} {
			if out[key] == nil {
				return nil, fmt.Errorf("CDN response omitted %s", key)
			}
		}
		out["canonical_origin_id"] = out["origin_id"]
		// Accept the API's explicit name-to-ID mapping without inventing it. An
		// imported distribution has no configured alias and retains canonical ID.
		if out["origin_type"] == "bucket" && v.str("origin_id") != "" && out["bucket_name"] == v.str("origin_id") {
			out["origin_id"] = v.str("origin_id")
		}
		return out, nil
	}
	return &cdnDistributionResource{r}
}

func (r *cdnDistributionResource) settle(ctx context.Context, v networkValues) error {
	ctx, cancel := context.WithTimeout(ctx, r.client.computeTimeout())
	defer cancel()
	for {
		if err := r.refresh(ctx, v, false); err != nil {
			return err
		}
		switch v.str("status") {
		case "active", "disabled":
			return nil
		case "deploying":
			if err := r.client.computePoll(ctx); err != nil {
				return err
			}
		default:
			return fmt.Errorf("CDN deployment entered unexpected status %q", v.str("status"))
		}
	}
}
func (r *cdnDistributionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var planned types.Object
	resp.Diagnostics.Append(req.Plan.Get(ctx, &planned)...)
	if resp.Diagnostics.HasError() {
		return
	}
	wantEnabled := networkObject(planned)["enabled"].(types.Bool).ValueBool()
	// enabled is update-only in the native API. Preserve identity before toggling.
	clone := *r.networkResource
	clone.requestFields = networkIdentityFields("name", "origin_type", "origin_id", "cache_policy")
	clone.Create(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		return
	}
	var state types.Object
	resp.Diagnostics.Append(resp.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v := networkObject(state)
	if !wantEnabled {
		if err := r.client.do(ctx, http.MethodPatch, r.updatePath(v), map[string]any{"enabled": false}, nil); err != nil {
			resp.Diagnostics.AddError("CDN created but disable failed", err.Error())
			return
		}
	}
	if err := r.settle(ctx, v); err != nil {
		resp.Diagnostics.Append(r.set(ctx, &resp.State, v)...)
		resp.Diagnostics.AddError("CDN deployment failed", err.Error())
		return
	}
	resp.Diagnostics.Append(r.set(ctx, &resp.State, v)...)
}
func (r *cdnDistributionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	r.networkResource.Update(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		return
	}
	var state types.Object
	resp.Diagnostics.Append(resp.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v := networkObject(state)
	if err := r.settle(ctx, v); err != nil {
		resp.Diagnostics.AddError("CDN deployment failed", err.Error())
		return
	}
	resp.Diagnostics.Append(r.set(ctx, &resp.State, v)...)
}

func NewCDNOriginResource() resource.Resource {
	base := "/cdn/origins"
	r := &networkResource{name: "cdn_origin", description: "An HTTPS custom origin for IBEE CDN. Terraform manages origin configuration, not the remote origin server. Remove dependent distributions before deleting the origin.",
		attributes:    map[string]schema.Attribute{"id": networkIDAttribute(), "name": networkRequired(false), "origin_url": networkRequired(false), "default_url": schema.StringAttribute{Computed: true}},
		requestFields: networkIdentityFields("name", "origin_url"), responseFields: networkIdentityFields("name", "origin_url", "default_url"),
		createPath: networkPath(base), readPath: func(v networkValues) string { return base + "/" + v.segment("id") }, updatePath: func(v networkValues) string { return base + "/" + v.segment("id") }, deletePath: func(v networkValues) string { return base + "/" + v.segment("id") }, idField: "id", importFields: []string{"id"}, waitDelete: true,
	}
	r.validate = func(v networkValues) error {
		u, e := url.Parse(v.str("origin_url"))
		if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Port() != "" || strings.HasSuffix(v.str("origin_url"), "/") {
			return fmt.Errorf("origin_url must be an HTTPS URL without credentials, port, query, fragment or trailing slash")
		}
		if strings.TrimSpace(v.str("name")) != v.str("name") || len(v.str("name")) < 1 || len(v.str("name")) > 128 {
			return fmt.Errorf("name must be 1–128 characters without surrounding whitespace")
		}
		return nil
	}
	return r
}

func NewCDNWebsiteResource() resource.Resource {
	item := func(v networkValues) string {
		return "/cdn/distributions/" + v.segment("distribution_id") + "/website-config"
	}
	r := &networkResource{name: "cdn_website", description: "Static website/SPA configuration on an existing bucket-backed CDN distribution. The index object must already exist. Destroy disables website routing without deleting objects or the distribution. Import using the distribution ID.",
		attributes:    map[string]schema.Attribute{"id": networkIDAttribute(), "distribution_id": networkRequired(true), "index_document": networkOptionalString("index.html", false), "enabled": schema.BoolAttribute{Computed: true}},
		requestFields: networkIdentityFields("index_document"), responseFields: networkIdentityFields("distribution_id", "index_document", "enabled"),
		createPath: item, readPath: item, updatePath: item, deletePath: item, createMethod: http.MethodPut, updateMethod: http.MethodPut,
		idField: "distribution_id", readIdentity: "distribution_id", importFields: []string{"distribution_id"}, waitDelete: true,
	}
	r.validate = func(v networkValues) error {
		key := v.str("index_document")
		if len(key) == 0 || len(key) > 1024 || strings.Contains(key, "\\") {
			return fmt.Errorf("index_document must be a relative object key up to 1024 bytes")
		}
		for _, c := range key {
			if c < 32 || c > 126 {
				return fmt.Errorf("index_document must contain printable ASCII only")
			}
		}
		for _, s := range strings.Split(key, "/") {
			if s == "" || s == "." || s == ".." {
				return fmt.Errorf("index_document contains an invalid path segment")
			}
		}
		return nil
	}
	r.readTransform = func(_ networkValues, out map[string]any) (map[string]any, error) {
		enabled, ok := out["enabled"].(bool)
		if !ok {
			return nil, fmt.Errorf("website response omitted boolean enabled")
		}
		if !enabled {
			return nil, &apiError{Status: 404, Body: "website routing disabled"}
		}
		if out["index_document"] == nil {
			return nil, fmt.Errorf("website response omitted index_document")
		}
		return out, nil
	}
	return r
}

func NewCDNDomainResource() resource.Resource {
	base := func(v networkValues) string {
		return "/cdn/distributions/" + v.segment("distribution_id") + "/custom-domains"
	}
	item := func(v networkValues) string { return base(v) + "/" + v.segment("domain") }
	r := &networkResource{name: "cdn_domain", description: "A custom domain associated with a CDN distribution. DNS ownership validation and TLS issuance remain asynchronous; this resource reports their status without claiming the domain is ready. Set the required CNAME at your DNS provider. Import using DISTRIBUTION_ID/DOMAIN. Destroy removes the association, not DNS records.",
		attributes:    map[string]schema.Attribute{"id": networkIDAttribute(), "distribution_id": networkRequired(true), "domain": networkRequired(true), "status": schema.StringAttribute{Computed: true}, "cname_name": schema.StringAttribute{Computed: true}, "cname_target": schema.StringAttribute{Computed: true}},
		requestFields: networkIdentityFields("domain"), responseFields: networkIdentityFields("domain", "status", "cname_name", "cname_target"),
		createPath: base, readPath: item, deletePath: item, idField: "domain", readIdentity: "domain", importFields: []string{"distribution_id", "domain"}, waitDelete: true,
	}
	r.validate = func(v networkValues) error {
		d := v.str("domain")
		if len(d) > 253 || !strings.Contains(d, ".") || strings.ToLower(d) != d {
			return fmt.Errorf("domain must be a lowercase DNS hostname")
		}
		for _, label := range strings.Split(d, ".") {
			if len(label) == 0 || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
				return fmt.Errorf("invalid domain label")
			}
			for _, c := range label {
				if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
					return fmt.Errorf("invalid domain character")
				}
			}
		}
		return nil
	}
	r.readTransform = func(_ networkValues, out map[string]any) (map[string]any, error) {
		if out["status"] == nil {
			return nil, fmt.Errorf("custom domain response omitted status")
		}
		if validation, ok := out["validation"].(map[string]any); ok {
			if record, ok := validation["cname_record"].(map[string]any); ok {
				out["cname_name"], out["cname_target"] = record["name"], record["value"]
			}
		}
		return out, nil
	}
	return r
}
