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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.ResourceWithImportState = (*secretIdentityResource)(nil)
	_ resource.ResourceWithImportState = (*secretIdentityScopeResource)(nil)
)

func configureSecretResource(req resource.ConfigureRequest, resp *resource.ConfigureResponse) *Client {
	if req.ProviderData == nil {
		return nil
	}
	client, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *Client, got %T", req.ProviderData))
	}
	return client
}

func secretIdentityPath(id string) string { return "/secret-store/identities/" + url.PathEscape(id) }

// splitImportID splits "first/second" import IDs.
func splitImportID(id, format string, resp *resource.ImportStateResponse) (string, string, bool) {
	first, second, ok := strings.Cut(id, "/")
	if !ok || first == "" || second == "" || strings.Contains(second, "/") {
		resp.Diagnostics.AddError("Invalid import ID", "Import with "+format+".")
		return "", "", false
	}
	return first, second, true
}

type secretIdentityResource struct{ client *Client }

func NewSecretIdentityResource() resource.Resource { return &secretIdentityResource{} }

type secretIdentityModel struct {
	ID                types.String `tfsdk:"id"`
	StoreID           types.String `tfsdk:"store_id"`
	Name              types.String `tfsdk:"name"`
	AuthMethod        types.String `tfsdk:"auth_method"`
	TokenPolicyMode   types.String `tfsdk:"token_policy_mode"`
	K8sNamespace      types.String `tfsdk:"k8s_namespace"`
	K8sServiceAccount types.String `tfsdk:"k8s_service_account"`
	Enabled           types.Bool   `tfsdk:"enabled"`
	Status            types.String `tfsdk:"status"`
	OpenbaoRoleName   types.String `tfsdk:"openbao_role_name"`
	RoleID            types.String `tfsdk:"role_id"`
	SecretID          types.String `tfsdk:"secret_id"`
}

type secretIdentityAPI struct {
	ID                string  `json:"id"`
	Name              string  `json:"name"`
	AuthMethod        string  `json:"auth_method"`
	Status            string  `json:"status"`
	TokenPolicyMode   string  `json:"token_policy_mode"`
	OpenbaoRoleName   string  `json:"openbao_role_name"`
	K8sNamespace      *string `json:"k8s_namespace"`
	K8sServiceAccount *string `json:"k8s_service_account"`
}

func (r *secretIdentityResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_secret_identity"
}

func (r *secretIdentityResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		Description: "A Secret Store application identity (AppRole or Kubernetes) that applications use to read secrets. Creation also grants it a scope on store_id; use ibee_secret_identity_scope for other stores. The API decides what is allowed. Each read of an AppRole identity's login details mints a new secret ID, so the provider reads them once, at creation, and keeps role_id and secret_id in state (secret_id is sensitive but not encrypted by the provider; use an encrypted state backend). Imports leave them null. Destroy permanently deletes the identity, its scopes and its sessions. Import with STORE_ID/IDENTITY_ID.",
		Attributes: map[string]schema.Attribute{
			"id":                  schema.StringAttribute{Computed: true, PlanModifiers: keep},
			"store_id":            schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Store the identity is created in and first granted access to."},
			"name":                schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Identity name, 1–128 characters. The API cannot rename identities, so a change replaces it."},
			"auth_method":         schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "approle or kubernetes.", Validators: []validator.String{computeOneOf("approle", "kubernetes")}},
			"token_policy_mode":   schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keep, Description: "read_only or read_write. The API default applies when omitted. A change is applied in place; the API also aligns the identity's existing scopes and revokes its sessions.", Validators: []validator.String{computeOneOf("read_only", "read_write")}},
			"k8s_namespace":       schema.StringAttribute{Optional: true, PlanModifiers: replace, Description: "Kubernetes namespace (kubernetes)."},
			"k8s_service_account": schema.StringAttribute{Optional: true, PlanModifiers: replace, Description: "Kubernetes service account (kubernetes)."},
			"enabled":             schema.BoolAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}, Description: "Set false to disable the identity and revoke its sessions; true enables it again. New identities are enabled."},
			"status":              schema.StringAttribute{Computed: true, Description: "active or disabled, as the API reports."},
			"openbao_role_name":   schema.StringAttribute{Computed: true, PlanModifiers: keep},
			"role_id":             schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: "AppRole role ID, read once at creation."},
			"secret_id":           schema.StringAttribute{Computed: true, Sensitive: true, PlanModifiers: keep, Description: "AppRole secret ID, returned once at creation and stored in state. Null after import and for Kubernetes identities."},
		},
	}
}

func (r *secretIdentityResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureSecretResource(req, resp)
}

func (out secretIdentityAPI) apply(state *secretIdentityModel) error {
	if out.ID == "" || out.Name == "" || out.AuthMethod == "" || out.Status == "" || out.TokenPolicyMode == "" {
		return fmt.Errorf("identity response is missing id, name, auth_method, status or token_policy_mode; state was preserved")
	}
	if !state.ID.IsNull() && !state.ID.IsUnknown() && state.ID.ValueString() != out.ID {
		return fmt.Errorf("identity response identifies a different identity; state was preserved")
	}
	state.ID = types.StringValue(out.ID)
	state.Name = types.StringValue(out.Name)
	state.AuthMethod = types.StringValue(out.AuthMethod)
	state.Status = types.StringValue(out.Status)
	state.Enabled = types.BoolValue(out.Status == "active")
	state.TokenPolicyMode = types.StringValue(out.TokenPolicyMode)
	state.OpenbaoRoleName = types.StringValue(out.OpenbaoRoleName)
	state.K8sNamespace = types.StringPointerValue(out.K8sNamespace)
	state.K8sServiceAccount = types.StringPointerValue(out.K8sServiceAccount)
	return nil
}

// setEnabled calls disable or enable, which answer with the identity.
func (r *secretIdentityResource) setEnabled(ctx context.Context, id string, enabled bool) (secretIdentityAPI, error) {
	operation := "/disable"
	if enabled {
		operation = "/enable"
	}
	var out secretIdentityAPI
	err := r.client.do(ctx, http.MethodPost, secretIdentityPath(id)+operation, nil, &out)
	return out, err
}

func (r *secretIdentityResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan secretIdentityModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"name": plan.Name.ValueString(), "auth_method": plan.AuthMethod.ValueString()}
	for key, value := range map[string]types.String{"token_policy_mode": plan.TokenPolicyMode, "k8s_namespace": plan.K8sNamespace, "k8s_service_account": plan.K8sServiceAccount} {
		if !value.IsNull() && !value.IsUnknown() {
			body[key] = value.ValueString()
		}
	}
	var out secretIdentityAPI
	if err := r.client.do(ctx, http.MethodPost, secretStorePath(plan.StoreID.ValueString())+"/identities", body, &out); err != nil {
		resp.Diagnostics.AddError("Failed to create secret identity", err.Error())
		return
	}
	wantEnabled := plan.Enabled
	plan.RoleID, plan.SecretID = types.StringNull(), types.StringNull()
	if err := out.apply(&plan); err != nil {
		resp.Diagnostics.AddError("Invalid created secret identity response", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// The login details are returned once here; later reads never fetch them.
	var access struct {
		RoleID   *string `json:"role_id"`
		SecretID *string `json:"secret_id"`
	}
	if err := r.client.do(ctx, http.MethodGet, secretIdentityPath(out.ID)+"/access", nil, &access); err != nil {
		resp.Diagnostics.AddError("Secret identity created but its login details could not be read", err.Error()+". The identity exists; role_id and secret_id stay empty.")
		return
	}
	plan.RoleID, plan.SecretID = types.StringPointerValue(access.RoleID), types.StringPointerValue(access.SecretID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if !wantEnabled.IsNull() && !wantEnabled.IsUnknown() && wantEnabled.ValueBool() != plan.Enabled.ValueBool() {
		changed, err := r.setEnabled(ctx, out.ID, wantEnabled.ValueBool())
		if err != nil {
			resp.Diagnostics.AddError("Secret identity created but enabling or disabling it failed", err.Error())
			return
		}
		if err := changed.apply(&plan); err != nil {
			resp.Diagnostics.AddError("Invalid secret identity response", err.Error())
			return
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
}

func (r *secretIdentityResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state secretIdentityModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out secretIdentityAPI
	if err := r.client.do(ctx, http.MethodGet, secretIdentityPath(state.ID.ValueString()), nil, &out); err != nil {
		if IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read secret identity", err.Error())
		return
	}
	if err := out.apply(&state); err != nil {
		resp.Diagnostics.AddError("Invalid secret identity response", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *secretIdentityResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state secretIdentityModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	if !plan.TokenPolicyMode.IsUnknown() && !plan.TokenPolicyMode.Equal(state.TokenPolicyMode) {
		var out secretIdentityAPI
		if err := r.client.do(ctx, http.MethodPatch, secretIdentityPath(id), map[string]any{"token_policy_mode": plan.TokenPolicyMode.ValueString()}, &out); err != nil {
			resp.Diagnostics.AddError("Failed to update secret identity", err.Error())
			return
		}
		if err := out.apply(&state); err != nil {
			resp.Diagnostics.AddError("Invalid secret identity response", err.Error())
			return
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	}
	if !plan.Enabled.IsUnknown() && !plan.Enabled.IsNull() && plan.Enabled.ValueBool() != state.Enabled.ValueBool() {
		out, err := r.setEnabled(ctx, id, plan.Enabled.ValueBool())
		if err != nil {
			resp.Diagnostics.AddError("Failed to enable or disable secret identity", err.Error())
			return
		}
		if err := out.apply(&state); err != nil {
			resp.Diagnostics.AddError("Invalid secret identity response", err.Error())
			return
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *secretIdentityResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state secretIdentityModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out struct {
		Status string `json:"status"`
	}
	if err := r.client.do(ctx, http.MethodDelete, secretIdentityPath(state.ID.ValueString()), nil, &out); err != nil && !IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to delete secret identity", err.Error())
	}
}

func (r *secretIdentityResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	store, id, ok := splitImportID(req.ID, "STORE_ID/IDENTITY_ID", resp)
	if !ok {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("store_id"), store)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("role_id"), types.StringNull())...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("secret_id"), types.StringNull())...)
}

type secretIdentityScopeResource struct{ client *Client }

func NewSecretIdentityScopeResource() resource.Resource { return &secretIdentityScopeResource{} }

type secretIdentityScopeModel struct {
	ID               types.String `tfsdk:"id"`
	IdentityID       types.String `tfsdk:"identity_id"`
	StoreID          types.String `tfsdk:"store_id"`
	AccessMode       types.String `tfsdk:"access_mode"`
	AllowVersionRead types.Bool   `tfsdk:"allow_version_read"`
	AllowRollback    types.Bool   `tfsdk:"allow_rollback"`
	AllowDestroy     types.Bool   `tfsdk:"allow_destroy"`
}

type secretIdentityScopeAPI struct {
	ID               string `json:"id"`
	IdentityID       string `json:"identity_id"`
	StoreID          string `json:"store_id"`
	AccessMode       string `json:"access_mode"`
	AllowVersionRead *bool  `json:"allow_version_read"`
	AllowRollback    *bool  `json:"allow_rollback"`
	AllowDestroy     *bool  `json:"allow_destroy"`
}

func (r *secretIdentityScopeResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_secret_identity_scope"
}

func (r *secretIdentityScopeResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keepBool := []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		Description: "Grants a Secret Store application identity access to a store. The API decides which combinations are allowed. Permissions change in place. Destroy removes the permission. Import with IDENTITY_ID/SCOPE_ID.",
		Attributes: map[string]schema.Attribute{
			"id":                 schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"identity_id":        schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Identity to grant access, such as ibee_secret_identity.id."},
			"store_id":           schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Store to grant access to."},
			"access_mode":        schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, Description: "read_only or read_write. The API default applies when omitted.", Validators: []validator.String{computeOneOf("read_only", "read_write")}},
			"allow_version_read": schema.BoolAttribute{Optional: true, Computed: true, PlanModifiers: keepBool, Description: "Allow reading older secret versions. The API default applies when omitted."},
			"allow_rollback":     schema.BoolAttribute{Optional: true, Computed: true, PlanModifiers: keepBool, Description: "Allow rolling secrets back. The API default applies when omitted."},
			"allow_destroy":      schema.BoolAttribute{Optional: true, Computed: true, PlanModifiers: keepBool, Description: "Allow destroying secret versions. The API default applies when omitted."},
		},
	}
}

func (r *secretIdentityScopeResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureSecretResource(req, resp)
}

func (out secretIdentityScopeAPI) apply(state *secretIdentityScopeModel) error {
	if out.ID == "" || out.IdentityID == "" || out.StoreID == "" || out.AccessMode == "" || out.AllowVersionRead == nil || out.AllowRollback == nil || out.AllowDestroy == nil {
		return fmt.Errorf("scope response is missing id, identity_id, store_id, access_mode or a permission; state was preserved")
	}
	if !state.ID.IsNull() && !state.ID.IsUnknown() && state.ID.ValueString() != out.ID {
		return fmt.Errorf("scope response identifies a different scope; state was preserved")
	}
	state.ID = types.StringValue(out.ID)
	state.IdentityID = types.StringValue(out.IdentityID)
	state.StoreID = types.StringValue(out.StoreID)
	state.AccessMode = types.StringValue(out.AccessMode)
	state.AllowVersionRead = types.BoolValue(*out.AllowVersionRead)
	state.AllowRollback = types.BoolValue(*out.AllowRollback)
	state.AllowDestroy = types.BoolValue(*out.AllowDestroy)
	return nil
}

// permissions sends access_mode and each permission that is set.
func (m secretIdentityScopeModel) permissions(body map[string]any) map[string]any {
	if !m.AccessMode.IsNull() && !m.AccessMode.IsUnknown() {
		body["access_mode"] = m.AccessMode.ValueString()
	}
	for key, value := range map[string]types.Bool{"allow_version_read": m.AllowVersionRead, "allow_rollback": m.AllowRollback, "allow_destroy": m.AllowDestroy} {
		if !value.IsNull() && !value.IsUnknown() {
			body[key] = value.ValueBool()
		}
	}
	return body
}

func (r *secretIdentityScopeResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan secretIdentityScopeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out secretIdentityScopeAPI
	if err := r.client.do(ctx, http.MethodPost, secretIdentityPath(plan.IdentityID.ValueString())+"/scopes", plan.permissions(map[string]any{"store_id": plan.StoreID.ValueString()}), &out); err != nil {
		resp.Diagnostics.AddError("Failed to create secret identity scope", err.Error())
		return
	}
	if err := out.apply(&plan); err != nil {
		resp.Diagnostics.AddError("Invalid created secret identity scope response", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *secretIdentityScopeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state secretIdentityScopeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// The API has no single-scope read, so find the scope in the identity's list.
	var list struct {
		Scopes *[]secretIdentityScopeAPI `json:"scopes"`
	}
	if err := r.client.do(ctx, http.MethodGet, secretIdentityPath(state.IdentityID.ValueString())+"/scopes", nil, &list); err != nil {
		if IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read secret identity scopes", err.Error())
		return
	}
	if list.Scopes == nil {
		resp.Diagnostics.AddError("Invalid secret identity scopes response", "The response omitted the scopes array; state was preserved.")
		return
	}
	for _, scope := range *list.Scopes {
		if scope.ID == state.ID.ValueString() {
			if err := scope.apply(&state); err != nil {
				resp.Diagnostics.AddError("Invalid secret identity scope response", err.Error())
				return
			}
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			return
		}
	}
	resp.State.RemoveResource(ctx)
}

func (r *secretIdentityScopeResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state secretIdentityScopeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out secretIdentityScopeAPI
	if err := r.client.do(ctx, http.MethodPatch, "/secret-store/scopes/"+url.PathEscape(state.ID.ValueString()), plan.permissions(map[string]any{}), &out); err != nil {
		resp.Diagnostics.AddError("Failed to update secret identity scope", err.Error())
		return
	}
	if err := out.apply(&state); err != nil {
		resp.Diagnostics.AddError("Invalid secret identity scope response", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *secretIdentityScopeResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state secretIdentityScopeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out struct {
		Status string `json:"status"`
	}
	if err := r.client.do(ctx, http.MethodDelete, "/secret-store/scopes/"+url.PathEscape(state.ID.ValueString()), nil, &out); err != nil && !IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to delete secret identity scope", err.Error())
	}
}

func (r *secretIdentityScopeResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	identity, id, ok := splitImportID(req.ID, "IDENTITY_ID/SCOPE_ID", resp)
	if !ok {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("identity_id"), identity)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}
