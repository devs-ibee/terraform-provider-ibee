package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type vmPowerAction struct{ client *Client }

func NewVmPowerAction() action.Action { return &vmPowerAction{} }
func (a *vmPowerAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm_power"
}
func (a *vmPowerAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "Starts, stops or reboots a cloud or GPU VM (POST .../actions/{operation}) and waits for the operation to finish. Requires Terraform 1.14 or later. Invoke manually or with an explicit action trigger; plan and refresh never change VM power. The API decides whether the VM can take the operation.", Attributes: map[string]schema.Attribute{
		"vm_id":           schema.StringAttribute{Required: true},
		"vm_type":         schema.StringAttribute{Required: true, Description: "cloud or gpu."},
		"operation":       schema.StringAttribute{Required: true, Description: "start, stop or reboot."},
		"force":           schema.BoolAttribute{Optional: true, Description: "Sent as force. Omit to use the API's default."},
		"idempotency_key": schema.StringAttribute{Optional: true, Description: "X-Idempotency-Key for the request. A new key is generated when omitted."},
	}}
}
func (a *vmPowerAction) Configure(_ context.Context, req action.ConfigureRequest, resp *action.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	var ok bool
	a.client, ok = req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *Client, got %T", req.ProviderData))
	}
}
func (a *vmPowerAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m struct {
		VmID           types.String `tfsdk:"vm_id"`
		VmType         types.String `tfsdk:"vm_type"`
		Operation      types.String `tfsdk:"operation"`
		Force          types.Bool   `tfsdk:"force"`
		IdempotencyKey types.String `tfsdk:"idempotency_key"`
	}
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	key := m.IdempotencyKey.ValueString()
	if key == "" {
		key = idempotencyKey()
	}
	body := map[string]any{}
	if known(m.Force) {
		body["force"] = m.Force.ValueBool()
	}
	var accepted operationAccepted
	path := "/compute/" + m.VmType.ValueString() + "-vms/" + url.PathEscape(m.VmID.ValueString()) + "/actions/" + m.Operation.ValueString()
	if err := a.client.doH(ctx, http.MethodPost, path, map[string]string{"X-Idempotency-Key": key}, body, &accepted); err != nil {
		resp.Diagnostics.AddError("VM power request failed", fmt.Sprintf("%v. Request idempotency key: %s", err, key))
		return
	}
	if accepted.OperationID == "" {
		resp.Diagnostics.AddError("Invalid VM power response", "The API accepted the request but returned no operation_id. Request idempotency key: "+key)
		return
	}
	if resp.SendProgress != nil {
		resp.SendProgress(action.InvokeProgressEvent{Message: "Waiting for VM power operation " + accepted.OperationID + "."})
	}
	if err := a.client.waitOperation(ctx, accepted.OperationID, 0); err != nil {
		resp.Diagnostics.AddError("VM power operation did not complete", err.Error())
	}
}
