package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
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
	BillingCatalog    types.String `tfsdk:"billing_catalog"`
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
		BillingCatalog recoveryCatalog `json:"billing_catalog"`
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
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{Description: "A block-storage volume. A size_gb change is sent to the resize operation; the API decides which changes it accepts. Destroy deletes the volume; the API decides whether an attached volume can be deleted. Import using the volume ID.", Attributes: map[string]schema.Attribute{
		"id":                  schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"name":                schema.StringAttribute{Required: true, PlanModifiers: replace},
		"site_id":             schema.StringAttribute{Required: true, PlanModifiers: replace},
		"sku_code":            schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keep, Description: "Block-storage SKU, such as a sku_code from ibee_block_storage_plans. Omit to let the API choose."},
		"size_gb":             schema.Int64Attribute{Required: true, Description: "Volume size in GB. Changes are sent to the resize operation."},
		"volume_class":        schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keep, Description: "capacity, balanced or performance. Omit to use the API's default."},
		"replica_count":       schema.Int64Attribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown(), int64planmodifier.RequiresReplace()}, Description: "Omit to use the API's default."},
		"allow_online_resize": schema.BoolAttribute{Optional: true, Description: "Sent as allow_online with a resize. Omit to use the API's default."},
		"vm_type":             schema.StringAttribute{Computed: true, Description: "Compute compatibility the API reports."},
		"state":               schema.StringAttribute{Computed: true}, "volume_name": schema.StringAttribute{Computed: true}, "billing_currency": schema.StringAttribute{Computed: true},
		"billing_catalog": schema.StringAttribute{Computed: true, Description: "The volume's billing_catalog (from its metadata) as a JSON object. Pass it as billing_catalog of ibee_cloud_vm_volume_attachment or ibee_gpu_vm_volume_attachment."},
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
	return v, err
}
func (m *blockVolumeModel) hydrate(v blockVolumeAPI) error {
	if v.ID == "" {
		return fmt.Errorf("block-volume response has no id")
	}
	m.ID = types.StringValue(v.ID)
	// A field the response omits keeps its recorded value.
	for _, field := range []struct {
		target *types.String
		value  string
	}{{&m.Name, v.Name}, {&m.SKUCode, catalogString(v.Metadata.BillingCatalog, "sku_code")}, {&m.VolumeClass, v.VolumeClass}, {&m.VmType, v.VmType}, {&m.State, v.State}, {&m.VolumeName, v.VolumeName}, {&m.BillingCurrency, catalogString(v.Metadata.BillingCatalog, "currency")}} {
		if field.value != "" {
			*field.target = types.StringValue(field.value)
		} else if field.target.IsUnknown() {
			*field.target = types.StringNull()
		}
	}
	if v.SiteID != nil && *v.SiteID != "" {
		m.SiteID = types.StringValue(*v.SiteID)
	}
	if v.Metadata.BillingCatalog != nil {
		encoded, err := json.Marshal(v.Metadata.BillingCatalog)
		if err != nil {
			return fmt.Errorf("invalid billing_catalog in block-volume response")
		}
		m.BillingCatalog = types.StringValue(string(encoded))
	} else if m.BillingCatalog.IsUnknown() {
		m.BillingCatalog = types.StringNull()
	}
	if v.SizeGb > 0 {
		m.SizeGb = types.Int64Value(v.SizeGb)
	}
	if v.ReplicaCount > 0 {
		m.ReplicaCount = types.Int64Value(v.ReplicaCount)
	} else if m.ReplicaCount.IsUnknown() {
		m.ReplicaCount = types.Int64Null()
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
			return nil
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
	body := map[string]any{"name": m.Name.ValueString(), "site_id": m.SiteID.ValueString(), "size_gb": m.SizeGb.ValueInt64(), "idempotency_key": idempotencyKey()}
	for key, value := range map[string]types.String{"sku_code": m.SKUCode, "volume_class": m.VolumeClass} {
		if !value.IsNull() && !value.IsUnknown() {
			body[key] = value.ValueString()
		}
	}
	if !m.ReplicaCount.IsNull() && !m.ReplicaCount.IsUnknown() {
		body["replica_count"] = m.ReplicaCount.ValueInt64()
	}
	var accepted blockVolumeActionAPI
	if err := r.client.do(ctx, http.MethodPost, blockVolumePath(""), body, &accepted); err != nil {
		resp.Diagnostics.AddError("Failed to create block volume", err.Error())
		return
	}
	if accepted.Volume.ID == "" {
		resp.Diagnostics.AddError("Invalid block-volume create response", "No volume ID was returned. Inspect the portal before retrying.")
		return
	}
	// Save the identity before waiting; the read after the operation fills the rest.
	if err := m.hydrate(accepted.Volume); err != nil {
		resp.Diagnostics.AddError("Invalid block-volume create response", err.Error())
		return
	}
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
	ctx, cancel := context.WithTimeout(ctx, r.client.computeTimeout())
	defer cancel()
	if m.SizeGb.ValueInt64() != old.SizeGb.ValueInt64() {
		body := map[string]any{"new_size_gb": m.SizeGb.ValueInt64(), "idempotency_key": idempotencyKey()}
		if !m.AllowOnlineResize.IsNull() && !m.AllowOnlineResize.IsUnknown() {
			body["allow_online"] = m.AllowOnlineResize.ValueBool()
		}
		var accepted blockVolumeActionAPI
		err := r.client.do(ctx, http.MethodPost, blockVolumePath(m.ID.ValueString())+"/resize", body, &accepted)
		if err != nil {
			resp.Diagnostics.AddError("Failed to resize block volume", err.Error())
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
	err := r.client.do(ctx, http.MethodDelete, blockVolumePath(m.ID.ValueString()), nil, nil)
	if IsNotFound(err) {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to delete block volume", err.Error())
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

// catalogString reads one string field of a billing catalog, or "".
func catalogString(catalog map[string]any, key string) string {
	value, _ := catalog[key].(string)
	return value
}
