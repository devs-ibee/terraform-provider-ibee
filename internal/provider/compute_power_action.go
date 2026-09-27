package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

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
	resp.Schema = schema.Schema{Description: "Explicit cloud/GPU VM start, stop or reboot. Requires Terraform 1.14 or later. Invoke manually or with an explicit action trigger; ordinary plan/refresh never changes VM power. Waits for operation success and canonical VM state. Does not force power operations.", Attributes: map[string]schema.Attribute{
		"vm_id":           schema.StringAttribute{Required: true},
		"vm_type":         schema.StringAttribute{Required: true, Description: "cloud or gpu."},
		"operation":       schema.StringAttribute{Required: true, Description: "start, stop, or reboot."},
		"idempotency_key": schema.StringAttribute{Optional: true, Description: "Optional unique request key. Reuse only when recovering the same invocation after a lost response; use a new key for a new operation."},
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
		IdempotencyKey types.String `tfsdk:"idempotency_key"`
	}
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	vmID, vmType, operation := m.VmID.ValueString(), m.VmType.ValueString(), m.Operation.ValueString()
	if strings.TrimSpace(vmID) == "" || (vmType != "cloud" && vmType != "gpu") || (operation != "start" && operation != "stop" && operation != "reboot") {
		resp.Diagnostics.AddError("Invalid VM power action", "Use a nonempty vm_id, vm_type cloud or gpu, and operation start, stop, or reboot.")
		return
	}
	if m.IdempotencyKey.IsUnknown() || (!m.IdempotencyKey.IsNull() && strings.TrimSpace(m.IdempotencyKey.ValueString()) == "") {
		resp.Diagnostics.AddError("Invalid idempotency key", "Provide a known, nonempty key or omit it to generate one.")
		return
	}
	endpoint := "/compute/" + vmType + "-vms/" + url.PathEscape(vmID)
	var current cloudVmAPI
	if err := a.client.do(ctx, http.MethodGet, endpoint, nil, &current); err != nil {
		resp.Diagnostics.AddError("Failed to inspect VM", err.Error())
		return
	}
	if current.identifier() != vmID || current.Status == "" {
		resp.Diagnostics.AddError("Invalid VM response", "VM read omitted its status or returned a different identity.")
		return
	}
	if operation != "stop" {
		if err := a.client.requireBillingEligibility(ctx, "", nil); err != nil {
			resp.Diagnostics.AddError("VM power billing eligibility denied", err.Error())
			return
		}
	}
	key := m.IdempotencyKey.ValueString()
	if key == "" {
		key = idempotencyKey()
	}
	var accepted operationAccepted
	if err := a.client.doH(ctx, http.MethodPost, endpoint+"/actions/"+operation, map[string]string{"X-Idempotency-Key": key}, map[string]any{"force": false, "requested_by": "terraform"}, &accepted); err != nil {
		resp.Diagnostics.AddError("VM power request failed", fmt.Sprintf("%v. Request idempotency key: %s", err, key))
		return
	}
	if accepted.OperationID == "" || accepted.VmID != vmID {
		resp.Diagnostics.AddError("Invalid VM power response", "Response omitted operation_id or returned a different VM. Request idempotency key: "+key)
		return
	}
	if resp.SendProgress != nil {
		resp.SendProgress(action.InvokeProgressEvent{Message: "Waiting for VM power operation " + accepted.OperationID + "."})
	}
	ctx, cancel := context.WithTimeout(ctx, a.client.computeTimeout())
	defer cancel()
	if err := a.client.waitOperation(ctx, accepted.OperationID, 0); err != nil {
		resp.Diagnostics.AddError("VM power operation did not complete", err.Error())
		return
	}
	wanted := "running"
	if operation == "stop" {
		wanted = "stopped"
	}
	for {
		var vm cloudVmAPI
		err := a.client.do(ctx, http.MethodGet, endpoint, nil, &vm)
		if err != nil && !retryableComputeRead(err) {
			resp.Diagnostics.AddError("Failed to confirm VM power state", err.Error())
			return
		}
		if err == nil {
			if vm.identifier() != vmID || vm.Status == "" {
				resp.Diagnostics.AddError("Invalid VM response", "VM read omitted its status or returned a different identity.")
				return
			}
			status := strings.ToLower(vm.Status)
			if status == wanted {
				if resp.SendProgress != nil {
					resp.SendProgress(action.InvokeProgressEvent{Message: "VM is " + wanted + "."})
				}
				return
			}
			switch status {
			case "running", "stopped", "starting", "stopping", "rebooting":
			default:
				resp.Diagnostics.AddError("Unexpected VM power state", fmt.Sprintf("Operation %s succeeded but VM reports %q.", accepted.OperationID, vm.Status))
				return
			}
		}
		if err := a.client.computePoll(ctx); err != nil {
			resp.Diagnostics.AddError("VM power state not confirmed", fmt.Sprintf("Operation %s completed but VM did not reach %s: %v", accepted.OperationID, wanted, err))
			return
		}
	}
}
