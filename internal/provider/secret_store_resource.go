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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                   = (*secretStoreResource)(nil)
	_ resource.ResourceWithConfigure      = (*secretStoreResource)(nil)
	_ resource.ResourceWithImportState    = (*secretStoreResource)(nil)
	_ resource.ResourceWithValidateConfig = (*secretStoreResource)(nil)
)

type secretStoreResource struct{ client *Client }

func NewSecretStoreResource() resource.Resource { return &secretStoreResource{} }

type secretStoreResourceModel struct {
	ID           types.String `tfsdk:"id"`
	Name         types.String `tfsdk:"name"`
	Description  types.String `tfsdk:"description"`
	StoreKey     types.String `tfsdk:"store_key"`
	Status       types.String `tfsdk:"status"`
	ForceArchive types.Bool   `tfsdk:"force_archive"`
}

type secretStoreAPI struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	StoreKey    string `json:"store_key"`
	Status      string `json:"status"`
}

func (r *secretStoreResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_secret_store"
}

func (r *secretStoreResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "An IBEE secret store. Destroy archives the store, blocking access and revoking sessions; it does not permanently erase secrets or free the name. The public API has no unarchive or permanent-delete operation.",
		Attributes: map[string]schema.Attribute{
			"id":            schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"name":          schema.StringAttribute{Required: true, Description: "Store display name, 1–128 characters."},
			"description":   schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("")},
			"store_key":     schema.StringAttribute{Computed: true, Description: "Stable store key assigned by the service."},
			"status":        schema.StringAttribute{Computed: true},
			"force_archive": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "Permit archive with active secrets, which blocks access to all contained secrets. Apply this setting before destroy. By default the provider lists all secrets and refuses to archive if an active secret remains."},
		},
	}
}

func (r *secretStoreResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	var ok bool
	r.client, ok = req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *Client, got %T", req.ProviderData))
	}
}

func (r *secretStoreResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg secretStoreResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !cfg.Name.IsNull() && !cfg.Name.IsUnknown() && (strings.TrimSpace(cfg.Name.ValueString()) == "" || len([]rune(cfg.Name.ValueString())) > 128) {
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Invalid store name", "Store names must contain 1–128 characters.")
	}
}

func secretStorePath(id string) string { return "/secret-store/stores/" + url.PathEscape(id) }

func (out secretStoreAPI) apply(state *secretStoreResourceModel) error {
	if out.ID == "" || out.Name == "" || out.StoreKey == "" || out.Status == "" {
		return fmt.Errorf("secret store response is missing id, name, store_key or status; state was preserved")
	}
	if !state.ID.IsNull() && !state.ID.IsUnknown() && state.ID.ValueString() != out.ID {
		return fmt.Errorf("secret store response identifies a different store; state was preserved")
	}
	state.ID = types.StringValue(out.ID)
	state.Name = types.StringValue(out.Name)
	state.Description = types.StringValue(out.Description)
	state.StoreKey = types.StringValue(out.StoreKey)
	state.Status = types.StringValue(out.Status)
	return nil
}

func (r *secretStoreResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan secretStoreResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// No public secret-manager catalog/quote is available. Server-side pricing,
	// entitlement and quota admission remain authoritative.
	var out secretStoreAPI
	if err := r.client.do(ctx, http.MethodPost, "/secret-store/stores", map[string]any{"name": plan.Name.ValueString(), "description": plan.Description.ValueString()}, &out); err != nil {
		resp.Diagnostics.AddError("Failed to create secret store", err.Error())
		return
	}
	if out.ID != "" {
		plan.ID, plan.StoreKey, plan.Status = types.StringValue(out.ID), types.StringValue(out.StoreKey), types.StringValue(out.Status)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
	if err := out.apply(&plan); err != nil {
		resp.Diagnostics.AddError("Invalid created secret store response", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *secretStoreResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state secretStoreResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out secretStoreAPI
	if err := r.client.do(ctx, http.MethodGet, secretStorePath(state.ID.ValueString()), nil, &out); err != nil {
		if IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read secret store", err.Error())
		return
	}
	if err := out.apply(&state); err != nil {
		resp.Diagnostics.AddError("Invalid secret store response", err.Error())
		return
	}
	if state.ForceArchive.IsNull() {
		state.ForceArchive = types.BoolValue(false)
	}
	if out.Status == "archived" {
		resp.Diagnostics.AddWarning("Secret store is archived", "The record and its reserved name still exist. Restore it through the portal before modifying it; the public API does not offer unarchive.")
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *secretStoreResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state secretStoreResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out secretStoreAPI
	if !plan.Name.Equal(state.Name) || !plan.Description.Equal(state.Description) {
		if err := r.client.do(ctx, http.MethodPatch, secretStorePath(state.ID.ValueString()), map[string]any{"name": plan.Name.ValueString(), "description": plan.Description.ValueString()}, &out); err != nil {
			resp.Diagnostics.AddError("Failed to update secret store", err.Error())
			return
		}
	} else if err := r.client.do(ctx, http.MethodGet, secretStorePath(state.ID.ValueString()), nil, &out); err != nil {
		resp.Diagnostics.AddError("Failed to refresh secret store", err.Error())
		return
	}
	if err := out.apply(&plan); err != nil {
		resp.Diagnostics.AddError("Invalid secret store response", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// List responses include soft-deleted records. Visit every page and fail closed
// for missing totals/records or unfamiliar statuses before archiving a store.
func (r *secretStoreResource) verifyNoActiveSecrets(ctx context.Context, id string) error {
	seen := map[string]bool{}
	for page := 1; ; page++ {
		var out struct {
			Secrets []secretAPI `json:"secrets"`
			Total   *int        `json:"total"`
		}
		if err := r.client.do(ctx, http.MethodGet, fmt.Sprintf("%s/secrets?page=%d&limit=100", secretStorePath(id), page), nil, &out); err != nil {
			return err
		}
		if out.Total == nil || *out.Total < 0 || out.Secrets == nil {
			return fmt.Errorf("secret list is incomplete; cannot verify that no active secrets remain")
		}
		for _, secret := range out.Secrets {
			if secret.ID == "" || seen[secret.ID] {
				return fmt.Errorf("secret list contains missing or repeated identities; cannot safely archive")
			}
			seen[secret.ID] = true
			if !secret.isDeleted() {
				return fmt.Errorf("active or unknown-status secrets remain; delete them first, or explicitly apply force_archive=true to archive all secrets in this store")
			}
		}
		if len(seen) >= *out.Total {
			return nil
		}
		if len(out.Secrets) == 0 {
			return fmt.Errorf("secret list ended before its reported total; cannot safely archive")
		}
	}
}

func (r *secretStoreResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state secretStoreResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out secretStoreAPI
	if err := r.client.do(ctx, http.MethodGet, secretStorePath(state.ID.ValueString()), nil, &out); err != nil {
		if IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Cannot read secret store before archive", err.Error())
		return
	}
	if err := out.apply(&state); err != nil {
		resp.Diagnostics.AddError("Cannot verify secret store before archive", err.Error())
		return
	}
	if out.Status == "archived" {
		return
	}
	if !state.ForceArchive.ValueBool() {
		if err := r.verifyNoActiveSecrets(ctx, state.ID.ValueString()); err != nil {
			resp.Diagnostics.AddError("Secret store archive blocked", err.Error())
			return
		}
	}
	if err := r.client.do(ctx, http.MethodPost, secretStorePath(state.ID.ValueString())+"/archive", nil, &out); err != nil {
		if !IsNotFound(err) {
			resp.Diagnostics.AddError("Failed to archive secret store", err.Error())
		}
		return
	}
	if out.ID != state.ID.ValueString() || out.Status != "archived" {
		resp.Diagnostics.AddError("Secret store archive not confirmed", "The response did not confirm this store is archived. State is preserved for retry.")
	}
}

func (r *secretStoreResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if strings.TrimSpace(req.ID) == "" || strings.ContainsAny(req.ID, "/\\") {
		resp.Diagnostics.AddError("Invalid secret store import ID", "Import with the store ID.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("force_archive"), false)...)
}
