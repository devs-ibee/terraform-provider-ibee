package provider

import (
	"context"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// CDN uses the public gateway, with the same canonical tenant and billing client
// as the other product resources. No portal session credentials are involved.
type cdnDistributionResource struct{ *networkResource }

func NewCDNDistributionResource() resource.Resource {
	base := "/cdn/distributions"
	r := &networkResource{name: "cdn_distribution", description: "An IBEE CDN distribution backed by a bucket or custom origin. Origin changes replace the distribution. Destroy deletes the distribution; it never deletes the origin." + networkBillingDescription,
		attributes: map[string]schema.Attribute{
			"id": networkIDAttribute(), "name": networkRequired(false), "origin_type": networkOptional(true), "origin_id": networkRequired(true), "cache_policy": networkOptional(false),
			"enabled":             schema.BoolAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}, Description: "Whether delivery is enabled. The create operation has no enabled field, so false is sent in a PATCH after create."},
			"canonical_origin_id": schema.StringAttribute{Computed: true, Description: "Canonical origin ID. For bucket origins, origin_id may be the bucket name; the API resolves it to this ID."},
			"status":              schema.StringAttribute{Computed: true}, "default_domain": schema.StringAttribute{Computed: true}, "default_url": schema.StringAttribute{Computed: true},
		},
		requestFields: networkIdentityFields("name", "origin_type", "origin_id", "cache_policy", "enabled"), responseFields: networkIdentityFields("name", "origin_type", "origin_id", "cache_policy", "enabled", "status", "default_domain", "default_url"),
		createOnlyFields: map[string]bool{"origin_type": true, "origin_id": true},
		createPath:       networkPath(base), readPath: func(v networkValues) string { return base + "/" + v.segment("id") }, updatePath: func(v networkValues) string { return base + "/" + v.segment("id") }, deletePath: func(v networkValues) string { return base + "/" + v.segment("id") },
		idField: "id", importFields: []string{"id"}, waitDelete: true,
	}
	r.responseFields["canonical_origin_id"] = "canonical_origin_id"
	r.readTransform = func(v networkValues, out map[string]any) (map[string]any, error) {
		if out["status"] == "deleted" {
			return nil, &apiError{Status: 404, Body: "distribution deleted"}
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
			return nil
		}
	}
}
func (r *cdnDistributionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var planned types.Object
	resp.Diagnostics.Append(req.Plan.Get(ctx, &planned)...)
	if resp.Diagnostics.HasError() {
		return
	}
	wantEnabled := networkObject(planned)["enabled"].(types.Bool)
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
	if known(wantEnabled) && !wantEnabled.ValueBool() {
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

func NewCDNWebsiteResource() resource.Resource {
	item := func(v networkValues) string {
		return "/cdn/distributions/" + v.segment("distribution_id") + "/website-config"
	}
	r := &networkResource{name: "cdn_website", description: "Static website configuration of a CDN distribution (PUT .../website-config). Destroy deletes the website configuration without deleting objects or the distribution. Import using the distribution ID.",
		attributes:    map[string]schema.Attribute{"id": networkIDAttribute(), "distribution_id": networkRequired(true), "index_document": networkOptional(false), "enabled": schema.BoolAttribute{Computed: true}},
		requestFields: networkIdentityFields("index_document"), responseFields: networkIdentityFields("distribution_id", "index_document", "enabled"),
		createPath: item, readPath: item, updatePath: item, deletePath: item, createMethod: http.MethodPut, updateMethod: http.MethodPut,
		idField: "distribution_id", readIdentity: "distribution_id", importFields: []string{"distribution_id"}, waitDelete: true,
	}
	r.readTransform = func(_ networkValues, out map[string]any) (map[string]any, error) {
		// Website routing that is off means this resource is gone.
		if enabled, ok := out["enabled"].(bool); ok && !enabled {
			return nil, &apiError{Status: 404, Body: "website routing disabled"}
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
	r := &networkResource{name: "cdn_domain", description: "A custom domain of a CDN distribution. The API validates DNS and issues TLS asynchronously; status reports where it is. Set the CNAME the API returns at your DNS provider. Import using DISTRIBUTION_ID/DOMAIN. Destroy removes the domain from the distribution, not DNS records.",
		attributes:    map[string]schema.Attribute{"id": networkIDAttribute(), "distribution_id": networkRequired(true), "domain": networkRequired(true), "status": schema.StringAttribute{Computed: true}, "cname_name": schema.StringAttribute{Computed: true}, "cname_target": schema.StringAttribute{Computed: true}},
		requestFields: networkIdentityFields("domain"), responseFields: networkIdentityFields("domain", "status", "cname_name", "cname_target"),
		createPath: base, readPath: item, deletePath: item, idField: "domain", readIdentity: "domain", importFields: []string{"distribution_id", "domain"}, waitDelete: true,
	}
	r.readTransform = func(_ networkValues, out map[string]any) (map[string]any, error) {
		if validation, ok := out["validation"].(map[string]any); ok {
			if record, ok := validation["cname_record"].(map[string]any); ok {
				out["cname_name"], out["cname_target"] = record["name"], record["value"]
			}
		}
		return out, nil
	}
	return r
}
