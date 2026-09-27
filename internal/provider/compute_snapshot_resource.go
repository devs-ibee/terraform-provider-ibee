package provider

import (
	"context"
	"fmt"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"net/http"
	"net/url"
)

type vmSnapshotResource struct {
	client *Client
	vmType string
}

func NewCloudVmSnapshotResource() resource.Resource { return &vmSnapshotResource{vmType: "cloud"} }
func NewGpuVmSnapshotResource() resource.Resource   { return &vmSnapshotResource{vmType: "gpu"} }

var _ resource.ResourceWithImportState = (*vmSnapshotResource)(nil)
var _ resource.ResourceWithValidateConfig = (*vmSnapshotResource)(nil)

type vmSnapshotModel struct {
	ID                    types.String `tfsdk:"id"`
	VmID                  types.String `tfsdk:"vm_id"`
	Name                  types.String `tfsdk:"name"`
	Description           types.String `tfsdk:"description"`
	Mode                  types.String `tfsdk:"mode"`
	SelectedDataVolumeIDs types.Set    `tfsdk:"selected_data_volume_ids"`
	Status                types.String `tfsdk:"status"`
	RecoveryPointID       types.String `tfsdk:"recovery_point_id"`
}
type vmSnapshotAPI struct {
	ID              string  `json:"snapshot_set_id"`
	VmID            string  `json:"vm_id"`
	Name            string  `json:"name"`
	Description     *string `json:"description"`
	Mode            string  `json:"capture_scope"`
	Status          string  `json:"status"`
	RecoveryPointID string  `json:"recovery_point_id"`
	VolumeManifest  []struct {
		SourceVolumeID string `json:"source_volume_id"`
		Role           string `json:"role"`
	} `json:"volume_manifest"`
}

func (r *vmSnapshotResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.vmType + "_vm_snapshot"
}
func (r *vmSnapshotResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{Description: "An immutable VM snapshot. Creation waits for capture completion. Deletion removes this recovery point; restoring is an explicit operation outside this resource. Billing eligibility checks account admission; no public snapshot price quote is available.", Attributes: map[string]schema.Attribute{
		"id":    schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"vm_id": schema.StringAttribute{Required: true, PlanModifiers: replace, Validators: []validator.String{computeNonEmpty()}}, "name": schema.StringAttribute{Required: true, PlanModifiers: replace, Validators: []validator.String{computeNonEmpty()}},
		"description":              schema.StringAttribute{Optional: true, PlanModifiers: replace},
		"mode":                     schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("all_attached"), PlanModifiers: replace, Validators: []validator.String{computeOneOf("root_only", "all_attached", "selective")}},
		"selected_data_volume_ids": schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: setdefault.StaticValue(types.SetValueMust(types.StringType, []attr.Value{})), PlanModifiers: []planmodifier.Set{setplanmodifier.RequiresReplace()}, Description: "Required for selective mode; otherwise must be empty."},
		"status":                   schema.StringAttribute{Computed: true}, "recovery_point_id": schema.StringAttribute{Computed: true},
	}}
}
func (r *vmSnapshotResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *Client, got %T", req.ProviderData))
		return
	}
	r.client = c
}
func (r *vmSnapshotResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m vmSnapshotModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || m.Mode.IsUnknown() || m.SelectedDataVolumeIDs.IsUnknown() {
		return
	}
	n := len(m.SelectedDataVolumeIDs.Elements())
	if m.Mode.ValueString() == "selective" && n == 0 {
		resp.Diagnostics.AddAttributeError(path.Root("selected_data_volume_ids"), "Missing volumes", "selective mode requires at least one data volume ID.")
	}
	if m.Mode.ValueString() != "selective" && n > 0 {
		resp.Diagnostics.AddAttributeError(path.Root("selected_data_volume_ids"), "Unexpected volumes", "Set mode to selective when selecting data volume IDs.")
	}
}
func (r *vmSnapshotResource) route(id string) string {
	return "/compute/" + r.vmType + "-vm-snapshots/" + url.PathEscape(id)
}
func (r *vmSnapshotResource) hydrate(ctx context.Context, m *vmSnapshotModel, s vmSnapshotAPI) error {
	if s.ID == "" || s.VmID == "" || s.Name == "" || s.Status == "" || s.Mode == "" {
		return fmt.Errorf("incomplete snapshot response")
	}
	m.ID = types.StringValue(s.ID)
	m.VmID = types.StringValue(s.VmID)
	m.Name = types.StringValue(s.Name)
	m.Description = types.StringPointerValue(s.Description)
	m.Mode = types.StringValue(s.Mode)
	m.Status = types.StringValue(s.Status)
	m.RecoveryPointID = types.StringValue(s.RecoveryPointID)
	selected := []string{}
	if s.Mode == "selective" {
		for _, v := range s.VolumeManifest {
			if v.Role == "data" {
				selected = append(selected, v.SourceVolumeID)
			}
		}
	}
	if s.Status == "succeeded" && s.Mode == "selective" && len(selected) == 0 {
		return fmt.Errorf("completed selective snapshot omitted its selected data-volume manifest")
	}
	// A queued snapshot may not yet have its volume manifest. Preserve requested IDs until capture finishes.
	if s.Mode != "selective" || len(selected) > 0 || m.SelectedDataVolumeIDs.IsNull() || m.SelectedDataVolumeIDs.IsUnknown() {
		values, d := types.SetValueFrom(ctx, types.StringType, selected)
		if d.HasError() {
			return fmt.Errorf("invalid snapshot volumes: %v", d)
		}
		m.SelectedDataVolumeIDs = values
	}
	return nil
}
func (r *vmSnapshotResource) refresh(ctx context.Context, m *vmSnapshotModel) error {
	var s vmSnapshotAPI
	if err := r.client.do(ctx, http.MethodGet, r.route(m.ID.ValueString()), nil, &s); err != nil {
		return err
	}
	if s.ID != m.ID.ValueString() {
		return fmt.Errorf("snapshot response ID does not match request")
	}
	return r.hydrate(ctx, m, s)
}
func (r *vmSnapshotResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m vmSnapshotModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var volumes []string
	resp.Diagnostics.Append(m.SelectedDataVolumeIDs.ElementsAs(ctx, &volumes, false)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.requireBillingEligibility(ctx, "", nil); err != nil {
		resp.Diagnostics.AddError("Snapshot billing eligibility denied", err.Error())
		return
	}
	body := map[string]any{"name": m.Name.ValueString(), "mode": m.Mode.ValueString(), "selected_data_volume_ids": volumes, "requested_by": "terraform"}
	if !m.Description.IsNull() {
		body["description"] = m.Description.ValueString()
	}
	var snapshot vmSnapshotAPI
	if err := r.client.do(ctx, http.MethodPost, "/compute/"+r.vmType+"-vms/"+url.PathEscape(m.VmID.ValueString())+"/snapshots", body, &snapshot); err != nil {
		resp.Diagnostics.AddError("Failed to create snapshot", err.Error())
		return
	}
	if snapshot.ID == "" {
		resp.Diagnostics.AddError("Invalid snapshot response", "API returned no snapshot_set_id; check the portal before retrying.")
		return
	}
	// Retain the identity even when response validation or asynchronous capture fails.
	m.ID = types.StringValue(snapshot.ID)
	m.Status = types.StringValue(snapshot.Status)
	m.RecoveryPointID = types.StringValue(snapshot.RecoveryPointID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, r.client.computeTimeout())
	defer cancel()
	for {
		if err := r.refresh(ctx, &m); err != nil {
			if retryableComputeRead(err) {
				if err := r.client.computePoll(ctx); err == nil {
					continue
				}
			}
			resp.Diagnostics.AddError("Failed to refresh snapshot", err.Error())
			return
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
		if resp.Diagnostics.HasError() {
			return
		}
		switch m.Status.ValueString() {
		case "succeeded":
			return
		case "failed", "cancelled":
			resp.Diagnostics.AddError("Snapshot capture did not complete", "Snapshot "+m.ID.ValueString()+" is "+m.Status.ValueString())
			return
		case "queued", "running":
		default:
			resp.Diagnostics.AddError("Invalid snapshot status", m.Status.ValueString())
			return
		}
		if err := r.client.computePoll(ctx); err != nil {
			resp.Diagnostics.AddError("Snapshot capture timed out", err.Error())
			return
		}
	}
}
func (r *vmSnapshotResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m vmSnapshotModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.refresh(ctx, &m); err != nil {
		if IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read snapshot", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *vmSnapshotResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Snapshot replacement required", "Snapshot configuration is immutable.")
}
func (r *vmSnapshotResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m vmSnapshotModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out struct {
		Status     string `json:"status"`
		SnapshotID string `json:"snapshot_set_id"`
	}
	err := r.client.do(ctx, http.MethodDelete, r.route(m.ID.ValueString()), nil, &out)
	if IsNotFound(err) {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to delete snapshot", err.Error())
		return
	}
	if out.Status != "deleted" || out.SnapshotID != m.ID.ValueString() {
		resp.Diagnostics.AddError("Snapshot deletion unconfirmed", "Expected deleted status and matching snapshot identity; resource remains tracked.")
	}
}
func (r *vmSnapshotResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
