package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type blockVolumeAttachmentResource struct{ client *Client }

func NewBlockVolumeAttachmentResource() resource.Resource { return &blockVolumeAttachmentResource{} }

var _ resource.ResourceWithImportState = (*blockVolumeAttachmentResource)(nil)

type blockVolumeAttachmentModel struct {
	ID               types.String `tfsdk:"id"`
	VolumeID         types.String `tfsdk:"volume_id"`
	NodeName         types.String `tfsdk:"node_name"`
	VmID             types.String `tfsdk:"vm_id"`
	VmType           types.String `tfsdk:"vm_type"`
	Mode             types.String `tfsdk:"mode"`
	DevicePath       types.String `tfsdk:"device_path"`
	ConfirmUnmounted types.Bool   `tfsdk:"confirm_unmounted"`
}

func (r *blockVolumeAttachmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_block_volume_attachment"
}
func (r *blockVolumeAttachmentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{Description: "Attaches a standalone block volume to a canonical storage node. This storage operation exposes a device on the node; it does not hot-plug or mount a VM guest. For VM guest integration use the cloud/GPU VM volume attachment resource. Do not manage one attachment through both APIs. Destroy requires explicit unmount acknowledgement and never forces detach. Import using VOLUME_ID/NODE_NAME.", Attributes: map[string]schema.Attribute{
		"id":                schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"volume_id":         schema.StringAttribute{Required: true, PlanModifiers: replace, Validators: []validator.String{computeNonEmpty()}},
		"node_name":         schema.StringAttribute{Required: true, PlanModifiers: replace, Validators: []validator.String{computeNonEmpty()}, Description: "Canonical storage node name; aliases may be normalized by the backend and must not be used."},
		"vm_id":             schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplaceIfConfigured()}, Validators: []validator.String{computeNonEmpty()}, Description: "Optional VM association metadata. Does not attach a disk to a guest by itself."},
		"vm_type":           schema.StringAttribute{Computed: true, Description: "Cloud/GPU compatibility read from the volume."},
		"mode":              schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("single-writer"), PlanModifiers: replace, Validators: []validator.String{computeOneOf("single-writer", "multi-writer")}},
		"device_path":       schema.StringAttribute{Computed: true},
		"confirm_unmounted": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "Set true only after the volume is unmounted and no longer used. Required before destruction; force is always false."},
	}}
}
func (r *blockVolumeAttachmentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
func (r *blockVolumeAttachmentResource) refresh(ctx context.Context, m *blockVolumeAttachmentModel) (bool, error) {
	v, err := (&blockVolumeResource{client: r.client}).get(ctx, m.VolumeID.ValueString())
	if err != nil {
		return false, err
	}
	if v.VolumeKind != "product" {
		return false, fmt.Errorf("only standalone product volumes can be attached; VM root disks are not supported")
	}
	if v.Attachments == nil || v.VmType == "" {
		return false, fmt.Errorf("volume omitted its attachment list or compute compatibility; safe attachment refresh is impossible")
	}
	var match *blockVolumeAttachmentAPI
	for i := range *v.Attachments {
		a := &(*v.Attachments)[i]
		if a.NodeName == "" || a.Mode == "" {
			return false, fmt.Errorf("volume returned an incomplete attachment identity")
		}
		if a.NodeName == m.NodeName.ValueString() {
			if match != nil {
				return false, fmt.Errorf("volume returned duplicate attachment identities")
			}
			match = a
		}
	}
	m.VmType = types.StringValue(v.VmType)
	if match == nil {
		return false, nil
	}
	if match.DevicePath == "" {
		return false, fmt.Errorf("attachment omitted its node device path")
	}
	m.ID = types.StringValue(m.VolumeID.ValueString() + "/" + match.NodeName)
	m.Mode = types.StringValue(match.Mode)
	m.VmID = types.StringPointerValue(match.VmID)
	m.DevicePath = types.StringValue(match.DevicePath)
	if m.ConfirmUnmounted.IsNull() || m.ConfirmUnmounted.IsUnknown() {
		m.ConfirmUnmounted = types.BoolValue(false)
	}
	return true, nil
}
func (r *blockVolumeAttachmentResource) wait(ctx context.Context, m *blockVolumeAttachmentModel, want bool) error {
	for {
		found, err := r.refresh(ctx, m)
		if !want && IsNotFound(err) {
			return nil
		}
		if err == nil && found == want {
			return nil
		}
		if err != nil && !retryableComputeRead(err) {
			return err
		}
		if err := r.client.computePoll(ctx); err != nil {
			return fmt.Errorf("attachment did not reach requested state: %w", err)
		}
	}
}
func (r *blockVolumeAttachmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m blockVolumeAttachmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	probe := m
	found, err := r.refresh(ctx, &probe)
	if err != nil {
		resp.Diagnostics.AddError("Failed to inspect block volume", err.Error())
		return
	}
	if found {
		resp.Diagnostics.AddError("Block volume is already attached", "Import using "+m.VolumeID.ValueString()+"/"+m.NodeName.ValueString()+" before managing this attachment.")
		return
	}
	if err := r.client.requireBillingEligibility(ctx, "", nil); err != nil {
		resp.Diagnostics.AddError("Block attachment billing eligibility denied", err.Error())
		return
	}
	key := idempotencyKey()
	body := map[string]any{"node_name": m.NodeName.ValueString(), "mode": m.Mode.ValueString(), "vm_type": probe.VmType.ValueString(), "idempotency_key": key}
	if !m.VmID.IsNull() && !m.VmID.IsUnknown() {
		body["vm_id"] = m.VmID.ValueString()
	}
	var accepted blockVolumeActionAPI
	if err := r.client.doH(ctx, http.MethodPost, blockVolumePath(m.VolumeID.ValueString())+"/attachments", map[string]string{"X-Idempotency-Key": key}, body, &accepted); err != nil {
		resp.Diagnostics.AddError("Failed to attach block volume", err.Error())
		return
	}
	m.ID = types.StringValue(m.VolumeID.ValueString() + "/" + m.NodeName.ValueString())
	m.VmType = probe.VmType
	m.DevicePath = types.StringNull()
	if m.VmID.IsUnknown() {
		m.VmID = types.StringNull()
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if accepted.Volume.ID != m.VolumeID.ValueString() {
		resp.Diagnostics.AddError("Invalid block attachment response", "Response volume identity does not match request; attachment identity is retained for recovery.")
		return
	}
	ctx, cancel := context.WithTimeout(ctx, r.client.computeTimeout())
	defer cancel()
	if err := (&blockVolumeResource{client: r.client}).waitOperation(ctx, m.VolumeID.ValueString(), accepted.Operation); err != nil {
		resp.Diagnostics.AddError("Block attachment did not complete", err.Error())
		return
	}
	if err := r.wait(ctx, &m, true); err != nil {
		resp.Diagnostics.AddError("Failed to confirm block attachment", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *blockVolumeAttachmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m blockVolumeAttachmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	found, err := r.refresh(ctx, &m)
	if IsNotFound(err) || (err == nil && !found) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read block attachment", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *blockVolumeAttachmentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m blockVolumeAttachmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	found, err := r.refresh(ctx, &m)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read block attachment", err.Error())
		return
	}
	if !found {
		resp.Diagnostics.AddError("Block attachment disappeared", "Refresh and apply to restore the attachment.")
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *blockVolumeAttachmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m blockVolumeAttachmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	probe := m
	found, err := r.refresh(ctx, &probe)
	if IsNotFound(err) || (err == nil && !found) {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to inspect block attachment", err.Error())
		return
	}
	if !m.VmID.Equal(probe.VmID) {
		resp.Diagnostics.AddError("Block attachment ownership changed", "The node attachment now refers to another VM. Refresh and inspect its ownership before detaching.")
		return
	}
	if !m.ConfirmUnmounted.ValueBool() {
		resp.Diagnostics.AddError("Unmount confirmation required", "Unmount the volume, set confirm_unmounted = true, and apply that setting before destroying the attachment.")
		return
	}
	key := idempotencyKey()
	body := map[string]any{"node_name": m.NodeName.ValueString(), "vm_type": probe.VmType.ValueString(), "force": false, "confirm_unmounted": true, "idempotency_key": key}
	var accepted blockVolumeActionAPI
	if err := r.client.doH(ctx, http.MethodPost, blockVolumePath(m.VolumeID.ValueString())+"/detach", map[string]string{"X-Idempotency-Key": key}, body, &accepted); err != nil {
		if IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to detach block volume", err.Error())
		return
	}
	if accepted.Volume.ID != m.VolumeID.ValueString() {
		resp.Diagnostics.AddError("Invalid block detach response", "Response volume identity does not match request.")
		return
	}
	ctx, cancel := context.WithTimeout(ctx, r.client.computeTimeout())
	defer cancel()
	if err := (&blockVolumeResource{client: r.client}).waitOperation(ctx, m.VolumeID.ValueString(), accepted.Operation); err != nil {
		resp.Diagnostics.AddError("Block detach did not complete", err.Error())
		return
	}
	if err := r.wait(ctx, &m, false); err != nil {
		resp.Diagnostics.AddError("Failed to confirm block detach", err.Error())
	}
}
func (r *blockVolumeAttachmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError("Invalid import ID", "Expected VOLUME_ID/NODE_NAME.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("volume_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("node_name"), parts[1])...)
}
