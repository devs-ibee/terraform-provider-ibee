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
	_ resource.ResourceWithConfigure   = (*containerRegistryResource)(nil)
	_ resource.ResourceWithImportState = (*containerRegistryResource)(nil)
)

func containerRegistryPath(id string) string {
	return "/container-registry/registries/" + url.PathEscape(id)
}

type containerRegistryResource struct{ client *Client }

func NewContainerRegistryResource() resource.Resource { return &containerRegistryResource{} }

type containerRegistryModel struct {
	ID                      types.String `tfsdk:"id"`
	Name                    types.String `tfsdk:"name"`
	SKUCode                 types.String `tfsdk:"sku_code"`
	Visibility              types.String `tfsdk:"visibility"`
	AcknowledgePublicEgress types.Bool   `tfsdk:"acknowledge_public_egress_charges"`
	VulnerabilityPullPolicy types.String `tfsdk:"vulnerability_pull_policy"`
	ObservedState           types.String `tfsdk:"observed_state"`
}

type containerRegistryAPI struct {
	RegistryID   string `json:"registry_id"`
	RegistryName string `json:"registry_name"`
	Visibility   string `json:"visibility"`
	Security     struct {
		VulnerabilityPullPolicy string `json:"vulnerability_pull_policy"`
	} `json:"security"`
	ObservedState      string  `json:"observed_state"`
	Generation         int64   `json:"generation"`
	ObservedGeneration int64   `json:"observed_generation"`
	DeletedAt          *string `json:"deleted_at"`
}

func (out containerRegistryAPI) deleted() bool {
	return (out.DeletedAt != nil && *out.DeletedAt != "") || out.ObservedState == "DELETED"
}

func (out containerRegistryAPI) settled() bool {
	return out.ObservedState == "ACTIVE" && out.ObservedGeneration >= out.Generation
}

func (r *containerRegistryResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container_registry"
}

func (r *containerRegistryResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		Description: "A Container Registry registry. Images live at registry.ibee.ai/<name>/<repository>:<tag>. The registry service decides names, plans (it asks Billing), visibility and states, and the provider relays its answer. Create waits until observed_state is ACTIVE. visibility and vulnerability_pull_policy change in place, and a new sku_code is sent as a plan upgrade (the API accepts larger plans only). Destroy deletes the registry, which must be empty, and waits until the API reports it deleted; the name is never reused. Import with the registry ID.",
		Attributes: map[string]schema.Attribute{
			"id":                                schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: "Registry ID."},
			"name":                              schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, Description: "Globally unique registry name. It cannot be changed, so a change replaces the registry."},
			"sku_code":                          schema.StringAttribute{Required: true, Description: "Storage plan SKU, for example REGISTRY-STORAGE-250GB. A change is sent as a plan upgrade with an Idempotency-Key; the API refuses smaller plans and a second pending change."},
			"visibility":                        schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keep, Description: "PRIVATE or PUBLIC. The API default (PRIVATE) applies when omitted. Changes in place."},
			"acknowledge_public_egress_charges": schema.BoolAttribute{Optional: true, Description: "Sent with create and with a visibility change. The API requires true for PUBLIC, because anonymous pull bandwidth is billed to the workspace."},
			"vulnerability_pull_policy":         schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keep, Description: "ALLOW, BLOCK_CRITICAL, BLOCK_HIGH, BLOCK_MEDIUM or BLOCK_LOW. The API default applies when omitted. Changes in place."},
			"observed_state":                    schema.StringAttribute{Computed: true, Description: "Provisioning state the API reports, for example ACTIVE."},
		},
	}
}

func (r *containerRegistryResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureSecretResource(req, resp)
}

func (out containerRegistryAPI) apply(state *containerRegistryModel) error {
	if out.RegistryID == "" || out.RegistryName == "" || out.Visibility == "" || out.ObservedState == "" {
		return fmt.Errorf("registry response is missing registry_id, registry_name, visibility or observed_state; state was preserved")
	}
	if !state.ID.IsNull() && !state.ID.IsUnknown() && state.ID.ValueString() != out.RegistryID {
		return fmt.Errorf("registry response identifies a different registry; state was preserved")
	}
	state.ID = types.StringValue(out.RegistryID)
	state.Name = types.StringValue(out.RegistryName)
	state.Visibility = types.StringValue(out.Visibility)
	state.VulnerabilityPullPolicy = types.StringValue(out.Security.VulnerabilityPullPolicy)
	state.ObservedState = types.StringValue(out.ObservedState)
	return nil
}

// waitRegistry polls the registry until it is ACTIVE with every change applied,
// or, with gone, until the API reports it deleted.
func (r *containerRegistryResource) waitRegistry(ctx context.Context, id string, gone bool) (containerRegistryAPI, error) {
	ctx, cancel := context.WithTimeout(ctx, r.client.computeTimeout())
	defer cancel()
	for {
		var out containerRegistryAPI
		err := r.client.do(ctx, http.MethodGet, containerRegistryPath(id), nil, &out)
		switch {
		case err == nil && gone && out.deleted():
			return out, nil
		case err == nil && !gone && out.settled():
			return out, nil
		case err == nil && out.ObservedState == "ERROR":
			return out, fmt.Errorf("the API reports observed_state ERROR for registry %s; fix the cause and retry provisioning (POST .../reconcile, `ibee registries retry`)", id)
		case err == nil && !gone && out.deleted():
			return out, fmt.Errorf("the API reports registry %s as deleted", id)
		case err != nil && gone && IsNotFound(err):
			return out, nil
		case err != nil && !retryableComputeRead(err):
			return out, err
		}
		if err := r.client.computePoll(ctx); err != nil {
			return out, fmt.Errorf("registry %s did not settle (observed_state %q): %w", id, out.ObservedState, err)
		}
	}
}

func (r *containerRegistryResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan containerRegistryModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{
		"registry_name":         plan.Name.ValueString(),
		"confirm_registry_name": plan.Name.ValueString(),
		"billing_catalog":       map[string]any{"sku_code": plan.SKUCode.ValueString()},
	}
	if !plan.Visibility.IsNull() && !plan.Visibility.IsUnknown() {
		body["visibility"] = plan.Visibility.ValueString()
	}
	if !plan.AcknowledgePublicEgress.IsNull() && !plan.AcknowledgePublicEgress.IsUnknown() {
		body["acknowledge_public_egress_charges"] = plan.AcknowledgePublicEgress.ValueBool()
	}
	if !plan.VulnerabilityPullPolicy.IsNull() && !plan.VulnerabilityPullPolicy.IsUnknown() {
		body["security"] = map[string]any{"vulnerability_pull_policy": plan.VulnerabilityPullPolicy.ValueString()}
	}
	var out containerRegistryAPI
	if err := r.client.do(ctx, http.MethodPost, "/container-registry/registries", body, &out); err != nil {
		resp.Diagnostics.AddError("Failed to create container registry", err.Error())
		return
	}
	if err := out.apply(&plan); err != nil {
		resp.Diagnostics.AddError("Invalid created container registry response", err.Error())
		return
	}
	// Keep the ID in state before waiting, so a failed wait can still be destroyed.
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() || out.settled() {
		return
	}
	settled, err := r.waitRegistry(ctx, out.RegistryID, false)
	if err != nil {
		resp.Diagnostics.AddError("Container registry created but not active", err.Error())
		return
	}
	if err := settled.apply(&plan); err != nil {
		resp.Diagnostics.AddError("Invalid container registry response", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *containerRegistryResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state containerRegistryModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out containerRegistryAPI
	if err := r.client.do(ctx, http.MethodGet, containerRegistryPath(state.ID.ValueString()), nil, &out); err != nil {
		if IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read container registry", err.Error())
		return
	}
	if out.deleted() {
		resp.State.RemoveResource(ctx)
		return
	}
	if err := out.apply(&state); err != nil {
		resp.Diagnostics.AddError("Invalid container registry response", err.Error())
		return
	}
	// sku_code is kept as configured; after an import it is read from the plan once.
	if state.SKUCode.IsNull() || state.SKUCode.IsUnknown() {
		var pkg struct {
			Current *struct {
				SKUCode string `json:"sku_code"`
			} `json:"current"`
		}
		if err := r.client.do(ctx, http.MethodGet, containerRegistryPath(out.RegistryID)+"/package", nil, &pkg); err != nil {
			resp.Diagnostics.AddError("Failed to read container registry plan", err.Error())
			return
		}
		if pkg.Current != nil && pkg.Current.SKUCode != "" {
			state.SKUCode = types.StringValue(pkg.Current.SKUCode)
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *containerRegistryResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state containerRegistryModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	changed := false
	if !plan.Visibility.IsUnknown() && !plan.Visibility.IsNull() && !plan.Visibility.Equal(state.Visibility) {
		body := map[string]any{"visibility": plan.Visibility.ValueString()}
		if !plan.AcknowledgePublicEgress.IsNull() && !plan.AcknowledgePublicEgress.IsUnknown() {
			body["acknowledge_public_egress_charges"] = plan.AcknowledgePublicEgress.ValueBool()
		}
		var out containerRegistryAPI
		if err := r.client.do(ctx, http.MethodPatch, containerRegistryPath(id)+"/visibility", body, &out); err != nil {
			resp.Diagnostics.AddError("Failed to change container registry visibility", err.Error())
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			return
		}
		state.Visibility, changed = plan.Visibility, true
	}
	if !plan.VulnerabilityPullPolicy.IsUnknown() && !plan.VulnerabilityPullPolicy.IsNull() && !plan.VulnerabilityPullPolicy.Equal(state.VulnerabilityPullPolicy) {
		body := map[string]any{"vulnerability_pull_policy": plan.VulnerabilityPullPolicy.ValueString()}
		var out struct {
			VulnerabilityPullPolicy string `json:"vulnerability_pull_policy"`
		}
		if err := r.client.do(ctx, http.MethodPatch, containerRegistryPath(id)+"/security/settings", body, &out); err != nil {
			resp.Diagnostics.AddError("Failed to update container registry security settings", err.Error())
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			return
		}
		state.VulnerabilityPullPolicy, changed = plan.VulnerabilityPullPolicy, true
	}
	if !plan.SKUCode.Equal(state.SKUCode) {
		// The upgrade is applied by the registry service; the API answers 202 while it is pending.
		headers := map[string]string{"Idempotency-Key": idempotencyKey()}
		body := map[string]any{"billing_catalog": map[string]any{"sku_code": plan.SKUCode.ValueString()}}
		var out struct {
			OperationID string `json:"operation_id"`
		}
		if err := r.client.doH(ctx, http.MethodPost, containerRegistryPath(id)+"/upgrade", headers, body, &out); err != nil {
			resp.Diagnostics.AddError("Failed to upgrade container registry plan", err.Error())
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			return
		}
		state.SKUCode, changed = plan.SKUCode, true
	}
	state.AcknowledgePublicEgress = plan.AcknowledgePublicEgress
	if changed {
		out, err := r.waitRegistry(ctx, id, false)
		if err != nil {
			resp.Diagnostics.AddError("Container registry change accepted but not applied", err.Error())
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			return
		}
		if err := out.apply(&state); err != nil {
			resp.Diagnostics.AddError("Invalid container registry response", err.Error())
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			return
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *containerRegistryResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state containerRegistryModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	var out containerRegistryAPI
	err := r.client.do(ctx, http.MethodDelete, containerRegistryPath(id)+"?confirm_registry_name="+url.QueryEscape(state.Name.ValueString()), nil, &out)
	if IsNotFound(err) {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to delete container registry", err.Error())
		return
	}
	if out.deleted() {
		return
	}
	if _, err := r.waitRegistry(ctx, id, true); err != nil {
		resp.Diagnostics.AddError("Container registry deletion not confirmed", err.Error())
	}
}

func (r *containerRegistryResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
