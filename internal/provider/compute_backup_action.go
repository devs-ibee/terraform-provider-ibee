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

// Backup creation and cancellation are explicit one-shot actions, not resources.
type vmBackupCreateAction struct{ client *Client }
type vmBackupRestoreCancelAction struct{ client *Client }

func NewVmBackupCreateAction() action.Action        { return &vmBackupCreateAction{} }
func NewVmBackupRestoreCancelAction() action.Action { return &vmBackupRestoreCancelAction{} }

func (a *vmBackupCreateAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm_backup_create"
}

func (a *vmBackupCreateAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	attrs := vmActionAttributes()
	attrs["billing_catalog"] = schema.StringAttribute{Optional: true, Description: "Optional backup billing catalog as a JSON object. Omit to use the enabled backup policy's stored catalog. Passed to the API unchanged."}
	attrs["requested_by"] = schema.StringAttribute{Optional: true, Description: "Optional request label. Omit to use the API default."}
	attrs["reason"] = schema.StringAttribute{Optional: true, Description: "Optional reason for the manual backup."}
	resp.Schema = schema.Schema{Description: "Starts a manual cloud or GPU VM backup and waits for it to finish. Requires an enabled backup policy; the API returns 409 when backups are disabled or another run is queued or running. Requires Terraform 1.14 or later. Invoke explicitly; plan and refresh never start a backup. This action does not store a managed resource.", Attributes: attrs}
}

func (a *vmBackupCreateAction) Configure(_ context.Context, req action.ConfigureRequest, resp *action.ConfigureResponse) {
	a.client = configureComputeAction(req, resp)
}

func (a *vmBackupCreateAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m struct {
		VmType         types.String `tfsdk:"vm_type"`
		VmID           types.String `tfsdk:"vm_id"`
		BillingCatalog types.String `tfsdk:"billing_catalog"`
		RequestedBy    types.String `tfsdk:"requested_by"`
		Reason         types.String `tfsdk:"reason"`
	}
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	catalog, err := recoveryCatalogBody(m.BillingCatalog)
	if err != nil {
		resp.Diagnostics.AddError("Invalid backup billing catalog", err.Error())
		return
	}
	body := map[string]any{}
	if catalog != nil {
		body["billing_catalog"] = catalog
	}
	for name, value := range map[string]types.String{"requested_by": m.RequestedBy, "reason": m.Reason} {
		if known(value) {
			body[name] = value.ValueString()
		}
	}
	var started recoveryStatus
	kind := m.VmType.ValueString()
	requestPath := "/compute/" + kind + "-vms/" + url.PathEscape(m.VmID.ValueString()) + "/backups/runs"
	if err := a.client.do(ctx, http.MethodPost, requestPath, body, &started); err != nil {
		resp.Diagnostics.AddError("Backup request failed", err.Error())
		return
	}
	if started.RunID == "" {
		resp.Diagnostics.AddError("Invalid backup response", "The API accepted the backup but returned no run_id. Check the VM's backup runs before retrying.")
		return
	}
	if resp.SendProgress != nil {
		resp.SendProgress(action.InvokeProgressEvent{Message: "Waiting for backup " + started.RunID + "."})
	}
	if _, err := a.client.waitRecovery(ctx, "/compute/"+kind+"-vm-backups/runs/"+url.PathEscape(started.RunID)); err != nil {
		resp.Diagnostics.AddError("Backup did not succeed", fmt.Sprintf("Backup %s: %v", started.RunID, err))
		return
	}
	if resp.SendProgress != nil {
		resp.SendProgress(action.InvokeProgressEvent{Message: "Backup " + started.RunID + " succeeded."})
	}
}

func (a *vmBackupRestoreCancelAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm_backup_restore_cancel"
}

func (a *vmBackupRestoreCancelAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "Cancels a queued cloud or GPU VM backup restore owned by the configured workspace. The API returns 409 if the restore is already running; this action does not terminate a running restore. Requires Terraform 1.14 or later. Invoke explicitly; plan and refresh never cancel a restore.", Attributes: map[string]schema.Attribute{
		"vm_type":    schema.StringAttribute{Required: true, Description: "cloud or gpu."},
		"restore_id": schema.StringAttribute{Required: true, Description: "Queued backup restore ID."},
	}}
}

func (a *vmBackupRestoreCancelAction) Configure(_ context.Context, req action.ConfigureRequest, resp *action.ConfigureResponse) {
	a.client = configureComputeAction(req, resp)
}

func (a *vmBackupRestoreCancelAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m struct {
		VmType    types.String `tfsdk:"vm_type"`
		RestoreID types.String `tfsdk:"restore_id"`
	}
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	requestPath := "/compute/" + m.VmType.ValueString() + "-vm-backups/restores/" + url.PathEscape(m.RestoreID.ValueString()) + "/actions/cancel"
	var cancelled recoveryStatus
	if err := a.client.do(ctx, http.MethodPost, requestPath, nil, &cancelled); err != nil {
		resp.Diagnostics.AddError("Backup restore cancellation failed", err.Error())
		return
	}
	if cancelled.RestoreID != m.RestoreID.ValueString() || cancelled.Status != "cancelled" {
		resp.Diagnostics.AddError("Invalid cancellation response", "The API did not confirm the requested restore was cancelled. Read its status before retrying.")
		return
	}
	if resp.SendProgress != nil {
		resp.SendProgress(action.InvokeProgressEvent{Message: "Restore " + cancelled.RestoreID + " cancelled."})
	}
}
