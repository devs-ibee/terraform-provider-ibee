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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type blockVolumeResource struct{ client *Client }

func NewBlockVolumeResource() resource.Resource { return &blockVolumeResource{} }

var _ resource.ResourceWithImportState = (*blockVolumeResource)(nil)

type blockVolumeModel struct {
	ID                types.String `tfsdk:"id"`
	Name              types.String `tfsdk:"name"`
	SiteID            types.String `tfsdk:"site_id"`
	SKUCode           types.String `tfsdk:"sku_code"`
	SizeGb            types.Int64  `tfsdk:"size_gb"`
	VolumeClass       types.String `tfsdk:"volume_class"`
	VmType            types.String `tfsdk:"vm_type"`
	AllowOnlineResize types.Bool   `tfsdk:"allow_online_resize"`
	ReplicaCount      types.Int64  `tfsdk:"replica_count"`
	State             types.String `tfsdk:"state"`
	VolumeName        types.String `tfsdk:"volume_name"`
	BillingCurrency   types.String `tfsdk:"billing_currency"`
}
type blockVolumeAPI struct {
	ID           string                      `json:"id"`
	Name         string                      `json:"name"`
	SiteID       *string                     `json:"site_id"`
	SizeGb       int64                       `json:"size_gb"`
	VolumeClass  string                      `json:"volume_class"`
	VolumeKind   string                      `json:"volume_kind"`
	VmType       string                      `json:"vm_type"`
	ReplicaCount int64                       `json:"replica_count"`
	State        string                      `json:"state"`
	VolumeName   string                      `json:"volume_name"`
	AttachedVmID *string                     `json:"attached_vm_id"`
	Attachments  *[]blockVolumeAttachmentAPI `json:"attachments"`
	Metadata     struct {
		BillingCatalog struct {
			SKUCode  string `json:"sku_code"`
			Currency string `json:"currency"`
		} `json:"billing_catalog"`
	} `json:"metadata"`
}
type blockVolumeAttachmentAPI struct {
	NodeName   string  `json:"node_name"`
	VmID       *string `json:"vm_id"`
	Mode       string  `json:"mode"`
	DevicePath string  `json:"device_path"`
}
type blockVolumeOperationAPI struct {
	ID        string `json:"id"`
	VolumeID  string `json:"volume_id"`
	Operation string `json:"operation"`
	Status    string `json:"status"`
	Error     string `json:"error"`
}
type blockVolumeActionAPI struct {
	Volume    blockVolumeAPI          `json:"volume"`
	Operation blockVolumeOperationAPI `json:"operation"`
}

func (r *blockVolumeResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_block_volume"
}
func (r *blockVolumeResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{Description: "A standalone block-storage volume. Size increases resize the existing volume; decreases replace it. Destroy refuses attached volumes and never forces detachment. SKU admission uses the server catalog without a client-invented price. The current public block-storage catalog is INR-only, so purchases require an INR organization. Import using the volume ID.", Attributes: map[string]schema.Attribute{
		"id":       schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"name":     schema.StringAttribute{Required: true, PlanModifiers: replace, Validators: []validator.String{computeNonEmpty()}},
		"site_id":  schema.StringAttribute{Required: true, PlanModifiers: replace, Validators: []validator.String{computeNonEmpty()}},
		"sku_code": schema.StringAttribute{Required: true, PlanModifiers: replace, Validators: []validator.String{computeNonEmpty()}, Description: "Active block-storage SKU from the IBEE catalog. The API validates the SKU and allowed size and derives authoritative billing fields."},
		"size_gb": schema.Int64Attribute{Required: true, Validators: []validator.Int64{computeIntValidator{1, 10000}}, PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplaceIf(func(_ context.Context, req planmodifier.Int64Request, resp *int64planmodifier.RequiresReplaceIfFuncResponse) {
			if !req.StateValue.IsNull() && !req.StateValue.IsUnknown() && !req.PlanValue.IsUnknown() {
				resp.RequiresReplace = req.PlanValue.ValueInt64() < req.StateValue.ValueInt64()
			}
		}, "Shrinking replaces the volume.", "Shrinking replaces the volume.")}},
		"volume_class":        schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("balanced"), PlanModifiers: replace, Validators: []validator.String{computeOneOf("capacity", "balanced", "performance")}},
		"vm_type":             schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("cloud"), PlanModifiers: replace, Validators: []validator.String{computeOneOf("cloud", "gpu")}, Description: "Compute storage backend compatibility."},
		"allow_online_resize": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "Pass permission for an online expansion to the service. Does not bypass backend resize restrictions."},
		"replica_count":       schema.Int64Attribute{Computed: true}, "state": schema.StringAttribute{Computed: true}, "volume_name": schema.StringAttribute{Computed: true}, "billing_currency": schema.StringAttribute{Computed: true},
	}}
}
func (r *blockVolumeResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
func blockVolumePath(id string) string {
	if id == "" {
		return "/block-storage/volumes"
	}
	return "/block-storage/volumes/" + url.PathEscape(id)
}
func (r *blockVolumeResource) get(ctx context.Context, id string) (blockVolumeAPI, error) {
	var v blockVolumeAPI
	err := r.client.do(ctx, http.MethodGet, blockVolumePath(id), nil, &v)
	if err == nil && v.ID != id {
		return v, fmt.Errorf("block-volume response identity does not match request")
	}
	return v, err
}
func (m *blockVolumeModel) hydrate(v blockVolumeAPI) error {
	if v.ID == "" || v.Name == "" || v.SiteID == nil || *v.SiteID == "" || v.SizeGb < 1 || v.ReplicaCount < 1 || v.VolumeClass == "" || v.VmType == "" || v.State == "" || v.VolumeName == "" || v.Metadata.BillingCatalog.SKUCode == "" || v.Metadata.BillingCatalog.Currency == "" || v.Attachments == nil {
		return fmt.Errorf("incomplete block-volume response; canonical placement, size, SKU, currency and attachment fields are required for safe refresh/import")
	}
	if v.VolumeKind != "product" {
		return fmt.Errorf("volume is not a standalone product volume; VM root disks must be managed by their VM")
	}
	m.ID = types.StringValue(v.ID)
	m.Name = types.StringValue(v.Name)
	m.SiteID = types.StringPointerValue(v.SiteID)
	m.SKUCode = types.StringValue(v.Metadata.BillingCatalog.SKUCode)
	m.SizeGb = types.Int64Value(v.SizeGb)
	m.VolumeClass = types.StringValue(v.VolumeClass)
	m.VmType = types.StringValue(v.VmType)
	m.ReplicaCount = types.Int64Value(v.ReplicaCount)
	m.State = types.StringValue(v.State)
	m.VolumeName = types.StringValue(v.VolumeName)
	m.BillingCurrency = types.StringValue(v.Metadata.BillingCatalog.Currency)
	if m.AllowOnlineResize.IsNull() || m.AllowOnlineResize.IsUnknown() {
		m.AllowOnlineResize = types.BoolValue(false)
	}
	return nil
}
func (r *blockVolumeResource) waitOperation(ctx context.Context, volumeID string, op blockVolumeOperationAPI) error {
	if op.ID == "" || op.VolumeID != volumeID {
		return fmt.Errorf("block operation omitted identity or referenced a different volume")
	}
	for {
		switch op.Status {
		case "succeeded":
			return nil
		case "failed":
			return fmt.Errorf("block-volume operation %s failed: %s", op.ID, op.Error)
		case "in-progress":
		default:
			return fmt.Errorf("block-volume operation %s returned unsupported status %q", op.ID, op.Status)
		}
		if err := r.client.computePoll(ctx); err != nil {
			return fmt.Errorf("block-volume operation %s did not complete: %w", op.ID, err)
		}
		var operations *[]blockVolumeOperationAPI
		err := r.client.do(ctx, http.MethodGet, blockVolumePath(volumeID)+"/operations", nil, &operations)
		if err != nil {
			if retryableComputeRead(err) {
				continue
			}
			return err
		}
		if operations == nil {
			return fmt.Errorf("block-volume operations response omitted the operation list")
		}
		found := false
		for _, candidate := range *operations {
			if candidate.ID == op.ID {
				if candidate.VolumeID != volumeID {
					return fmt.Errorf("block operation changed volume identity")
				}
				op = candidate
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("block-volume operation %s disappeared", op.ID)
		}
	}
}
func (r *blockVolumeResource) ready(ctx context.Context, m *blockVolumeModel) error {
	for {
		v, err := r.get(ctx, m.ID.ValueString())
		if err != nil {
			if retryableComputeRead(err) {
				if err := r.client.computePoll(ctx); err != nil {
					return err
				}
				continue
			}
			return err
		}
		if err := m.hydrate(v); err != nil {
			return err
		}
		switch v.State {
		case "ready", "in-use":
			return nil
		case "creating", "attaching", "detaching", "resizing":
		case "error", "deleting":
			return fmt.Errorf("volume %s reached state %q", v.ID, v.State)
		default:
			return fmt.Errorf("unsupported block-volume state %q", v.State)
		}
		if err := r.client.computePoll(ctx); err != nil {
			return err
		}
	}
}
func (r *blockVolumeResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m blockVolumeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.requireBillingEligibilityForCurrency(ctx, m.SKUCode.ValueString(), nil, "INR"); err != nil {
		resp.Diagnostics.AddError("Block-volume billing eligibility denied", err.Error())
		return
	}
	key := idempotencyKey()
	body := map[string]any{"name": m.Name.ValueString(), "site_id": m.SiteID.ValueString(), "sku_code": m.SKUCode.ValueString(), "size_gb": m.SizeGb.ValueInt64(), "volume_class": m.VolumeClass.ValueString(), "volume_kind": "product", "vm_type": m.VmType.ValueString(), "delete_on_termination": false, "idempotency_key": key}
	var accepted blockVolumeActionAPI
	if err := r.client.doH(ctx, http.MethodPost, blockVolumePath(""), map[string]string{"X-Idempotency-Key": key}, body, &accepted); err != nil {
		resp.Diagnostics.AddError("Failed to create block volume", err.Error())
		return
	}
	if accepted.Volume.ID == "" {
		resp.Diagnostics.AddError("Invalid block-volume create response", "No volume ID was returned. Inspect the portal before retrying.")
		return
	}
	m.ID = types.StringValue(accepted.Volume.ID)
	m.State = types.StringValue(accepted.Volume.State)
	m.VolumeName = types.StringValue(accepted.Volume.VolumeName)
	m.ReplicaCount = types.Int64Value(accepted.Volume.ReplicaCount)
	m.BillingCurrency = types.StringValue("INR")
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, r.client.computeTimeout())
	defer cancel()
	if err := r.waitOperation(ctx, m.ID.ValueString(), accepted.Operation); err != nil {
		resp.Diagnostics.AddError("Block-volume creation did not complete", err.Error())
		return
	}
	if err := r.ready(ctx, &m); err != nil {
		resp.Diagnostics.AddError("Failed to refresh created block volume", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *blockVolumeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m blockVolumeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := r.get(ctx, m.ID.ValueString())
	if IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read block volume", err.Error())
		return
	}
	if err := m.hydrate(v); err != nil {
		resp.Diagnostics.AddError("Incomplete block-volume response", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *blockVolumeResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m, old blockVolumeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if m.SizeGb.ValueInt64() < old.SizeGb.ValueInt64() {
		resp.Diagnostics.AddError("Block volume cannot shrink", "A smaller size requires replacement.")
		return
	}
	ctx, cancel := context.WithTimeout(ctx, r.client.computeTimeout())
	defer cancel()
	if m.SizeGb.ValueInt64() > old.SizeGb.ValueInt64() {
		if err := r.client.requireBillingEligibilityForCurrency(ctx, m.SKUCode.ValueString(), nil, "INR"); err != nil {
			resp.Diagnostics.AddError("Block-volume resize billing eligibility denied", err.Error())
			return
		}
		key := idempotencyKey()
		var accepted blockVolumeActionAPI
		err := r.client.doH(ctx, http.MethodPost, blockVolumePath(m.ID.ValueString())+"/resize", map[string]string{"X-Idempotency-Key": key}, map[string]any{"new_size_gb": m.SizeGb.ValueInt64(), "allow_online": m.AllowOnlineResize.ValueBool(), "idempotency_key": key}, &accepted)
		if err != nil {
			resp.Diagnostics.AddError("Failed to resize block volume", err.Error())
			return
		}
		if accepted.Volume.ID != m.ID.ValueString() {
			resp.Diagnostics.AddError("Invalid block-volume resize response", "Response referred to a different volume; existing state remains tracked.")
			return
		}
		if err := r.waitOperation(ctx, m.ID.ValueString(), accepted.Operation); err != nil {
			resp.Diagnostics.AddError("Block-volume resize did not complete", err.Error())
			return
		}
	}
	if err := r.ready(ctx, &m); err != nil {
		resp.Diagnostics.AddError("Failed to refresh resized block volume", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *blockVolumeResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m blockVolumeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := r.get(ctx, m.ID.ValueString())
	if IsNotFound(err) {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to inspect block volume", err.Error())
		return
	}
	if v.Attachments == nil {
		resp.Diagnostics.AddError("Unknown block-volume attachments", "API omitted attachments; refusing deletion.")
		return
	}
	if len(*v.Attachments) > 0 || (v.AttachedVmID != nil && strings.TrimSpace(*v.AttachedVmID) != "") {
		resp.Diagnostics.AddError("Block volume is attached", "Detach the volume from its VM before destroying it. Terraform never forces detachment.")
		return
	}
	key := idempotencyKey()
	var deleted struct {
		Status string `json:"status"`
		ID     string `json:"id"`
	}
	err = r.client.doH(ctx, http.MethodDelete, blockVolumePath(m.ID.ValueString())+"?"+url.Values{"force": {"false"}, "idempotency_key": {key}}.Encode(), map[string]string{"X-Idempotency-Key": key}, nil, &deleted)
	if IsNotFound(err) {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to delete block volume", err.Error())
		return
	}
	if deleted.Status != "deleted" || deleted.ID != m.ID.ValueString() {
		resp.Diagnostics.AddError("Block-volume deletion unconfirmed", "Expected deleted status and matching volume ID.")
		return
	}
	ctx, cancel := context.WithTimeout(ctx, r.client.computeTimeout())
	defer cancel()
	for {
		_, err = r.get(ctx, m.ID.ValueString())
		if IsNotFound(err) {
			return
		}
		if err != nil && !retryableComputeRead(err) {
			resp.Diagnostics.AddError("Failed to confirm block-volume deletion", err.Error())
			return
		}
		if err := r.client.computePoll(ctx); err != nil {
			resp.Diagnostics.AddError("Block-volume deletion timed out", err.Error())
			return
		}
	}
}
func (r *blockVolumeResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
