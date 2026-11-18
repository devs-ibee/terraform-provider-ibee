package provider

import (
	"context"
	"net/http"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// ---------------------------------------------------------------- ibee_bucket_object_lock

type bucketObjectLockResource struct{ client *Client }

func NewBucketObjectLockResource() resource.Resource { return &bucketObjectLockResource{} }

type bucketObjectLockModel struct {
	ID                types.String `tfsdk:"id"`
	BucketName        types.String `tfsdk:"bucket_name"`
	Mode              types.String `tfsdk:"mode"`
	Days              types.Int64  `tfsdk:"days"`
	Years             types.Int64  `tfsdk:"years"`
	ObjectLockEnabled types.String `tfsdk:"object_lock_enabled"`
}

type bucketObjectLockAPI struct {
	ObjectLockEnabled string `json:"object_lock_enabled"`
	Rule              *struct {
		Mode  string `json:"mode"`
		Days  *int64 `json:"days"`
		Years *int64 `json:"years"`
	} `json:"rule"`
}

func (r *bucketObjectLockResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_bucket_object_lock"
}

func (r *bucketObjectLockResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The default retention of a bucket created with object lock: new objects can't be deleted or overwritten until it ends. Create and update set it (PUT); destroy removes the default retention (objects already locked stay locked). The service decides what a bucket allows. Import with the bucket name.",
		Attributes: map[string]schema.Attribute{
			"id":                  schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, Description: "The bucket name."},
			"bucket_name":         schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, Description: "A bucket created with object lock."},
			"mode":                schema.StringAttribute{Required: true, Description: "GOVERNANCE (users with the bypass permission can shorten it) or COMPLIANCE (no one can)."},
			"days":                schema.Int64Attribute{Optional: true, Description: "Retention in days, 1 to 36500. Give days or years."},
			"years":               schema.Int64Attribute{Optional: true, Description: "Retention in years, 1 to 100. Give days or years."},
			"object_lock_enabled": schema.StringAttribute{Computed: true, Description: "Enabled when the bucket was created with object lock."},
		},
	}
}

func (r *bucketObjectLockResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResourceClient(req, resp)
}

func (r *bucketObjectLockResource) put(ctx context.Context, bucket string, rule map[string]any) error {
	var out map[string]any
	return r.client.do(ctx, http.MethodPut, bucketPath(bucket)+"/object-lock", map[string]any{"rule": rule}, &out)
}

func (r *bucketObjectLockResource) read(ctx context.Context, state *bucketObjectLockModel) (bool, error) {
	var out bucketObjectLockAPI
	if err := r.client.do(ctx, http.MethodGet, bucketPath(state.BucketName.ValueString())+"/object-lock", nil, &out); err != nil {
		if IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if out.Rule == nil {
		return false, nil
	}
	state.ID = types.StringValue(state.BucketName.ValueString())
	state.Mode = types.StringValue(out.Rule.Mode)
	state.Days = types.Int64PointerValue(out.Rule.Days)
	state.Years = types.Int64PointerValue(out.Rule.Years)
	state.ObjectLockEnabled = types.StringValue(out.ObjectLockEnabled)
	return true, nil
}

func (m bucketObjectLockModel) rule() map[string]any {
	rule := map[string]any{"mode": m.Mode.ValueString()}
	if !m.Days.IsNull() && !m.Days.IsUnknown() {
		rule["days"] = m.Days.ValueInt64()
	}
	if !m.Years.IsNull() && !m.Years.IsUnknown() {
		rule["years"] = m.Years.ValueInt64()
	}
	return rule
}

func (r *bucketObjectLockResource) apply(ctx context.Context, plan *bucketObjectLockModel, diags interface{ AddError(string, string) }) bool {
	if err := r.put(ctx, plan.BucketName.ValueString(), plan.rule()); err != nil {
		diags.AddError("Failed to set the bucket's default retention", err.Error())
		return false
	}
	if found, err := r.read(ctx, plan); err != nil || !found {
		diags.AddError("Failed to read the bucket's default retention", "The API reports no default retention after it was set.")
		return false
	}
	return true
}

func (r *bucketObjectLockResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan bucketObjectLockModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || !r.apply(ctx, &plan, &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *bucketObjectLockResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state bucketObjectLockModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	found, err := r.read(ctx, &state)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read the bucket's default retention", err.Error())
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *bucketObjectLockResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan bucketObjectLockModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || !r.apply(ctx, &plan, &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *bucketObjectLockResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state bucketObjectLockModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.put(ctx, state.BucketName.ValueString(), nil); err != nil && !IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to remove the bucket's default retention", err.Error())
	}
}

func (r *bucketObjectLockResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("bucket_name"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// ---------------------------------------------------------------- ibee_bucket_custom_domain

type bucketCustomDomainResource struct{ client *Client }

func NewBucketCustomDomainResource() resource.Resource { return &bucketCustomDomainResource{} }

type bucketCustomDomainModel struct {
	ID          types.String `tfsdk:"id"`
	BucketName  types.String `tfsdk:"bucket_name"`
	Domain      types.String `tfsdk:"domain"`
	CnameTarget types.String `tfsdk:"cname_target"`
	Status      types.String `tfsdk:"status"`
}

type bucketCustomDomainAPI struct {
	Domain      string `json:"domain"`
	CnameTarget string `json:"cname_target"`
	Status      string `json:"status"`
}

func (r *bucketCustomDomainResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_bucket_custom_domain"
}

func (r *bucketCustomDomainResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "A custom hostname that serves a bucket's public files. Create the CNAME record shown in cname_target at your DNS provider; the service checks it and issues TLS (status). Destroy removes the hostname from the bucket. Import with BUCKET_NAME/DOMAIN.",
		Attributes: map[string]schema.Attribute{
			"id":           schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, Description: "BUCKET_NAME/DOMAIN."},
			"bucket_name":  schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Bucket name."},
			"domain":       schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Hostname, such as assets.example.com."},
			"cname_target": schema.StringAttribute{Computed: true, Description: "Hostname the domain's CNAME record must point at."},
			"status":       schema.StringAttribute{Computed: true, Description: "DNS check and TLS state the service reports."},
		},
	}
}

func (r *bucketCustomDomainResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResourceClient(req, resp)
}

func (r *bucketCustomDomainResource) read(ctx context.Context, state *bucketCustomDomainModel) (bool, error) {
	var out struct {
		CnameTarget   string                  `json:"cname_target"`
		CustomDomains []bucketCustomDomainAPI `json:"custom_domains"`
	}
	if err := r.client.do(ctx, http.MethodGet, bucketPath(state.BucketName.ValueString())+"/custom-domains", nil, &out); err != nil {
		if IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	for _, domain := range out.CustomDomains {
		if domain.Domain != state.Domain.ValueString() {
			continue
		}
		target := domain.CnameTarget
		if target == "" {
			target = out.CnameTarget
		}
		state.ID = types.StringValue(state.BucketName.ValueString() + "/" + domain.Domain)
		state.CnameTarget = types.StringValue(target)
		state.Status = types.StringValue(domain.Status)
		return true, nil
	}
	return false, nil
}

func (r *bucketCustomDomainResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan bucketCustomDomainModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out bucketCustomDomainAPI
	if err := r.client.do(ctx, http.MethodPost, bucketPath(plan.BucketName.ValueString())+"/custom-domains", map[string]any{"domain": plan.Domain.ValueString()}, &out); err != nil {
		resp.Diagnostics.AddError("Failed to add the bucket custom domain", err.Error())
		return
	}
	plan.ID = types.StringValue(plan.BucketName.ValueString() + "/" + plan.Domain.ValueString())
	plan.CnameTarget = types.StringValue(out.CnameTarget)
	plan.Status = types.StringValue(out.Status)
	if found, err := r.read(ctx, &plan); err != nil {
		resp.Diagnostics.AddError("Failed to read the bucket custom domain", err.Error())
		return
	} else if !found && plan.CnameTarget.ValueString() == "" {
		resp.Diagnostics.AddError("Bucket custom domain not found after create", "The API does not list the domain it accepted.")
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *bucketCustomDomainResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state bucketCustomDomainModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	found, err := r.read(ctx, &state)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read the bucket custom domain", err.Error())
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *bucketCustomDomainResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Custom domain changes require replacement", "The bucket and domain identify the custom domain.")
}

func (r *bucketCustomDomainResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state bucketCustomDomainModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out map[string]any
	route := bucketPath(state.BucketName.ValueString()) + "/custom-domains/" + url.PathEscape(state.Domain.ValueString())
	if err := r.client.do(ctx, http.MethodDelete, route, nil, &out); err != nil && !IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to remove the bucket custom domain", err.Error())
	}
}

func (r *bucketCustomDomainResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	bucket, domain, ok := splitImportID(req.ID, "BUCKET_NAME/DOMAIN", resp)
	if !ok {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("bucket_name"), bucket)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("domain"), domain)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
