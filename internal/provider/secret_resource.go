package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_                 resource.Resource                   = (*secretResource)(nil)
	_                 resource.ResourceWithConfigure      = (*secretResource)(nil)
	_                 resource.ResourceWithImportState    = (*secretResource)(nil)
	_                 resource.ResourceWithValidateConfig = (*secretResource)(nil)
	secretNamePattern                                     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,63}$`)
)

type secretResource struct{ client *Client }

func NewSecretResource() resource.Resource { return &secretResource{} }

type secretResourceModel struct {
	ID             types.String `tfsdk:"id"`
	StoreID        types.String `tfsdk:"store_id"`
	SecretName     types.String `tfsdk:"secret_name"`
	ValueWO        types.String `tfsdk:"value_wo"`
	ValueWOVersion types.Int64  `tfsdk:"value_wo_version"`
	CurrentVersion types.Int64  `tfsdk:"current_version"`
	StoreKey       types.String `tfsdk:"store_key"`
	Status         types.String `tfsdk:"status"`
}

type secretAPI struct {
	ID         string `json:"id"`
	StoreID    string `json:"store_id"`
	SecretName string `json:"secret_name"`
	StoreKey   string `json:"store_key"`
	Status     string `json:"status"`
}

// Deliberately do not decode SecretValue.data. Plaintext must never enter state.
type secretVersionAPI struct {
	ID       string `json:"id"`
	Metadata struct {
		Version *int64 `json:"version"`
	} `json:"metadata"`
}

func (r *secretResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_secret"
}

func (r *secretResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "An IBEE secret with a write-only JSON value (Terraform 1.11+). Values are never stored in plan or state. Increase value_wo_version to rotate; value changes alone cannot be detected. Destroy soft-deletes the latest value, preserving history and the reserved name. Import with the secret ID; imported value_wo_version starts at 0.",
		Attributes: map[string]schema.Attribute{
			"id":               schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"store_id":         schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"secret_name":      schema.StringAttribute{Required: true, Description: "2–64 lowercase letters, digits and hyphens; first character must be a letter or digit.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"value_wo":         schema.StringAttribute{Required: true, Sensitive: true, WriteOnly: true, Description: "JSON object containing the secret data. Use jsonencode with an ephemeral sensitive input. Never persisted by this resource."},
			"value_wo_version": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(0), Description: "Nonnegative rotation trigger. Increase it whenever changing value_wo. Initial/imported value is 0; the API version is separate."},
			"current_version":  schema.Int64Attribute{Computed: true, Description: "Current API version, refreshed without persisting secret data. Updates use this version for check-and-set to prevent overwriting concurrent changes."},
			"store_key":        schema.StringAttribute{Computed: true},
			"status":           schema.StringAttribute{Computed: true},
		},
	}
}

func (r *secretResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	var ok bool
	r.client, ok = req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *Client, got %T", req.ProviderData))
	}
}

func decodeSecretValue(raw types.String) (map[string]any, error) {
	if raw.IsNull() || raw.IsUnknown() {
		return nil, fmt.Errorf("value_wo must be a known JSON object when creating or rotating a secret")
	}
	var value map[string]any
	decoder := json.NewDecoder(strings.NewReader(raw.ValueString()))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil || value == nil || !json.Valid([]byte(raw.ValueString())) {
		return nil, fmt.Errorf("value_wo must contain exactly one valid JSON object")
	}
	return value, nil
}

func (r *secretResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg secretResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !cfg.SecretName.IsNull() && !cfg.SecretName.IsUnknown() && !secretNamePattern.MatchString(cfg.SecretName.ValueString()) {
		resp.Diagnostics.AddAttributeError(path.Root("secret_name"), "Invalid secret name", "Use 2–64 lowercase letters, digits and hyphens, beginning with a letter or digit.")
	}
	if !cfg.StoreID.IsNull() && !cfg.StoreID.IsUnknown() && (strings.TrimSpace(cfg.StoreID.ValueString()) == "" || strings.ContainsAny(cfg.StoreID.ValueString(), "/\\")) {
		resp.Diagnostics.AddAttributeError(path.Root("store_id"), "Invalid store ID", "Specify a nonempty secret store ID without path separators.")
	}
	if !cfg.ValueWO.IsNull() && !cfg.ValueWO.IsUnknown() {
		if _, err := decodeSecretValue(cfg.ValueWO); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("value_wo"), "Invalid secret value", err.Error())
		}
	}
	if !cfg.ValueWOVersion.IsUnknown() && cfg.ValueWOVersion.ValueInt64() < 0 {
		resp.Diagnostics.AddAttributeError(path.Root("value_wo_version"), "Invalid rotation version", "The rotation trigger must be a nonnegative integer.")
	}
}

func secretPath(id string) string { return "/secret-store/secrets/" + url.PathEscape(id) }

func (out secretAPI) isDeleted() bool {
	// The published contract says deleted; deployed service source currently
	// returns soft_deleted. Both represent retained tombstones, not a 404.
	return out.Status == "deleted" || out.Status == "soft_deleted"
}

func (out secretAPI) apply(state *secretResourceModel) error {
	if out.ID == "" || out.StoreID == "" || out.SecretName == "" || out.StoreKey == "" || out.Status == "" {
		return fmt.Errorf("secret metadata is missing required identity or status fields; state was preserved")
	}
	if !state.ID.IsNull() && !state.ID.IsUnknown() && state.ID.ValueString() != out.ID {
		return fmt.Errorf("secret metadata identifies a different secret; state was preserved")
	}
	state.ID, state.StoreID = types.StringValue(out.ID), types.StringValue(out.StoreID)
	state.SecretName, state.StoreKey, state.Status = types.StringValue(out.SecretName), types.StringValue(out.StoreKey), types.StringValue(out.Status)
	state.ValueWO = types.StringNull()
	return nil
}

// Secret endpoints may echo input or plaintext in an error body. Do not expose
// that body (or JSON conversion errors) in Terraform diagnostics.
func secretSafeError(err error) string {
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		message := fmt.Sprintf("Secret API request failed with HTTP %d. Response details are omitted to protect secret data.", apiErr.Status)
		if guidance := apiErr.guidance(); guidance != "" {
			message += " " + guidance
		}
		return message
	}
	if errors.Is(err, context.Canceled) {
		return "Secret API request was cancelled."
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "Secret API request timed out."
	}
	return "Secret API request failed. Response details are omitted to protect secret data; check API availability and permissions."
}

func (r *secretResource) readVersion(ctx context.Context, id string) (types.Int64, error) {
	var out secretVersionAPI
	if err := r.client.do(ctx, http.MethodGet, secretPath(id)+"/value", nil, &out); err != nil {
		return types.Int64Null(), fmt.Errorf("%s", secretSafeError(err))
	}
	if out.ID != id || out.Metadata.Version == nil || *out.Metadata.Version < 1 {
		return types.Int64Null(), fmt.Errorf("secret value response did not provide a valid matching identity and positive version")
	}
	return types.Int64Value(*out.Metadata.Version), nil
}

func (r *secretResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan secretResourceModel
	var valueWO types.String
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	// Write-only values are supplied in Config, never in Plan.
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("value_wo"), &valueWO)...)
	if resp.Diagnostics.HasError() {
		return
	}
	value, err := decodeSecretValue(valueWO)
	if err != nil {
		resp.Diagnostics.AddError("Invalid secret value", err.Error())
		return
	}
	if err := r.client.requireBillingEligibility(ctx, "", nil); err != nil {
		resp.Diagnostics.AddError("Secret billing eligibility failed", err.Error())
		return
	}
	var out secretAPI
	if err := r.client.do(ctx, http.MethodPost, secretStorePath(plan.StoreID.ValueString())+"/secrets", map[string]any{"secret_name": plan.SecretName.ValueString(), "value": value}, &out); err != nil {
		resp.Diagnostics.AddError("Failed to create secret", secretSafeError(err))
		return
	}
	plan.ValueWO, plan.CurrentVersion = types.StringNull(), types.Int64Null()
	if out.ID != "" {
		plan.ID, plan.StoreKey, plan.Status = types.StringValue(out.ID), types.StringValue(out.StoreKey), types.StringValue(out.Status)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
	if err := out.apply(&plan); err != nil {
		resp.Diagnostics.AddError("Invalid created secret response", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	version, err := r.readVersion(ctx, plan.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Secret created but version refresh failed", err.Error())
		return
	}
	plan.CurrentVersion = version
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *secretResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state secretResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out secretAPI
	if err := r.client.do(ctx, http.MethodGet, secretPath(state.ID.ValueString()), nil, &out); err != nil {
		if IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read secret metadata", secretSafeError(err))
		return
	}
	if err := out.apply(&state); err != nil {
		resp.Diagnostics.AddError("Invalid secret metadata", err.Error())
		return
	}
	if state.ValueWOVersion.IsNull() {
		state.ValueWOVersion = types.Int64Value(0)
	}
	if out.isDeleted() {
		state.CurrentVersion = types.Int64Null()
		resp.Diagnostics.AddWarning("Secret is soft-deleted", "The record, reserved name and historical versions remain. Restore it through the portal before rotating it. Terraform preserves its identity instead of attempting to create a duplicate.")
	} else {
		version, err := r.readVersion(ctx, state.ID.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Failed to read secret version", err.Error())
			return
		}
		state.CurrentVersion = version
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *secretResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state secretResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ValueWO = types.StringNull()
	if plan.ValueWOVersion.Equal(state.ValueWOVersion) {
		plan.CurrentVersion, plan.Status, plan.StoreKey = state.CurrentVersion, state.Status, state.StoreKey
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}
	if plan.ValueWOVersion.ValueInt64() <= state.ValueWOVersion.ValueInt64() {
		resp.Diagnostics.AddError("Rotation version must increase", "Increase value_wo_version to rotate the secret; decreasing the trigger is not allowed.")
		return
	}
	if state.Status.ValueString() != "active" || state.CurrentVersion.IsNull() || state.CurrentVersion.IsUnknown() || state.CurrentVersion.ValueInt64() < 1 {
		resp.Diagnostics.AddError("Cannot safely rotate secret", "Refresh an active secret with a valid current_version before rotation. Restore a soft-deleted secret through the portal first.")
		return
	}
	var valueWO types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("value_wo"), &valueWO)...)
	if resp.Diagnostics.HasError() {
		return
	}
	value, err := decodeSecretValue(valueWO)
	if err != nil {
		resp.Diagnostics.AddError("Invalid secret value", err.Error())
		return
	}
	var out secretVersionAPI
	body := map[string]any{"value": value, "cas": state.CurrentVersion.ValueInt64()}
	if err := r.client.do(ctx, http.MethodPut, secretPath(state.ID.ValueString())+"/value", body, &out); err != nil {
		resp.Diagnostics.AddError("Failed to rotate secret", secretSafeError(err)+" On a version conflict, refresh and review the external change before retrying.")
		return
	}
	if out.ID != state.ID.ValueString() || out.Metadata.Version == nil || *out.Metadata.Version <= state.CurrentVersion.ValueInt64() {
		resp.Diagnostics.AddError("Secret rotation response invalid", "The API did not return a matching ID and a newer version. Refresh and review the current secret before retrying.")
		return
	}
	plan.CurrentVersion, plan.StoreKey, plan.Status = types.Int64Value(*out.Metadata.Version), state.StoreKey, types.StringValue("active")
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *secretResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state secretResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out secretAPI
	if err := r.client.do(ctx, http.MethodGet, secretPath(state.ID.ValueString()), nil, &out); err != nil {
		if IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Cannot read secret before deletion", secretSafeError(err))
		return
	}
	if err := out.apply(&state); err != nil {
		resp.Diagnostics.AddError("Cannot verify secret before deletion", err.Error())
		return
	}
	if out.isDeleted() {
		return
	}
	if err := r.client.do(ctx, http.MethodDelete, secretPath(state.ID.ValueString()), nil, &out); err != nil {
		if !IsNotFound(err) {
			resp.Diagnostics.AddError("Failed to soft-delete secret", secretSafeError(err))
		}
		return
	}
	if out.ID != state.ID.ValueString() || !out.isDeleted() {
		resp.Diagnostics.AddError("Secret deletion not confirmed", "The response did not confirm soft deletion of this secret. State is preserved for retry.")
	}
}

func (r *secretResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if strings.TrimSpace(req.ID) == "" || strings.ContainsAny(req.ID, "/\\") {
		resp.Diagnostics.AddError("Invalid secret import ID", "Import with the secret ID.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("value_wo_version"), 0)...)
}
