package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Restores and manual backup runs are one-shot operations, so they are
// Terraform actions like ibee_vm_power. Each sends one request, relays the
// API's answer and waits for the restore or run to finish.

func configureComputeAction(req action.ConfigureRequest, resp *action.ConfigureResponse) *Client {
	if req.ProviderData == nil {
		return nil
	}
	client, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *Client, got %T", req.ProviderData))
	}
	return client
}

func vmActionAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"vm_type": schema.StringAttribute{Required: true, Description: "cloud or gpu.", Validators: []validator.String{computeOneOf("cloud", "gpu")}},
		"vm_id":   schema.StringAttribute{Required: true, Description: "VM ID.", Validators: []validator.String{computeNonEmpty()}},
	}
}

// recoveryStatus is the part of a BackupRun or RecoveryRestore the actions read.
type recoveryStatus struct {
	RunID           string  `json:"run_id"`
	RestoreID       string  `json:"restore_id"`
	Status          string  `json:"status"`
	RecoveryPointID *string `json:"recovery_point_id"`
	TargetVmID      *string `json:"target_vm_id"`
	ErrorMessage    *string `json:"error_message"`
}

// waitRecovery polls a backup run or restore until the API reports a final status.
func (c *Client) waitRecovery(ctx context.Context, statusPath string) (recoveryStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, c.computeTimeout())
	defer cancel()
	var last recoveryStatus
	for {
		var current recoveryStatus
		err := c.do(ctx, http.MethodGet, statusPath, nil, &current)
		if err == nil {
			last = current
			switch current.Status {
			case "succeeded":
				return current, nil
			case "failed", "cancelled":
				message := ""
				if current.ErrorMessage != nil {
					message = ": " + *current.ErrorMessage
				}
				return current, fmt.Errorf("the API reports %s%s", current.Status, message)
			case "queued", "running":
			default:
				return current, fmt.Errorf("the API returned unsupported status %q", current.Status)
			}
		} else if !retryableComputeRead(err) {
			return last, err
		}
		if err := c.computePoll(ctx); err != nil {
			return last, fmt.Errorf("not finished (last status %q): %w", last.Status, err)
		}
	}
}

type restoreTargetModel struct {
	VmType           types.String `tfsdk:"vm_type"`
	VmID             types.String `tfsdk:"vm_id"`
	TargetMode       types.String `tfsdk:"target_mode"`
	TargetVmName     types.String `tfsdk:"target_vm_name"`
	TargetPlanID     types.String `tfsdk:"target_plan_id"`
	TargetSiteID     types.String `tfsdk:"target_site_id"`
	TargetCpu        types.Int64  `tfsdk:"target_cpu"`
	TargetRamMb      types.Int64  `tfsdk:"target_ram_mb"`
	TargetDiskGb     types.Int64  `tfsdk:"target_disk_gb"`
	TargetGpuCount   types.Int64  `tfsdk:"target_gpu_count"`
	TargetGpuModel   types.String `tfsdk:"target_gpu_model"`
	SelectedVolumeID types.String `tfsdk:"selected_volume_id"`
	AutoStart        types.Bool   `tfsdk:"auto_start"`
}

// body sends target_mode and every target field that is set; the API decides
// what each mode needs.
func (m restoreTargetModel) body() map[string]any {
	body := map[string]any{"target_mode": m.TargetMode.ValueString(), "requested_by": "terraform"}
	for key, value := range map[string]types.String{"target_vm_name": m.TargetVmName, "target_plan_id": m.TargetPlanID, "target_site_id": m.TargetSiteID, "target_gpu_model": m.TargetGpuModel, "selected_volume_id": m.SelectedVolumeID} {
		if !value.IsNull() && !value.IsUnknown() {
			body[key] = value.ValueString()
		}
	}
	for key, value := range map[string]types.Int64{"target_cpu": m.TargetCpu, "target_ram_mb": m.TargetRamMb, "target_disk_gb": m.TargetDiskGb, "target_gpu_count": m.TargetGpuCount} {
		if !value.IsNull() && !value.IsUnknown() {
			body[key] = value.ValueInt64()
		}
	}
	if !m.AutoStart.IsNull() && !m.AutoStart.IsUnknown() {
		body["auto_start"] = m.AutoStart.ValueBool()
	}
	return body
}

type snapshotRestoreModel struct {
	restoreTargetModel
	SnapshotSetID types.String `tfsdk:"snapshot_set_id"`
}

type backupRestoreModel struct {
	restoreTargetModel
	RecoveryPointID types.String `tfsdk:"recovery_point_id"`
}

type vmRestoreAction struct {
	client *Client
	backup bool
}

func NewVmSnapshotRestoreAction() action.Action { return &vmRestoreAction{} }
func NewVmBackupRestoreAction() action.Action   { return &vmRestoreAction{backup: true} }

func (a *vmRestoreAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm_snapshot_restore"
	if a.backup {
		resp.TypeName = req.ProviderTypeName + "_vm_backup_restore"
	}
}

func (a *vmRestoreAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	attrs := vmActionAttributes()
	attrs["vm_id"] = schema.StringAttribute{Required: true, Description: "ID of the VM the snapshot or backup was taken from.", Validators: []validator.String{computeNonEmpty()}}
	attrs["target_mode"] = schema.StringAttribute{Required: true, Description: "replace (restore over the source VM), new_vm (create a VM from the recovery point) or volume_only (restore one volume, set selected_volume_id).", Validators: []validator.String{computeOneOf("replace", "new_vm", "volume_only")}}
	attrs["target_vm_name"] = schema.StringAttribute{Optional: true, Description: "Name of the new VM (new_vm)."}
	attrs["target_plan_id"] = schema.StringAttribute{Optional: true, Description: "Plan for the new VM (new_vm), from ibee_compute_plans."}
	attrs["target_site_id"] = schema.StringAttribute{Optional: true, Description: "Site for the new VM (new_vm)."}
	attrs["target_cpu"] = schema.Int64Attribute{Optional: true, Description: "vCPUs for the new VM."}
	attrs["target_ram_mb"] = schema.Int64Attribute{Optional: true, Description: "Memory in MB for the new VM."}
	attrs["target_disk_gb"] = schema.Int64Attribute{Optional: true, Description: "Root disk in GB for the new VM."}
	attrs["target_gpu_count"] = schema.Int64Attribute{Optional: true, Description: "GPUs for a new GPU VM."}
	attrs["target_gpu_model"] = schema.StringAttribute{Optional: true, Description: "GPU model for a new GPU VM."}
	attrs["selected_volume_id"] = schema.StringAttribute{Optional: true, Description: "Volume to restore (volume_only)."}
	attrs["auto_start"] = schema.BoolAttribute{Optional: true, Description: "Start the VM after the restore. The API default applies when omitted."}
	description := "Restores a VM snapshot set. Requires Terraform 1.14 or later. Invoke manually or with an explicit action trigger; plan and refresh never restore. Sends the fields that are set and the API decides whether the restore is allowed and what each target mode needs. Waits until the API reports the restore succeeded, failed or was cancelled. A new VM created by new_vm is not managed by Terraform until you import it."
	if a.backup {
		attrs["recovery_point_id"] = schema.StringAttribute{Required: true, Description: "Backup recovery point (backup run) to restore.", Validators: []validator.String{computeNonEmpty()}}
		description = strings.Replace(description, "a VM snapshot set", "a VM backup recovery point", 1)
	} else {
		attrs["snapshot_set_id"] = schema.StringAttribute{Required: true, Description: "Snapshot set to restore, such as ibee_cloud_vm_snapshot.id.", Validators: []validator.String{computeNonEmpty()}}
	}
	resp.Schema = schema.Schema{Description: description, Attributes: attrs}
}

func (a *vmRestoreAction) Configure(_ context.Context, req action.ConfigureRequest, resp *action.ConfigureResponse) {
	a.client = configureComputeAction(req, resp)
}

func (a *vmRestoreAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	if a.backup {
		var m backupRestoreModel
		resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
		if resp.Diagnostics.HasError() {
			return
		}
		kind, body := m.VmType.ValueString(), m.body()
		body["recovery_point_id"] = m.RecoveryPointID.ValueString()
		a.restore(ctx, "/compute/"+kind+"-vms/"+url.PathEscape(m.VmID.ValueString())+"/backups/actions/restore", "/compute/"+kind+"-vm-backups/restores/", body, resp)
		return
	}
	var m snapshotRestoreModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	kind, query := m.VmType.ValueString(), url.Values{"vm_id": {m.VmID.ValueString()}}.Encode()
	a.restore(ctx, "/compute/"+kind+"-vm-snapshots/"+url.PathEscape(m.SnapshotSetID.ValueString())+"/actions/restore?"+query, "/compute/"+kind+"-vm-snapshots/restores/", m.body(), resp)
}

func (a *vmRestoreAction) restore(ctx context.Context, requestPath, statusPath string, body map[string]any, resp *action.InvokeResponse) {
	var started recoveryStatus
	if err := a.client.do(ctx, http.MethodPost, requestPath, body, &started); err != nil {
		resp.Diagnostics.AddError("Restore request failed", err.Error())
		return
	}
	if started.RestoreID == "" {
		resp.Diagnostics.AddError("Invalid restore response", "The API accepted the restore but returned no restore_id. Check the VM in the portal before retrying.")
		return
	}
	if resp.SendProgress != nil {
		resp.SendProgress(action.InvokeProgressEvent{Message: "Waiting for restore " + started.RestoreID + "."})
	}
	done, err := a.client.waitRecovery(ctx, statusPath+url.PathEscape(started.RestoreID))
	if err != nil {
		resp.Diagnostics.AddError("Restore did not succeed", fmt.Sprintf("Restore %s: %v", started.RestoreID, err))
		return
	}
	if resp.SendProgress != nil {
		message := "Restore " + started.RestoreID + " succeeded."
		if done.TargetVmID != nil && *done.TargetVmID != "" {
			message = "Restore " + started.RestoreID + " succeeded on VM " + *done.TargetVmID + "."
		}
		resp.SendProgress(action.InvokeProgressEvent{Message: message})
	}
}

type vmBackupRunAction struct{ client *Client }

func NewVmBackupRunAction() action.Action { return &vmBackupRunAction{} }

func (a *vmBackupRunAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm_backup_run"
}

func (a *vmBackupRunAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	attrs := vmActionAttributes()
	attrs["billing_catalog"] = schema.StringAttribute{Optional: true, Description: "Backup storage billing catalog as a JSON object (use jsonencode), passed to the API unchanged. The current API requires it; the API decides whether it is valid.", Validators: []validator.String{recoveryCatalogValidator{}}}
	attrs["reason"] = schema.StringAttribute{Optional: true, Description: "Optional note stored with the run."}
	resp.Schema = schema.Schema{Description: "Runs a manual backup of a cloud or GPU VM now, using the VM's backup configuration (see ibee_cloud_vm_backup_policy). Requires Terraform 1.14 or later. Invoke manually or with an explicit action trigger; plan and refresh never run a backup. The API decides whether the run is allowed. Waits until the API reports the run succeeded, failed or was cancelled.", Attributes: attrs}
}

func (a *vmBackupRunAction) Configure(_ context.Context, req action.ConfigureRequest, resp *action.ConfigureResponse) {
	a.client = configureComputeAction(req, resp)
}

func (a *vmBackupRunAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m struct {
		VmType         types.String `tfsdk:"vm_type"`
		VmID           types.String `tfsdk:"vm_id"`
		BillingCatalog types.String `tfsdk:"billing_catalog"`
		Reason         types.String `tfsdk:"reason"`
	}
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"requested_by": "terraform"}
	catalog, err := recoveryCatalogBody(m.BillingCatalog)
	if err != nil {
		resp.Diagnostics.AddError("Invalid billing catalog", err.Error())
		return
	}
	if catalog != nil {
		body["billing_catalog"] = catalog
	}
	if !m.Reason.IsNull() && !m.Reason.IsUnknown() {
		body["reason"] = m.Reason.ValueString()
	}
	var run recoveryStatus
	if err := a.client.do(ctx, http.MethodPost, "/compute/"+m.VmType.ValueString()+"-vms/"+url.PathEscape(m.VmID.ValueString())+"/backups/runs", body, &run); err != nil {
		resp.Diagnostics.AddError("Backup run request failed", err.Error())
		return
	}
	if run.RunID == "" {
		resp.Diagnostics.AddError("Invalid backup run response", "The API queued the backup but returned no run_id. Check the VM's backups in the portal before retrying.")
		return
	}
	if resp.SendProgress != nil {
		resp.SendProgress(action.InvokeProgressEvent{Message: "Waiting for backup run " + run.RunID + "."})
	}
	done, err := a.client.waitRecovery(ctx, "/compute/"+m.VmType.ValueString()+"-vm-backups/runs/"+url.PathEscape(run.RunID))
	if err != nil {
		resp.Diagnostics.AddError("Backup run did not succeed", fmt.Sprintf("Backup run %s: %v", run.RunID, err))
		return
	}
	if resp.SendProgress != nil {
		message := "Backup run " + run.RunID + " succeeded."
		if done.RecoveryPointID != nil && *done.RecoveryPointID != "" {
			message = "Backup run " + run.RunID + " succeeded; recovery point " + *done.RecoveryPointID + "."
		}
		resp.SendProgress(action.InvokeProgressEvent{Message: message})
	}
}
