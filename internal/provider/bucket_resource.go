package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                   = (*bucketResource)(nil)
	_ resource.ResourceWithConfigure      = (*bucketResource)(nil)
	_ resource.ResourceWithImportState    = (*bucketResource)(nil)
	_ resource.ResourceWithValidateConfig = (*bucketResource)(nil)
)

type bucketResource struct{ client *Client }

func NewBucketResource() resource.Resource { return &bucketResource{} }

type bucketResourceModel struct {
	ID                types.String `tfsdk:"id"`
	Name              types.String `tfsdk:"name"`
	Region            types.String `tfsdk:"region"`
	IsPublic          types.Bool   `tfsdk:"is_public"`
	ObjectLockEnabled types.Bool   `tfsdk:"object_lock_enabled"`
	ForceDestroy      types.Bool   `tfsdk:"force_destroy"`
	Status            types.String `tfsdk:"status"`
	Plan              types.String `tfsdk:"plan"`
	SiteID            types.String `tfsdk:"site_id"`
	ObjectCount       types.Int64  `tfsdk:"object_count"`
	TotalSize         types.Int64  `tfsdk:"total_size"`
}

// Pointer fields distinguish missing usage counters from a verified empty bucket.
type bucketAPI struct {
	Name              string  `json:"name"`
	Region            *string `json:"region"`
	IsPublic          *bool   `json:"is_public"`
	ObjectLockEnabled *bool   `json:"bucket_lock_enabled"`
	Status            *string `json:"status"`
	Plan              *string `json:"plan"`
	SiteID            *string `json:"site_id"`
	ObjectCount       *int64  `json:"object_count"`
	TotalSize         *int64  `json:"total_size"`
}

func (r *bucketResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_bucket"
}

func (r *bucketResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "An IBEE object storage bucket. By default the provider refuses deletion unless both reported usage counters are zero. This preflight is not atomic; stop writers before destroy. The provider never empties a bucket itself, and the service may reject deletion when objects or retained versions remain. Imported buckets use force_destroy=false.",
		Attributes: map[string]schema.Attribute{
			"id":                  schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"name":                schema.StringAttribute{Required: true, Description: "Bucket name, 3–63 characters. Import using this name.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"region":              schema.StringAttribute{Required: true, Description: "Object Storage region identifier, not a compute site ID.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"is_public":           schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "Allow unauthenticated read access."},
			"object_lock_enabled": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()}, Description: "Enable object lock at creation. Manage a default retention rule separately with ibee_bucket_retention; the service cannot clear that rule after creation."},
			"force_destroy":       schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "Skip the provider's usage-counter deletion guard and permit the bucket delete API call. This does not purge objects or bypass server emptiness/retention checks. Apply before destroy. On API versions that delete recursively, this permits content deletion."},
			"status":              schema.StringAttribute{Computed: true},
			"plan":                schema.StringAttribute{Computed: true, Description: "Storage plan returned by the API."},
			"site_id":             schema.StringAttribute{Computed: true},
			"object_count":        schema.Int64Attribute{Computed: true},
			"total_size":          schema.Int64Attribute{Computed: true, Description: "Reported storage usage in bytes."},
		},
	}
}

func (r *bucketResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	var ok bool
	r.client, ok = req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *Client, got %T", req.ProviderData))
	}
}

func (r *bucketResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg bucketResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !cfg.Name.IsUnknown() && !cfg.Name.IsNull() && (len(cfg.Name.ValueString()) < 3 || len(cfg.Name.ValueString()) > 63 || strings.ContainsAny(cfg.Name.ValueString(), "/\\")) {
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Invalid bucket name", "Use a bucket name between 3 and 63 characters without path separators.")
	}
	if !cfg.Region.IsUnknown() && !cfg.Region.IsNull() && strings.TrimSpace(cfg.Region.ValueString()) == "" {
		resp.Diagnostics.AddAttributeError(path.Root("region"), "Missing storage region", "Specify the Object Storage region configured for the target environment.")
	}
}

func bucketPath(name string) string { return "/object-storage/buckets/" + url.PathEscape(name) }

func (out bucketAPI) apply(state *bucketResourceModel) error {
	if out.Name == "" || out.Region == nil || *out.Region == "" || out.IsPublic == nil || out.ObjectLockEnabled == nil {
		return fmt.Errorf("bucket response is missing name, region, is_public or bucket_lock_enabled; state was preserved")
	}
	if !state.ID.IsNull() && !state.ID.IsUnknown() && state.ID.ValueString() != out.Name {
		return fmt.Errorf("bucket response identifies a different bucket; state was preserved")
	}
	state.ID = types.StringValue(out.Name)
	state.Name = types.StringValue(out.Name)
	state.Region = types.StringPointerValue(out.Region)
	state.IsPublic = types.BoolPointerValue(out.IsPublic)
	state.ObjectLockEnabled = types.BoolPointerValue(out.ObjectLockEnabled)
	state.Status = types.StringPointerValue(out.Status)
	state.Plan = types.StringPointerValue(out.Plan)
	state.SiteID = types.StringPointerValue(out.SiteID)
	state.ObjectCount = types.Int64PointerValue(out.ObjectCount)
	state.TotalSize = types.Int64PointerValue(out.TotalSize)
	return nil
}

func (r *bucketResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan bucketResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// The published contract has no storage catalog/quote. This is account
	// admission only; the product service must enforce actual usage pricing.
	if err := r.client.requireBillingEligibility(ctx, "", nil); err != nil {
		resp.Diagnostics.AddError("Bucket billing eligibility failed", err.Error())
		return
	}
	body := map[string]any{"name": plan.Name.ValueString(), "region": plan.Region.ValueString(), "is_public": plan.IsPublic.ValueBool(), "object_lock_enabled": plan.ObjectLockEnabled.ValueBool()}
	var out bucketAPI
	if err := r.client.do(ctx, http.MethodPost, "/object-storage/buckets", body, &out); err != nil {
		resp.Diagnostics.AddError("Failed to create bucket", err.Error())
		return
	}
	// Name is the stable public identity. Keep it even if a successful create
	// returned incomplete metadata so the resource can be refreshed or removed.
	plan.ID = plan.Name
	plan.Status, plan.Plan, plan.SiteID = types.StringNull(), types.StringNull(), types.StringNull()
	plan.ObjectCount, plan.TotalSize = types.Int64Null(), types.Int64Null()
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if err := out.apply(&plan); err != nil {
		resp.Diagnostics.AddError("Invalid created bucket response", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if err := r.client.do(ctx, http.MethodGet, bucketPath(plan.ID.ValueString()), nil, &out); err != nil {
		resp.Diagnostics.AddError("Bucket created but refresh failed", err.Error())
		return
	}
	if err := out.apply(&plan); err != nil {
		resp.Diagnostics.AddError("Invalid bucket response", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *bucketResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state bucketResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out bucketAPI
	if err := r.client.do(ctx, http.MethodGet, bucketPath(state.ID.ValueString()), nil, &out); err != nil {
		if IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read bucket", err.Error())
		return
	}
	if err := out.apply(&state); err != nil {
		resp.Diagnostics.AddError("Invalid bucket response", err.Error())
		return
	}
	if state.ForceDestroy.IsNull() {
		state.ForceDestroy = types.BoolValue(false)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *bucketResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state bucketResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out bucketAPI
	if !plan.IsPublic.Equal(state.IsPublic) {
		if err := r.client.do(ctx, http.MethodPatch, bucketPath(state.ID.ValueString()), map[string]any{"is_public": plan.IsPublic.ValueBool()}, &out); err != nil {
			resp.Diagnostics.AddError("Failed to update bucket", err.Error())
			return
		}
	} else if err := r.client.do(ctx, http.MethodGet, bucketPath(state.ID.ValueString()), nil, &out); err != nil {
		resp.Diagnostics.AddError("Failed to refresh bucket", err.Error())
		return
	}
	if err := out.apply(&plan); err != nil {
		resp.Diagnostics.AddError("Invalid bucket response", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *bucketResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state bucketResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !state.ForceDestroy.ValueBool() {
		var out bucketAPI
		if err := r.client.do(ctx, http.MethodGet, bucketPath(state.ID.ValueString()), nil, &out); err != nil {
			if IsNotFound(err) {
				return
			}
			resp.Diagnostics.AddError("Cannot verify bucket is empty", err.Error())
			return
		}
		if err := out.apply(&state); err != nil {
			resp.Diagnostics.AddError("Cannot verify bucket is empty", err.Error())
			return
		}
		if out.ObjectCount == nil || out.TotalSize == nil || *out.ObjectCount != 0 || *out.TotalSize != 0 {
			resp.Diagnostics.AddError("Bucket deletion blocked", "Bucket is nonempty or usage is unavailable. Empty it and stop writers before retrying. If counters are stale after emptying, explicitly apply force_destroy=true to skip this local check; the API may still reject a nonempty or retained bucket.")
			return
		}
	}
	if err := r.client.do(ctx, http.MethodDelete, bucketPath(state.ID.ValueString()), nil, nil); err != nil && !IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to delete bucket", err.Error()+" The provider does not purge objects, historical versions or retained data; empty the bucket through the supported storage workflow before retrying.")
	}
}

func (r *bucketResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if strings.TrimSpace(req.ID) == "" || strings.ContainsAny(req.ID, "/\\") {
		resp.Diagnostics.AddError("Invalid bucket import ID", "Import with the bucket name.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("force_destroy"), false)...)
}
