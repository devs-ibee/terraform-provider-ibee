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

// vmAccessAction sends one VM access update: inline SSH keys, a password
// reset or SSH password login. ibee_cloud_vm and ibee_gpu_vm keep managing
// their Secret Store ssh_key_ids in place.
type vmAccessAction struct{ client *Client }

func NewVmAccessAction() action.Action { return &vmAccessAction{} }

func (a *vmAccessAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm_access"
}

func (a *vmAccessAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	attrs := vmActionAttributes()
	attrs["ssh_key_mode"] = schema.StringAttribute{Optional: true, Description: "add or remove, for ssh_keys and ssh_key_ids."}
	attrs["ssh_keys"] = schema.ListAttribute{Optional: true, ElementType: types.StringType, Description: "Inline public SSH keys to add or remove."}
	attrs["ssh_key_ids"] = schema.ListAttribute{Optional: true, ElementType: types.StringType, Description: "Secret Store SSH key IDs to add or remove. For keys an ibee_cloud_vm or ibee_gpu_vm manages, change its ssh_key_ids instead, or the next apply reverts the change."}
	attrs["new_password"] = schema.StringAttribute{Optional: true, Description: "New password for the Linux account. Pass it from a sensitive variable."}
	attrs["password_auth_enabled"] = schema.BoolAttribute{Optional: true, Description: "Turn SSH password login on or off."}
	attrs["confirm_remove_last_ssh_key"] = schema.BoolAttribute{Optional: true, Description: "Confirm a removal that leaves the account without SSH keys."}
	attrs["idempotency_key"] = schema.StringAttribute{Optional: true, Description: "X-Idempotency-Key for the request. A new key is generated when omitted."}
	resp.Schema = schema.Schema{Description: "Updates access on a cloud or GPU VM without rebooting it: adds or removes SSH keys, resets the Linux user password, or turns SSH password login on or off. Requires Terraform 1.14 or later. Invoke manually or with an explicit action trigger. Sends the fields that are set; the API decides whether the change is allowed. Waits for the operation to finish.", Attributes: attrs}
}

func (a *vmAccessAction) Configure(_ context.Context, req action.ConfigureRequest, resp *action.ConfigureResponse) {
	a.client = configureComputeAction(req, resp)
}

func (a *vmAccessAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m struct {
		VmType              types.String `tfsdk:"vm_type"`
		VmID                types.String `tfsdk:"vm_id"`
		SSHKeyMode          types.String `tfsdk:"ssh_key_mode"`
		SSHKeys             types.List   `tfsdk:"ssh_keys"`
		SSHKeyIDs           types.List   `tfsdk:"ssh_key_ids"`
		NewPassword         types.String `tfsdk:"new_password"`
		PasswordAuthEnabled types.Bool   `tfsdk:"password_auth_enabled"`
		ConfirmRemoveLast   types.Bool   `tfsdk:"confirm_remove_last_ssh_key"`
		IdempotencyKey      types.String `tfsdk:"idempotency_key"`
	}
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{}
	for key, value := range map[string]types.String{"ssh_key_mode": m.SSHKeyMode, "new_password": m.NewPassword} {
		if !value.IsNull() && !value.IsUnknown() {
			body[key] = value.ValueString()
		}
	}
	for key, value := range map[string]types.List{"ssh_keys": m.SSHKeys, "ssh_key_ids": m.SSHKeyIDs} {
		if value.IsNull() || value.IsUnknown() {
			continue
		}
		var items []string
		resp.Diagnostics.Append(value.ElementsAs(ctx, &items, false)...)
		body[key] = items
	}
	for key, value := range map[string]types.Bool{"password_auth_enabled": m.PasswordAuthEnabled, "confirm_remove_last_ssh_key": m.ConfirmRemoveLast} {
		if !value.IsNull() && !value.IsUnknown() {
			body[key] = value.ValueBool()
		}
	}
	if resp.Diagnostics.HasError() {
		return
	}
	key := m.IdempotencyKey.ValueString()
	if key == "" {
		key = idempotencyKey()
	}
	var accepted operationAccepted
	if err := a.client.doH(ctx, http.MethodPatch, "/compute/"+m.VmType.ValueString()+"-vms/"+url.PathEscape(m.VmID.ValueString())+"/actions/access", map[string]string{"X-Idempotency-Key": key}, body, &accepted); err != nil {
		resp.Diagnostics.AddError("VM access request failed", fmt.Sprintf("%v. Request idempotency key: %s", err, key))
		return
	}
	if accepted.OperationID == "" {
		resp.Diagnostics.AddError("Invalid VM access response", "The API accepted the change but returned no operation_id. Request idempotency key: "+key)
		return
	}
	if resp.SendProgress != nil {
		resp.SendProgress(action.InvokeProgressEvent{Message: "Waiting for VM access operation " + accepted.OperationID + "."})
	}
	if err := a.client.waitOperation(ctx, accepted.OperationID, 0); err != nil {
		resp.Diagnostics.AddError("VM access operation did not complete", err.Error())
	}
}
