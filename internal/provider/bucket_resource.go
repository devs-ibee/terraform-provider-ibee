package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = (*bucketResource)(nil)
	_ resource.ResourceWithConfigure   = (*bucketResource)(nil)
	_ resource.ResourceWithImportState = (*bucketResource)(nil)
)

type bucketResource struct{ client *Client }

func NewBucketResource() resource.Resource { return &bucketResource{} }

type bucketResourceModel struct {
	ID                types.String `tfsdk:"id"`
	Name              types.String `tfsdk:"name"`
	Region            types.String `tfsdk:"region"`
	IsPublic          types.Bool   `tfsdk:"is_public"`
	ObjectLockEnabled types.Bool   `tfsdk:"object_lock_enabled"`
	DefaultRetention  types.Object `tfsdk:"default_retention"`
	Status            types.String `tfsdk:"status"`
	Plan              types.String `tfsdk:"plan"`
	SiteID            types.String `tfsdk:"site_id"`
	ObjectCount       types.Int64  `tfsdk:"object_count"`
	TotalSize         types.Int64  `tfsdk:"total_size"`
}

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

// bucketRetention is the contract's DefaultRetention.
type bucketRetention struct {
	Mode  string `json:"mode"`
	Days  *int64 `json:"days,omitempty"`
	Years *int64 `json:"years,omitempty"`
}

var bucketRetentionTypes = map[string]attr.Type{"mode": types.StringType, "days": types.Int64Type, "years": types.Int64Type}

func (r *bucketResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_bucket"
}

func (r *bucketResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "An IBEE object storage bucket. Destroy deletes the bucket (DELETE /object-storage/buckets/{name}); the API decides whether it can be deleted, for example it refuses a bucket with object lock. Manage lifecycle, CORS and notification rules with ibee_bucket_lifecycle, ibee_bucket_cors and ibee_bucket_notifications. Import using the bucket name.",
		Attributes: map[string]schema.Attribute{
			"id":                  schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"name":                schema.StringAttribute{Required: true, Description: "Bucket name.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"region":              schema.StringAttribute{Required: true, Description: "Object Storage region identifier, such as in-south-2.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"is_public":           schema.BoolAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}, Description: "Allow unauthenticated read access. Changes are sent with PATCH. Omit to use the API's default."},
			"object_lock_enabled": schema.BoolAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown(), boolplanmodifier.RequiresReplace()}, Description: "Create the bucket with object lock. It is chosen at creation only, so a change replaces the bucket."},
			"default_retention": schema.SingleNestedAttribute{
				Optional: true, Computed: true,
				PlanModifiers: []planmodifier.Object{objectplanmodifier.UseStateForUnknown()},
				Description:   "Default retention for new objects of a bucket with object lock: sent with create, and changes are sent to PUT .../object-lock. Read back from GET .../object-lock. The API decides what it accepts (it currently refuses to remove a rule).",
				Attributes: map[string]schema.Attribute{
					"mode":  schema.StringAttribute{Required: true, Description: "GOVERNANCE or COMPLIANCE."},
					"days":  schema.Int64Attribute{Optional: true, Description: "Retention in days. Give this or years."},
					"years": schema.Int64Attribute{Optional: true, Description: "Retention in years. Give this or days."},
				},
			},
			"status":       schema.StringAttribute{Computed: true},
			"plan":         schema.StringAttribute{Computed: true, Description: "Storage plan returned by the API."},
			"site_id":      schema.StringAttribute{Computed: true},
			"object_count": schema.Int64Attribute{Computed: true},
			"total_size":   schema.Int64Attribute{Computed: true, Description: "Reported storage usage in bytes."},
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

func bucketPath(name string) string { return "/object-storage/buckets/" + url.PathEscape(name) }

func (out bucketAPI) apply(state *bucketResourceModel) {
	if out.Name != "" {
		state.ID = types.StringValue(out.Name)
		state.Name = types.StringValue(out.Name)
	}
	if out.Region != nil {
		state.Region = types.StringValue(*out.Region)
	}
	if out.IsPublic != nil {
		state.IsPublic = types.BoolValue(*out.IsPublic)
	}
	if out.ObjectLockEnabled != nil {
		state.ObjectLockEnabled = types.BoolValue(*out.ObjectLockEnabled)
	}
	state.Status = types.StringPointerValue(out.Status)
	state.Plan = types.StringPointerValue(out.Plan)
	state.SiteID = types.StringPointerValue(out.SiteID)
	state.ObjectCount = types.Int64PointerValue(out.ObjectCount)
	state.TotalSize = types.Int64PointerValue(out.TotalSize)
}

// retentionBody is the configured default_retention as the API's DefaultRetention, or nil.
func retentionBody(value types.Object) *bucketRetention {
	if value.IsNull() || value.IsUnknown() {
		return nil
	}
	a := value.Attributes()
	rule := &bucketRetention{Mode: a["mode"].(types.String).ValueString()}
	if days := a["days"].(types.Int64); known(days) {
		n := days.ValueInt64()
		rule.Days = &n
	}
	if years := a["years"].(types.Int64); known(years) {
		n := years.ValueInt64()
		rule.Years = &n
	}
	return rule
}

// refresh reads the bucket and, for a bucket with object lock, its default retention.
func (r *bucketResource) refresh(ctx context.Context, state *bucketResourceModel) error {
	var out bucketAPI
	if err := r.client.do(ctx, http.MethodGet, bucketPath(state.ID.ValueString()), nil, &out); err != nil {
		return err
	}
	out.apply(state)
	retention := types.ObjectNull(bucketRetentionTypes)
	if out.ObjectLockEnabled != nil && *out.ObjectLockEnabled {
		var lock struct {
			Rule *bucketRetention `json:"rule"`
		}
		if err := r.client.do(ctx, http.MethodGet, bucketPath(state.ID.ValueString())+"/object-lock", nil, &lock); err != nil {
			return fmt.Errorf("read object lock: %w", err)
		}
		if lock.Rule != nil {
			retention = types.ObjectValueMust(bucketRetentionTypes, map[string]attr.Value{"mode": types.StringValue(lock.Rule.Mode), "days": types.Int64PointerValue(lock.Rule.Days), "years": types.Int64PointerValue(lock.Rule.Years)})
		}
	}
	state.DefaultRetention = retention
	return nil
}

func (r *bucketResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan bucketResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"name": plan.Name.ValueString(), "region": plan.Region.ValueString()}
	if known(plan.IsPublic) {
		body["is_public"] = plan.IsPublic.ValueBool()
	}
	if known(plan.ObjectLockEnabled) {
		body["object_lock_enabled"] = plan.ObjectLockEnabled.ValueBool()
	}
	if rule := retentionBody(plan.DefaultRetention); rule != nil {
		body["default_retention"] = rule
	}
	var out bucketAPI
	if err := r.client.do(ctx, http.MethodPost, "/object-storage/buckets", body, &out); err != nil {
		resp.Diagnostics.AddError("Failed to create bucket", err.Error())
		return
	}
	// The name is the bucket's identity; keep it even if the refresh fails.
	plan.ID = plan.Name
	out.apply(&plan)
	for _, value := range []*types.Bool{&plan.IsPublic, &plan.ObjectLockEnabled} {
		if value.IsUnknown() {
			*value = types.BoolNull()
		}
	}
	if plan.DefaultRetention.IsUnknown() {
		plan.DefaultRetention = types.ObjectNull(bucketRetentionTypes)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if err := r.refresh(ctx, &plan); err != nil {
		resp.Diagnostics.AddError("Bucket created but refresh failed", err.Error())
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
	if err := r.refresh(ctx, &state); err != nil {
		if IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read bucket", err.Error())
		return
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
	if known(plan.IsPublic) && !plan.IsPublic.Equal(state.IsPublic) {
		if err := r.client.do(ctx, http.MethodPatch, bucketPath(state.ID.ValueString()), map[string]any{"is_public": plan.IsPublic.ValueBool()}, nil); err != nil {
			resp.Diagnostics.AddError("Failed to update bucket", err.Error())
			return
		}
	}
	if !plan.DefaultRetention.IsUnknown() && !plan.DefaultRetention.Equal(state.DefaultRetention) {
		// A rule removed from the configuration is sent as null; the API decides.
		var rule any
		if configured := retentionBody(plan.DefaultRetention); configured != nil {
			rule = configured
		}
		if err := r.client.do(ctx, http.MethodPut, bucketPath(state.ID.ValueString())+"/object-lock", map[string]any{"rule": rule}, nil); err != nil {
			resp.Diagnostics.AddError("Failed to set bucket default retention", err.Error())
			return
		}
	}
	if err := r.refresh(ctx, &plan); err != nil {
		resp.Diagnostics.AddError("Failed to refresh bucket", err.Error())
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
	if err := r.client.do(ctx, http.MethodDelete, bucketPath(state.ID.ValueString()), nil, nil); err != nil && !IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to delete bucket", err.Error())
	}
}

func (r *bucketResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
