package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                   = (*bucketRetentionResource)(nil)
	_ resource.ResourceWithConfigure      = (*bucketRetentionResource)(nil)
	_ resource.ResourceWithValidateConfig = (*bucketRetentionResource)(nil)
	_ resource.ResourceWithImportState    = (*bucketRetentionResource)(nil)
)

type bucketRetentionResource struct{ client *Client }

func NewBucketRetentionResource() resource.Resource { return &bucketRetentionResource{} }

type bucketRetentionResourceModel struct {
	ID              types.String `tfsdk:"id"`
	BucketName      types.String `tfsdk:"bucket_name"`
	Mode            types.String `tfsdk:"mode"`
	Days            types.Int64  `tfsdk:"days"`
	Years           types.Int64  `tfsdk:"years"`
	RetainOnDestroy types.Bool   `tfsdk:"retain_on_destroy"`
}

type bucketRetentionRule struct {
	Mode  string `json:"mode"`
	Days  *int64 `json:"days,omitempty"`
	Years *int64 `json:"years,omitempty"`
}

type bucketRetentionAPI struct {
	ObjectLockEnabled string          `json:"object_lock_enabled"`
	Rule              json.RawMessage `json:"rule"`
}

func (r *bucketRetentionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_bucket_retention"
}

func (r *bucketRetentionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manage the default retention rule of an Object Lock-enabled bucket through its native object-lock-configuration endpoint. Exactly one of days or years is required. The current service cannot clear this rule. Destroy therefore fails by default; explicitly applying retain_on_destroy=true permits Terraform to relinquish management while retaining the remote rule and all protected objects. Never use this resource to erase retained data. Endpoint deployment must be verified before live use.",
		Attributes: map[string]schema.Attribute{
			"id":                schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, Description: "Bucket name, also used as the import ID."},
			"bucket_name":       schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, Description: "Existing bucket created with object_lock_enabled=true. Import an existing default rule instead of overwriting it during creation."},
			"mode":              schema.StringAttribute{Required: true, Description: "GOVERNANCE or COMPLIANCE. Changes affect the default rule; previously protected objects retain their existing retention."},
			"days":              schema.Int64Attribute{Optional: true, Description: "Default duration in days, 1–36500. Conflicts with years."},
			"years":             schema.Int64Attribute{Optional: true, Description: "Default duration in years, 1–100. Conflicts with days."},
			"retain_on_destroy": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "Explicitly allow Terraform destroy to stop managing this rule without clearing it. Apply true before destroy. The API has no supported default-retention delete/clear operation."},
		},
	}
}

func (r *bucketRetentionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	var ok bool
	r.client, ok = req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *Client, got %T", req.ProviderData))
	}
}

func (r *bucketRetentionResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg bucketRetentionResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !cfg.BucketName.IsNull() && !cfg.BucketName.IsUnknown() && (strings.TrimSpace(cfg.BucketName.ValueString()) == "" || strings.ContainsAny(cfg.BucketName.ValueString(), "/\\")) {
		resp.Diagnostics.AddAttributeError(path.Root("bucket_name"), "Invalid bucket name", "Specify a bucket name without path separators.")
	}
	if !cfg.Mode.IsNull() && !cfg.Mode.IsUnknown() && cfg.Mode.ValueString() != "GOVERNANCE" && cfg.Mode.ValueString() != "COMPLIANCE" {
		resp.Diagnostics.AddAttributeError(path.Root("mode"), "Invalid retention mode", "Use GOVERNANCE or COMPLIANCE.")
	}
	if !cfg.Days.IsUnknown() && !cfg.Years.IsUnknown() && cfg.Days.IsNull() == cfg.Years.IsNull() {
		resp.Diagnostics.AddError("Invalid retention duration", "Set exactly one of days or years.")
	}
	if !cfg.Days.IsNull() && !cfg.Days.IsUnknown() && (cfg.Days.ValueInt64() < 1 || cfg.Days.ValueInt64() > 36500) {
		resp.Diagnostics.AddAttributeError(path.Root("days"), "Invalid retention duration", "Days must be between 1 and 36500.")
	}
	if !cfg.Years.IsNull() && !cfg.Years.IsUnknown() && (cfg.Years.ValueInt64() < 1 || cfg.Years.ValueInt64() > 100) {
		resp.Diagnostics.AddAttributeError(path.Root("years"), "Invalid retention duration", "Years must be between 1 and 100.")
	}
}

func bucketRetentionPath(name string) string { return bucketPath(name) + "/object-lock-configuration" }

func (rule bucketRetentionRule) validate() error {
	if rule.Mode != "GOVERNANCE" && rule.Mode != "COMPLIANCE" {
		return fmt.Errorf("retention response has an unsupported mode")
	}
	if (rule.Days == nil) == (rule.Years == nil) {
		return fmt.Errorf("retention response must contain exactly one of days or years")
	}
	if rule.Days != nil && (*rule.Days < 1 || *rule.Days > 36500) {
		return fmt.Errorf("retention days are outside the supported range")
	}
	if rule.Years != nil && (*rule.Years < 1 || *rule.Years > 100) {
		return fmt.Errorf("retention years are outside the supported range")
	}
	return nil
}

func (r *bucketRetentionResource) readRule(ctx context.Context, bucketName string) (*bucketRetentionRule, error) {
	var out bucketRetentionAPI
	if err := r.client.do(ctx, http.MethodGet, bucketRetentionPath(bucketName), nil, &out); err != nil {
		if !IsNotFound(err) {
			return nil, err
		}
		// A 404 can mean a missing bucket, disabled Object Lock, or an
		// undeployed route. Only the first two establish resource absence.
		var bucket bucketAPI
		if bucketErr := r.client.do(ctx, http.MethodGet, bucketPath(bucketName), nil, &bucket); bucketErr != nil {
			if IsNotFound(bucketErr) {
				return nil, nil
			}
			return nil, bucketErr
		}
		if bucket.Name != bucketName || bucket.ObjectLockEnabled == nil {
			return nil, fmt.Errorf("cannot determine Object Lock status after retention endpoint returned 404")
		}
		if !*bucket.ObjectLockEnabled {
			return nil, nil
		}
		return nil, fmt.Errorf("retention endpoint returned 404 for an existing Object Lock-enabled bucket; verify endpoint deployment before retrying")
	}
	if out.ObjectLockEnabled != "Enabled" || len(out.Rule) == 0 {
		return nil, fmt.Errorf("retention response is missing Object Lock status or the rule field")
	}
	if string(out.Rule) == "null" {
		return nil, nil
	}
	var rule bucketRetentionRule
	if err := json.Unmarshal(out.Rule, &rule); err != nil {
		return nil, fmt.Errorf("invalid retention rule response: %w", err)
	}
	if err := rule.validate(); err != nil {
		return nil, err
	}
	return &rule, nil
}

func (state *bucketRetentionResourceModel) applyRule(rule *bucketRetentionRule) {
	state.Mode = types.StringValue(rule.Mode)
	state.Days = types.Int64PointerValue(rule.Days)
	state.Years = types.Int64PointerValue(rule.Years)
}

func (state bucketRetentionResourceModel) requestRule() (bucketRetentionRule, error) {
	rule := bucketRetentionRule{Mode: state.Mode.ValueString()}
	if !state.Days.IsNull() && !state.Days.IsUnknown() {
		v := state.Days.ValueInt64()
		rule.Days = &v
	}
	if !state.Years.IsNull() && !state.Years.IsUnknown() {
		v := state.Years.ValueInt64()
		rule.Years = &v
	}
	return rule, rule.validate()
}

func (r *bucketRetentionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan bucketRetentionResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rule, err := plan.requestRule()
	if err != nil {
		resp.Diagnostics.AddError("Invalid retention rule", err.Error())
		return
	}
	var bucket bucketAPI
	if err := r.client.do(ctx, http.MethodGet, bucketPath(plan.BucketName.ValueString()), nil, &bucket); err != nil {
		resp.Diagnostics.AddError("Cannot verify retention bucket", err.Error())
		return
	}
	if bucket.Name != plan.BucketName.ValueString() || bucket.ObjectLockEnabled == nil || !*bucket.ObjectLockEnabled {
		resp.Diagnostics.AddError("Bucket Object Lock is required", "Create the bucket with object_lock_enabled=true before adding default retention.")
		return
	}
	existing, err := r.readRule(ctx, plan.BucketName.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Cannot inspect existing bucket retention", err.Error())
		return
	}
	if existing != nil {
		resp.Diagnostics.AddError("Bucket retention already exists", "Import this bucket's default rule using the bucket name before modifying it. Existing retention was not overwritten.")
		return
	}
	// This changes bucket policy only; it does not provision capacity or
	// purchase storage. Service-side operation authorization is authoritative.
	if err := r.client.do(ctx, http.MethodPut, bucketRetentionPath(plan.BucketName.ValueString()), map[string]any{"rule": rule}, nil); err != nil {
		resp.Diagnostics.AddError("Failed to set bucket retention", err.Error())
		return
	}
	plan.ID = plan.BucketName
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	actual, err := r.readRule(ctx, plan.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Retention set but refresh failed", err.Error())
		return
	}
	if actual == nil {
		resp.Diagnostics.AddError("Retention set but missing on refresh", "The service did not return the configured rule. State is preserved for recovery.")
		return
	}
	plan.applyRule(actual)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *bucketRetentionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state bucketRetentionResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rule, err := r.readRule(ctx, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to read bucket retention", err.Error())
		return
	}
	if rule == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	state.BucketName = state.ID
	if state.RetainOnDestroy.IsNull() {
		state.RetainOnDestroy = types.BoolValue(false)
	}
	state.applyRule(rule)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *bucketRetentionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state bucketRetentionResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rule, err := plan.requestRule()
	if err != nil {
		resp.Diagnostics.AddError("Invalid retention rule", err.Error())
		return
	}
	if !plan.Mode.Equal(state.Mode) || !plan.Days.Equal(state.Days) || !plan.Years.Equal(state.Years) {
		if err := r.client.do(ctx, http.MethodPut, bucketRetentionPath(state.ID.ValueString()), map[string]any{"rule": rule}, nil); err != nil {
			resp.Diagnostics.AddError("Failed to update bucket retention", err.Error())
			return
		}
	}
	actual, err := r.readRule(ctx, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to refresh bucket retention", err.Error())
		return
	}
	if actual == nil {
		resp.Diagnostics.AddError("Bucket retention missing", "The service no longer returns a default rule. Refresh and review the plan before retrying.")
		return
	}
	plan.applyRule(actual)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *bucketRetentionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state bucketRetentionResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rule, err := r.readRule(ctx, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Cannot verify retained bucket rule", err.Error())
		return
	}
	if rule == nil {
		return
	}
	if !state.RetainOnDestroy.ValueBool() {
		resp.Diagnostics.AddError("Bucket retention cannot be cleared through this API", "The service has no default-retention delete operation and rejects an empty rule. To deliberately stop managing this rule while retaining it remotely, apply retain_on_destroy=true before destroy. Protected objects are never erased by this resource.")
		return
	}
	resp.Diagnostics.AddWarning("Bucket retention remains active", "Terraform is relinquishing management because retain_on_destroy=true. The remote default rule, Object Lock and existing object retention remain unchanged.")
}

func (r *bucketRetentionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if strings.TrimSpace(req.ID) == "" || strings.ContainsAny(req.ID, "/\\") {
		resp.Diagnostics.AddError("Invalid retention import ID", "Import with the bucket name.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("bucket_name"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("retain_on_destroy"), false)...)
}
