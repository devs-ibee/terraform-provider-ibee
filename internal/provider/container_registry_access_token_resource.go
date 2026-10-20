package provider

import (
	"context"
	"net/http"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.ResourceWithConfigure   = (*containerRegistryAccessTokenResource)(nil)
	_ resource.ResourceWithImportState = (*containerRegistryAccessTokenResource)(nil)
)

func containerRegistryTokensPath(registryID string) string {
	return containerRegistryPath(registryID) + "/access-tokens"
}

type containerRegistryAccessTokenResource struct{ client *Client }

func NewContainerRegistryAccessTokenResource() resource.Resource {
	return &containerRegistryAccessTokenResource{}
}

type containerRegistryAccessTokenModel struct {
	ID            types.String `tfsdk:"id"`
	RegistryID    types.String `tfsdk:"registry_id"`
	Name          types.String `tfsdk:"name"`
	Permission    types.String `tfsdk:"permission"`
	ExpiresInDays types.Int64  `tfsdk:"expires_in_days"`
	Description   types.String `tfsdk:"description"`
	RegistryIDs   types.Set    `tfsdk:"registry_ids"`
	Username      types.String `tfsdk:"username"`
	Secret        types.String `tfsdk:"secret"`
	ExpiresAt     types.String `tfsdk:"expires_at"`
}

// containerRegistryTokenAPI covers both answers: create returns username and
// secret, the list returns harbor_username and never a secret.
type containerRegistryTokenAPI struct {
	TokenID        string  `json:"token_id"`
	TokenName      string  `json:"token_name"`
	Username       string  `json:"username"`
	HarborUsername string  `json:"harbor_username"`
	Secret         string  `json:"secret"`
	Permission     string  `json:"permission"`
	ExpiresAt      *string `json:"expires_at"`
	RevokedAt      *string `json:"revoked_at"`
}

func (r *containerRegistryAccessTokenResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container_registry_access_token"
}

func (r *containerRegistryAccessTokenResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		Description: "A Container Registry access token: a docker login credential for registry.ibee.ai. The registry service decides what is allowed (it asks Billing at creation). Every input change replaces the token. The username and secret are returned once, at creation, and kept in state (secret is sensitive but not encrypted by the provider; use an encrypted state backend). Reads list the registry's tokens and never return the secret; imports leave it null. A revoked token, or one the list no longer shows, leaves state. Destroy revokes the token. Import with REGISTRY_ID/TOKEN_ID.",
		Attributes: map[string]schema.Attribute{
			"id":              schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: "Token ID."},
			"registry_id":     schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Registry the token is created for, such as ibee_container_registry.id."},
			"name":            schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Token name, unique among the organization's active tokens."},
			"permission":      schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()}, Description: "PULL or PULL_PUSH. The API default (PULL) applies when omitted."},
			"expires_in_days": schema.Int64Attribute{Optional: true, PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}, Description: "1 to 365, or -1 for a token that does not expire. The API default applies when omitted."},
			"description":     schema.StringAttribute{Optional: true, PlanModifiers: replace, Description: "Description, sent at creation."},
			"registry_ids":    schema.SetAttribute{Optional: true, ElementType: types.StringType, PlanModifiers: []planmodifier.Set{setplanmodifier.RequiresReplace()}, Description: "Other registries in the same workspace the token may also use, sent at creation. The registry_id registry is always included."},
			"username":        schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: "Docker login username. It contains `$`, so quote it in shells."},
			"secret":          schema.StringAttribute{Computed: true, Sensitive: true, PlanModifiers: keep, Description: "Docker login password, returned once at creation and stored in state. Null after import."},
			"expires_at":      schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: "Expiry time the API reports, or null for a token that does not expire."},
		},
	}
}

func (r *containerRegistryAccessTokenResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureSecretResource(req, resp)
}

func (out containerRegistryTokenAPI) apply(state *containerRegistryAccessTokenModel) {
	state.ID = types.StringValue(out.TokenID)
	state.Name = types.StringValue(out.TokenName)
	state.Permission = types.StringValue(out.Permission)
	state.ExpiresAt = types.StringPointerValue(out.ExpiresAt)
	if username := out.Username + out.HarborUsername; username != "" {
		state.Username = types.StringValue(username)
	}
}

func (r *containerRegistryAccessTokenResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan containerRegistryAccessTokenModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"token_name": plan.Name.ValueString()}
	if !plan.Permission.IsNull() && !plan.Permission.IsUnknown() {
		body["permission"] = plan.Permission.ValueString()
	}
	if !plan.ExpiresInDays.IsNull() && !plan.ExpiresInDays.IsUnknown() {
		body["expires_in_days"] = plan.ExpiresInDays.ValueInt64()
	}
	if !plan.Description.IsNull() && !plan.Description.IsUnknown() {
		body["description"] = plan.Description.ValueString()
	}
	if !plan.RegistryIDs.IsNull() && !plan.RegistryIDs.IsUnknown() {
		var ids []string
		resp.Diagnostics.Append(plan.RegistryIDs.ElementsAs(ctx, &ids, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
		body["registry_ids"] = ids
	}
	var out containerRegistryTokenAPI
	if err := r.client.do(ctx, http.MethodPost, containerRegistryTokensPath(plan.RegistryID.ValueString()), body, &out); err != nil {
		resp.Diagnostics.AddError("Failed to create container registry access token", err.Error())
		return
	}
	if out.TokenID == "" {
		resp.Diagnostics.AddError("Invalid created access token response", "The response omitted token_id.")
		return
	}
	plan.Username, plan.Secret = types.StringNull(), types.StringNull()
	out.apply(&plan)
	if out.Secret != "" {
		plan.Secret = types.StringValue(out.Secret)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *containerRegistryAccessTokenResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state containerRegistryAccessTokenModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// The API has no single-token read, so find the token in the registry's list.
	var tokens []containerRegistryTokenAPI
	if err := r.client.do(ctx, http.MethodGet, containerRegistryTokensPath(state.RegistryID.ValueString()), nil, &tokens); err != nil {
		if IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read container registry access tokens", err.Error())
		return
	}
	for _, token := range tokens {
		if token.TokenID != state.ID.ValueString() {
			continue
		}
		if token.RevokedAt != nil && *token.RevokedAt != "" {
			break
		}
		token.apply(&state)
		if state.Secret.IsUnknown() {
			state.Secret = types.StringNull()
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}
	resp.State.RemoveResource(ctx)
}

func (r *containerRegistryAccessTokenResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Access token changes require replacement", "Every input replaces the token; the API cannot update tokens in place.")
}

func (r *containerRegistryAccessTokenResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state containerRegistryAccessTokenModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out struct {
		Detail string `json:"detail"`
	}
	if err := r.client.do(ctx, http.MethodDelete, containerRegistryTokensPath(state.RegistryID.ValueString())+"/"+url.PathEscape(state.ID.ValueString()), nil, &out); err != nil && !IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to revoke container registry access token", err.Error())
	}
}

func (r *containerRegistryAccessTokenResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	registry, id, ok := splitImportID(req.ID, "REGISTRY_ID/TOKEN_ID", resp)
	if !ok {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("registry_id"), registry)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("secret"), types.StringNull())...)
}
