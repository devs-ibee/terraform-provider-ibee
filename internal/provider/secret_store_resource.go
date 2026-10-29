package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = (*secretStoreResource)(nil)
	_ resource.ResourceWithConfigure   = (*secretStoreResource)(nil)
	_ resource.ResourceWithImportState = (*secretStoreResource)(nil)
)

type secretStoreResource struct{ client *Client }

func NewSecretStoreResource() resource.Resource { return &secretStoreResource{} }

type secretStoreResourceModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	StoreKey    types.String `tfsdk:"store_key"`
	Status      types.String `tfsdk:"status"`
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
		Description: "An IBEE secret store. The public API has no operation to change a store, so a change to name or description replaces it. Destroy (and replacement) archives the store, including any secrets it still holds, blocking access and revoking sessions; the API decides whether the archive is allowed. Archiving does not permanently erase secrets or free the name, so a replacement store needs a new name.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"name":        schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, Description: "Store display name. A change replaces the store."},
			"description": schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()}, Description: "A change replaces the store."},
			"store_key":   schema.StringAttribute{Computed: true, Description: "Stable store key assigned by the service."},
			"status":      schema.StringAttribute{Computed: true},
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

func secretStorePath(id string) string { return "/secret-store/stores/" + url.PathEscape(id) }

func (out secretStoreAPI) apply(state *secretStoreResourceModel) error {
	if out.ID == "" {
		return fmt.Errorf("secret store response has no id")
	}
	state.ID = types.StringValue(out.ID)
	if out.Name != "" {
		state.Name = types.StringValue(out.Name)
	}
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
	body := map[string]any{"name": plan.Name.ValueString()}
	if known(plan.Description) {
		body["description"] = plan.Description.ValueString()
	}
	var out secretStoreAPI
	if err := r.client.do(ctx, http.MethodPost, "/secret-store/stores", body, &out); err != nil {
		resp.Diagnostics.AddError("Failed to create secret store", err.Error())
		return
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
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *secretStoreResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state secretStoreResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// name and description replace the store; there is nothing to change in place.
	var out secretStoreAPI
	if err := r.client.do(ctx, http.MethodGet, secretStorePath(state.ID.ValueString()), nil, &out); err != nil {
		resp.Diagnostics.AddError("Failed to refresh secret store", err.Error())
		return
	}
	if err := out.apply(&plan); err != nil {
		resp.Diagnostics.AddError("Invalid secret store response", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete archives the store (POST .../archive); a store already archived is left as it is.
func (r *secretStoreResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state secretStoreResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || state.Status.ValueString() == "archived" {
		return
	}
	if err := r.client.do(ctx, http.MethodPost, secretStorePath(state.ID.ValueString())+"/archive", nil, nil); err != nil && !IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to archive secret store", err.Error())
	}
}

func (r *secretStoreResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
