package provider

// Secret Manager (contract 0.11.0): the public API manages names, metadata and app
// credentials. Secret values are read and written only at the workspace's Vault API,
// so no resource here takes or returns a value.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const secretManagerTimeout = 15 * time.Minute

// secretManagerPath escapes each segment of a secret path such as apps/billing/db.
func secretManagerPath(prefix, secretPath string) string {
	parts := strings.Split(strings.Trim(secretPath, "/"), "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return "/secret-manager/" + prefix + "/" + strings.Join(parts, "/")
}

// findString returns the first non-empty string value of any of keys, searching
// nested objects (Secret Manager answers carry ids inside an operation payload).
func findString(value any, keys ...string) string {
	switch v := value.(type) {
	case map[string]any:
		for _, key := range keys {
			if s, ok := v[key].(string); ok && s != "" {
				return s
			}
		}
		for _, nested := range v {
			if s := findString(nested, keys...); s != "" {
				return s
			}
		}
	case []any:
		for _, nested := range v {
			if s := findString(nested, keys...); s != "" {
				return s
			}
		}
	}
	return ""
}

// waitSecretManagerOperation polls GET /secret-manager/operations/{id} until it ends.
func (c *Client) waitSecretManagerOperation(ctx context.Context, operationID string) error {
	ctx, cancel := context.WithTimeout(ctx, secretManagerTimeout)
	defer cancel()
	for {
		var op struct {
			Status    string  `json:"status"`
			ErrorCode *string `json:"error_code"`
		}
		if err := c.do(ctx, http.MethodGet, "/secret-manager/operations/"+url.PathEscape(operationID), nil, &op); err != nil {
			return fmt.Errorf("read Secret Manager operation %s: %w", operationID, err)
		}
		switch op.Status {
		case "completed":
			return nil
		case "failed":
			code := ""
			if op.ErrorCode != nil {
				code = *op.ErrorCode
			}
			return fmt.Errorf("Secret Manager operation %s failed: %s", operationID, code)
		}
		if err := waitContext(ctx, c.pollDelay()); err != nil {
			return fmt.Errorf("Secret Manager operation %s did not finish: %w", operationID, err)
		}
	}
}

func (c *Client) pollDelay() time.Duration {
	if c.pollInterval > 0 {
		return c.pollInterval
	}
	return 2 * time.Second
}

// ---------------------------------------------------------------- ibee_secret_manager

type secretManagerResource struct{ client *Client }

func NewSecretManagerResource() resource.Resource { return &secretManagerResource{} }

type secretManagerModel struct {
	ID          types.String `tfsdk:"id"`
	Namespace   types.String `tfsdk:"namespace"`
	MaxVersions types.Int64  `tfsdk:"max_versions"`
	State       types.String `tfsdk:"state"`
	Endpoint    types.String `tfsdk:"endpoint"`
}

type secretManagerWorkspaceAPI struct {
	WorkspaceID string  `json:"workspace_id"`
	Namespace   string  `json:"namespace"`
	State       string  `json:"state"`
	MaxVersions int64   `json:"max_versions"`
	Endpoint    *string `json:"endpoint"`
}

func (r *secretManagerResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_secret_manager"
}

func (r *secretManagerResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		Description: "Secret Manager for the provider's workspace. Create enables it with a namespace and waits until it is active. Enabling is permanent: the namespace and default version limit can't change afterwards, and destroy only removes the resource from Terraform state (Secret Manager stays enabled). Secret values are written and read at the workspace's Vault API (`endpoint`), never through this provider. Import with any ID; the workspace's current Secret Manager is read.",
		Attributes: map[string]schema.Attribute{
			"id":           schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: "The workspace ID."},
			"namespace":    schema.StringAttribute{Required: true, Description: "Vault namespace, 3 to 32 lowercase letters, digits or hyphens. It can't change after enabling."},
			"max_versions": schema.Int64Attribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}, Description: "Default number of versions kept per secret. The API default applies when omitted; it can't change after enabling."},
			"state":        schema.StringAttribute{Computed: true, Description: "Secret Manager state, such as active."},
			"endpoint":     schema.StringAttribute{Computed: true, Description: "Vault API address for secret values, when the service reports one."},
		},
	}
}

func (r *secretManagerResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResourceClient(req, resp)
}

func (r *secretManagerResource) read(ctx context.Context, state *secretManagerModel) (bool, error) {
	var out secretManagerWorkspaceAPI
	if err := r.client.do(ctx, http.MethodGet, "/secret-manager/workspace", nil, &out); err != nil {
		if IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	state.ID = types.StringValue(out.WorkspaceID)
	state.Namespace = types.StringValue(out.Namespace)
	state.MaxVersions = types.Int64Value(out.MaxVersions)
	state.State = types.StringValue(out.State)
	state.Endpoint = types.StringPointerValue(out.Endpoint)
	return true, nil
}

func (r *secretManagerResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan secretManagerModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"namespace": plan.Namespace.ValueString()}
	if !plan.MaxVersions.IsNull() && !plan.MaxVersions.IsUnknown() {
		body["max_versions"] = plan.MaxVersions.ValueInt64()
	}
	var op map[string]any
	if err := r.client.doH(ctx, http.MethodPost, "/secret-manager/workspace/enable", map[string]string{"Idempotency-Key": idempotencyKey()}, body, &op); err != nil {
		resp.Diagnostics.AddError("Failed to enable Secret Manager", err.Error()+" If Secret Manager is already enabled for this workspace, import it with `terraform import`.")
		return
	}
	if id := findString(op, "id"); id != "" {
		if err := r.client.waitSecretManagerOperation(ctx, id); err != nil {
			resp.Diagnostics.AddError("Secret Manager was not enabled", err.Error())
			return
		}
	}
	found, err := r.read(ctx, &plan)
	if err != nil || !found {
		resp.Diagnostics.AddError("Failed to read Secret Manager after enabling", fmt.Sprint(err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *secretManagerResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state secretManagerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	found, err := r.read(ctx, &state)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read Secret Manager", err.Error())
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *secretManagerResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state secretManagerModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if plan.Namespace.ValueString() != state.Namespace.ValueString() ||
		(!plan.MaxVersions.IsUnknown() && plan.MaxVersions.ValueInt64() != state.MaxVersions.ValueInt64()) {
		resp.Diagnostics.AddError("Secret Manager can't be changed after enabling", "Enabling is permanent; the namespace and default version limit stay as enabled.")
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *secretManagerResource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning("Secret Manager stays enabled", "Enabling Secret Manager is permanent. The resource is removed from Terraform state only; the workspace's secrets and app credentials are untouched.")
}

func (r *secretManagerResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// ---------------------------------------------------------------- ibee_secret_manager_secret

// secretTypeKey is where Secret Manager keeps a secret's type among its tags.
const secretTypeKey = "ibee_type"

// serviceTagKeys are the tags the service writes itself: never user tags.
var serviceTagKeys = map[string]bool{secretTypeKey: true, "managed_by": true}

// reservedTagKeys refuses the service's own keys in custom_metadata.
type reservedTagKeys struct{}

func (reservedTagKeys) Description(context.Context) string {
	return "custom_metadata may not set ibee_type or managed_by"
}

func (v reservedTagKeys) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (reservedTagKeys) ValidateMap(_ context.Context, req validator.MapRequest, resp *validator.MapResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	for key := range req.ConfigValue.Elements() {
		if serviceTagKeys[key] {
			resp.Diagnostics.AddAttributeError(req.Path, "Reserved tag",
				fmt.Sprintf("%s is kept by Secret Manager itself. Set the secret type with the type attribute; managed_by is set by the service.", key))
		}
	}
}

type secretManagerSecretResource struct{ client *Client }

func NewSecretManagerSecretResource() resource.Resource { return &secretManagerSecretResource{} }

type secretManagerSecretModel struct {
	ID                 types.String `tfsdk:"id"`
	Path               types.String `tfsdk:"path"`
	Type               types.String `tfsdk:"type"`
	CustomMetadata     types.Map    `tfsdk:"custom_metadata"`
	MaxVersions        types.Int64  `tfsdk:"max_versions"`
	CasRequired        types.Bool   `tfsdk:"cas_required"`
	DeleteVersionAfter types.String `tfsdk:"delete_version_after"`
	CurrentVersion     types.Int64  `tfsdk:"current_version"`
}

type secretManagerMetadataAPI struct {
	CustomMetadata     map[string]string `json:"custom_metadata"`
	MaxVersions        *int64            `json:"max_versions"`
	CasRequired        *bool             `json:"cas_required"`
	DeleteVersionAfter *string           `json:"delete_version_after"`
	CurrentVersion     *int64            `json:"current_version"`
}

func (r *secretManagerSecretResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_secret_manager_secret"
}

func (r *secretManagerSecretResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A Secret Manager secret's metadata: its path, owner tags and version settings. **The value is never managed here**: write and read values at the workspace's Vault API (for example `bao kv put`), so they never enter Terraform state. Create and update write the metadata (PUT); destroy deletes the secret with every version, permanently. Import with the secret path, such as apps/billing/db.",
		Attributes: map[string]schema.Attribute{
			"id":                   schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, Description: "The secret path."},
			"path":                 schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, Description: "Secret path, such as apps/billing/db: 1 to 20 segments of letters, digits, `_`, `.` and `-`."},
			"type":                 schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, Description: "Secret type, such as key_value, api_key or database_credentials (the Secret Manager secret types list). Fixed once created: the API refuses a change. The service default (key_value) applies when omitted."},
			"custom_metadata":      schema.MapAttribute{Optional: true, ElementType: types.StringType, Validators: []validator.Map{reservedTagKeys{}}, Description: "Free-form tags such as owner or team. No secret values. The service's own keys (ibee_type, managed_by) are not tags: use type."},
			"max_versions":         schema.Int64Attribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}, Description: "Versions kept for this secret. The service default applies when omitted."},
			"cas_required":         schema.BoolAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}, Description: "Whether every write must pass the current version (check-and-set)."},
			"delete_version_after": schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, Description: "Duration after which versions are deleted, such as 720h. The service default applies when omitted."},
			"current_version":      schema.Int64Attribute{Computed: true, Description: "Latest version number; 0 before the first value is written."},
		},
	}
}

func (r *secretManagerSecretResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResourceClient(req, resp)
}

func (r *secretManagerSecretResource) write(ctx context.Context, plan *secretManagerSecretModel) error {
	// The whole tag set is always sent, so removed tags go; the service keeps the
	// type it has when none is sent, and adds its own managed_by key.
	tags := map[string]string{}
	if !plan.CustomMetadata.IsNull() && !plan.CustomMetadata.IsUnknown() {
		for key, value := range plan.CustomMetadata.Elements() {
			if s, ok := value.(types.String); ok {
				tags[key] = s.ValueString()
			}
		}
	}
	if !plan.Type.IsNull() && !plan.Type.IsUnknown() {
		tags[secretTypeKey] = plan.Type.ValueString()
	}
	body := map[string]any{"custom_metadata": tags}
	if !plan.MaxVersions.IsNull() && !plan.MaxVersions.IsUnknown() {
		body["max_versions"] = plan.MaxVersions.ValueInt64()
	}
	if !plan.CasRequired.IsNull() && !plan.CasRequired.IsUnknown() {
		body["cas_required"] = plan.CasRequired.ValueBool()
	}
	if !plan.DeleteVersionAfter.IsNull() && !plan.DeleteVersionAfter.IsUnknown() {
		body["delete_version_after"] = plan.DeleteVersionAfter.ValueString()
	}
	var out map[string]any
	return r.client.do(ctx, http.MethodPut, secretManagerPath("secret-metadata", plan.Path.ValueString()), body, &out)
}

func (r *secretManagerSecretResource) read(ctx context.Context, state *secretManagerSecretModel) (bool, error) {
	var out secretManagerMetadataAPI
	if err := r.client.do(ctx, http.MethodGet, secretManagerPath("secret-metadata", state.Path.ValueString()), nil, &out); err != nil {
		if IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	state.ID = types.StringValue(state.Path.ValueString())
	state.Type = types.StringNull()
	if kind, ok := out.CustomMetadata[secretTypeKey]; ok {
		state.Type = types.StringValue(kind)
	}
	userTags := map[string]string{}
	for key, value := range out.CustomMetadata {
		if !serviceTagKeys[key] {
			userTags[key] = value
		}
	}
	if len(userTags) == 0 && state.CustomMetadata.IsNull() {
		// No tags configured and none stored: keep null rather than an empty map.
	} else {
		tags, diags := types.MapValueFrom(ctx, types.StringType, userTags)
		if diags.HasError() {
			return false, fmt.Errorf("read custom_metadata")
		}
		state.CustomMetadata = tags
	}
	state.MaxVersions = types.Int64PointerValue(out.MaxVersions)
	state.CasRequired = types.BoolPointerValue(out.CasRequired)
	state.DeleteVersionAfter = types.StringPointerValue(out.DeleteVersionAfter)
	state.CurrentVersion = types.Int64PointerValue(out.CurrentVersion)
	return true, nil
}

func (r *secretManagerSecretResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan secretManagerSecretModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.write(ctx, &plan); err != nil {
		resp.Diagnostics.AddError("Failed to create secret metadata", err.Error())
		return
	}
	if found, err := r.read(ctx, &plan); err != nil || !found {
		resp.Diagnostics.AddError("Failed to read secret metadata after create", fmt.Sprint(err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *secretManagerSecretResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state secretManagerSecretModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	found, err := r.read(ctx, &state)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read secret metadata", err.Error())
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *secretManagerSecretResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan secretManagerSecretModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.write(ctx, &plan); err != nil {
		resp.Diagnostics.AddError("Failed to update secret metadata", err.Error())
		return
	}
	if found, err := r.read(ctx, &plan); err != nil || !found {
		resp.Diagnostics.AddError("Failed to read secret metadata after update", fmt.Sprint(err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *secretManagerSecretResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state secretManagerSecretModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out map[string]any
	if err := r.client.do(ctx, http.MethodDelete, secretManagerPath("secret-metadata", state.Path.ValueString()), nil, &out); err != nil && !IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to delete secret", err.Error())
	}
}

func (r *secretManagerSecretResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("path"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// ---------------------------------------------------------------- ibee_secret_manager_app_credential

type secretManagerAppCredentialResource struct{ client *Client }

func NewSecretManagerAppCredentialResource() resource.Resource {
	return &secretManagerAppCredentialResource{}
}

type secretManagerAppCredentialModel struct {
	ID        types.String `tfsdk:"id"`
	Name      types.String `tfsdk:"name"`
	Access    types.String `tfsdk:"access"`
	Folder    types.String `tfsdk:"folder"`
	State     types.String `tfsdk:"state"`
	RoleID    types.String `tfsdk:"role_id"`
	SecretID  types.String `tfsdk:"secret_id"`
	Namespace types.String `tfsdk:"namespace"`
	Mount     types.String `tfsdk:"mount"`
	Endpoint  types.String `tfsdk:"endpoint"`
}

type secretManagerCredentialAPI struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Access string `json:"access"`
	Folder string `json:"folder"`
	State  string `json:"state"`
}

func (r *secretManagerAppCredentialResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_secret_manager_app_credential"
}

func (r *secretManagerAppCredentialResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		Description: "A Secret Manager app credential (AppRole) that an application uses to sign in to the workspace's Vault API. Create waits until it is active, then issues a secret ID once and keeps it in state with the role ID (`secret_id` is sensitive but not encrypted by the provider; use an encrypted state backend). Every input change replaces the credential; to rotate the secret ID, replace the resource. Reads never return the secret ID; imports leave it null. Destroy revokes the credential: new sign-ins stop at once. Import with the credential ID.",
		Attributes: map[string]schema.Attribute{
			"id":        schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: "Credential ID."},
			"name":      schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Credential name, unique in the workspace."},
			"access":    schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()}, Description: "read or read_write. The API default applies when omitted."},
			"folder":    schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()}, Description: "Folder the credential may use, such as apps/billing. Omit for the whole namespace."},
			"state":     schema.StringAttribute{Computed: true, Description: "Credential state, such as active."},
			"role_id":   schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: "AppRole role ID."},
			"secret_id": schema.StringAttribute{Computed: true, Sensitive: true, PlanModifiers: keep, Description: "AppRole secret ID, issued once at creation and stored in state. Null after import."},
			"namespace": schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: "Vault namespace to sign in to (X-Vault-Namespace)."},
			"mount":     schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: "AppRole auth mount, such as approle."},
			"endpoint":  schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: "Vault API address, when the service reports one."},
		},
	}
}

func (r *secretManagerAppCredentialResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResourceClient(req, resp)
}

func (r *secretManagerAppCredentialResource) credentialPath(id string) string {
	return "/secret-manager/app-credentials/" + url.PathEscape(id)
}

// get reads the credential; found is false when it is gone or revoked.
func (r *secretManagerAppCredentialResource) get(ctx context.Context, id string) (secretManagerCredentialAPI, bool, error) {
	var out secretManagerCredentialAPI
	if err := r.client.do(ctx, http.MethodGet, r.credentialPath(id), nil, &out); err != nil {
		if IsNotFound(err) {
			return out, false, nil
		}
		return out, false, err
	}
	return out, out.State != "revoked" && out.State != "revoking", nil
}

func (r *secretManagerAppCredentialResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan secretManagerAppCredentialModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"name": plan.Name.ValueString()}
	if !plan.Access.IsNull() && !plan.Access.IsUnknown() {
		body["access"] = plan.Access.ValueString()
	}
	if !plan.Folder.IsNull() && !plan.Folder.IsUnknown() {
		body["folder"] = plan.Folder.ValueString()
	}
	var accepted map[string]any
	if err := r.client.doH(ctx, http.MethodPost, "/secret-manager/app-credentials", map[string]string{"Idempotency-Key": idempotencyKey()}, body, &accepted); err != nil {
		resp.Diagnostics.AddError("Failed to create app credential", err.Error())
		return
	}
	// The answer is {"credential": {"id": ...}, "operation": {...}}.
	id := ""
	if credential, ok := accepted["credential"].(map[string]any); ok {
		id, _ = credential["id"].(string)
	}
	if id == "" {
		id = findString(accepted, "credential_id")
	}
	if id == "" {
		resp.Diagnostics.AddError("Invalid created app credential response", "The response carried no credential id.")
		return
	}
	// Wait until the credential is active, then issue its secret ID once.
	waitCtx, cancel := context.WithTimeout(ctx, secretManagerTimeout)
	defer cancel()
	var credential secretManagerCredentialAPI
	for {
		out, _, err := r.get(waitCtx, id)
		if err != nil && !IsNotFound(err) {
			resp.Diagnostics.AddError("Failed to read app credential", err.Error())
			return
		}
		credential = out
		if out.State == "active" {
			break
		}
		if out.State == "failed" || out.State == "revoked" || out.State == "revoking" {
			resp.Diagnostics.AddError("App credential was not created", "The credential ended in state "+out.State+".")
			return
		}
		if err := waitContext(waitCtx, r.client.pollDelay()); err != nil {
			resp.Diagnostics.AddError("App credential did not become active", err.Error())
			return
		}
	}
	var issued struct {
		RoleID    string  `json:"role_id"`
		SecretID  string  `json:"secret_id"`
		Namespace string  `json:"namespace"`
		Mount     string  `json:"mount"`
		Endpoint  *string `json:"endpoint"`
	}
	if err := r.client.do(ctx, http.MethodPost, r.credentialPath(id)+"/secret-id", nil, &issued); err != nil {
		resp.Diagnostics.AddError("Failed to issue the app credential's secret ID", err.Error())
		return
	}
	plan.ID = types.StringValue(id)
	plan.Name = types.StringValue(credential.Name)
	plan.Access = types.StringValue(credential.Access)
	plan.Folder = types.StringValue(credential.Folder)
	plan.State = types.StringValue(credential.State)
	plan.RoleID = types.StringValue(issued.RoleID)
	plan.SecretID = types.StringValue(issued.SecretID)
	plan.Namespace = types.StringValue(issued.Namespace)
	plan.Mount = types.StringValue(issued.Mount)
	plan.Endpoint = types.StringPointerValue(issued.Endpoint)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *secretManagerAppCredentialResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state secretManagerAppCredentialModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, found, err := r.get(ctx, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to read app credential", err.Error())
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}
	state.Name = types.StringValue(out.Name)
	state.Access = types.StringValue(out.Access)
	state.Folder = types.StringValue(out.Folder)
	state.State = types.StringValue(out.State)
	for _, value := range []*types.String{&state.RoleID, &state.SecretID, &state.Namespace, &state.Mount, &state.Endpoint} {
		if value.IsUnknown() {
			*value = types.StringNull()
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *secretManagerAppCredentialResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("App credential changes require replacement", "Every input replaces the credential; replace it to rotate the secret ID.")
}

func (r *secretManagerAppCredentialResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state secretManagerAppCredentialModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out map[string]any
	if err := r.client.do(ctx, http.MethodDelete, r.credentialPath(state.ID.ValueString()), nil, &out); err != nil && !IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to revoke app credential", err.Error())
	}
}

func (r *secretManagerAppCredentialResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	for _, name := range []string{"role_id", "secret_id", "namespace", "mount", "endpoint"} {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(name), types.StringNull())...)
	}
}
