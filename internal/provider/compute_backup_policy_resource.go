package provider

import (
	"context"
	"fmt"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"net/http"
	"net/url"
	"time"
)

type vmBackupPolicyResource struct {
	client *Client
	vmType string
}

func NewCloudVmBackupPolicyResource() resource.Resource {
	return &vmBackupPolicyResource{vmType: "cloud"}
}
func NewGpuVmBackupPolicyResource() resource.Resource { return &vmBackupPolicyResource{vmType: "gpu"} }

var _ resource.ResourceWithImportState = (*vmBackupPolicyResource)(nil)
var _ resource.ResourceWithValidateConfig = (*vmBackupPolicyResource)(nil)

type vmBackupPolicyModel struct {
	ID                     types.String `tfsdk:"id"`
	VmID                   types.String `tfsdk:"vm_id"`
	PolicyID               types.String `tfsdk:"policy_id"`
	Frequency              types.String `tfsdk:"frequency"`
	Timezone               types.String `tfsdk:"timezone"`
	Hour                   types.Int64  `tfsdk:"hour"`
	Minute                 types.Int64  `tfsdk:"minute"`
	DayOfWeek              types.Int64  `tfsdk:"day_of_week"`
	WindowMinutes          types.Int64  `tfsdk:"window_minutes"`
	RetentionDays          types.Int64  `tfsdk:"retention_days"`
	FullBackupIntervalDays types.Int64  `tfsdk:"full_backup_interval_days"`
	IncrementalEnabled     types.Bool   `tfsdk:"incremental_enabled"`
	NextRunAt              types.String `tfsdk:"next_run_at"`
	BillingCatalog         types.String `tfsdk:"billing_catalog"`
}
type vmBackupPolicyAPI struct {
	PolicyID       string          `json:"policy_id"`
	VmID           string          `json:"vm_id"`
	Enabled        *bool           `json:"enabled"`
	BillingCatalog recoveryCatalog `json:"billing_catalog"`
	Schedule       struct {
		Frequency     string `json:"frequency"`
		Timezone      string `json:"timezone"`
		Hour          int64  `json:"hour"`
		Minute        int64  `json:"minute"`
		DayOfWeek     *int64 `json:"day_of_week"`
		WindowMinutes int64  `json:"window_minutes"`
	} `json:"schedule"`
	RetentionDays          int64   `json:"retention_days"`
	FullBackupIntervalDays int64   `json:"full_backup_interval_days"`
	IncrementalEnabled     bool    `json:"incremental_enabled"`
	NextRunAt              *string `json:"next_run_at"`
}

func (r *vmBackupPolicyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.vmType + "_vm_backup_policy"
}
func (r *vmBackupPolicyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	integer := func(value, min, max int64) schema.Int64Attribute {
		return schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(value), Validators: []validator.Int64{computeIntValidator{min, max}}}
	}
	scheduleInteger := func(min, max int64) schema.Int64Attribute {
		return schema.Int64Attribute{Optional: true, Computed: true, Validators: []validator.Int64{computeIntValidator{min, max}}}
	}
	resp.Schema = schema.Schema{Description: "Enables and manages automated VM backups. Destroy disables future backups and retains existing recovery points, which may continue incurring storage charges. Import using the VM ID. Billing checks account admission; the public API has no backup price quote.", Attributes: map[string]schema.Attribute{
		"id": schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, Description: "VM ID, used as the stable identity of its singleton policy."}, "vm_id": schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, Validators: []validator.String{computeNonEmpty()}}, "policy_id": schema.StringAttribute{Computed: true},
		"billing_catalog": recoveryCatalogAttribute("backup_storage", false),
		"frequency":       schema.StringAttribute{Optional: true, Computed: true, Validators: []validator.String{computeOneOf("hourly", "daily", "weekly")}, Description: "New schedules support daily (default) or weekly. An existing hourly schedule is preserved only while unchanged; explicitly select daily/weekly to migrate."}, "timezone": schema.StringAttribute{Optional: true, Computed: true, Description: "Defaults to UTC for new policies; omission preserves an existing timezone."},
		"hour": schema.Int64Attribute{Optional: true, Computed: true, Validators: []validator.Int64{computeIntValidator{0, 23}}, Description: "Hour in the selected timezone. Defaults to 12 for new policies; omission preserves existing/imported hours, including the old default 20."}, "minute": scheduleInteger(0, 59), "window_minutes": scheduleInteger(5, 180), "retention_days": integer(7, 1, 365), "full_backup_interval_days": integer(7, 1, 30),
		"day_of_week": schema.Int64Attribute{Optional: true, Computed: true, Validators: []validator.Int64{computeIntValidator{0, 6}}, Description: "Only for weekly schedules: Monday=0 through Sunday=6. Required for new weekly schedules; omission preserves an existing weekly day."}, "incremental_enabled": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true)}, "next_run_at": schema.StringAttribute{Computed: true},
	}}
}
func (r *vmBackupPolicyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
func (r *vmBackupPolicyResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m vmBackupPolicyModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !m.Timezone.IsUnknown() && !m.Timezone.IsNull() {
		if _, err := time.LoadLocation(m.Timezone.ValueString()); err != nil || m.Timezone.ValueString() == "" || m.Timezone.ValueString() == "Local" || len(m.Timezone.ValueString()) > 128 {
			resp.Diagnostics.AddAttributeError(path.Root("timezone"), "Invalid timezone", "Use a valid IANA timezone, for example UTC or Asia/Kolkata.")
		}
	}
	if !m.Frequency.IsNull() && !m.Frequency.IsUnknown() && m.Frequency.ValueString() != "weekly" && !m.DayOfWeek.IsNull() && !m.DayOfWeek.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("day_of_week"), "Unexpected day", "day_of_week is only used with frequency = weekly.")
	}
}
func (r *vmBackupPolicyResource) route(vmID string, action string) string {
	return "/compute/" + r.vmType + "-vms/" + url.PathEscape(vmID) + "/backups/" + action
}
func (m *vmBackupPolicyModel) body() map[string]any {
	schedule := map[string]any{"frequency": m.Frequency.ValueString(), "timezone": m.Timezone.ValueString(), "hour": m.Hour.ValueInt64(), "minute": m.Minute.ValueInt64(), "window_minutes": m.WindowMinutes.ValueInt64()}
	if !m.DayOfWeek.IsNull() {
		schedule["day_of_week"] = m.DayOfWeek.ValueInt64()
	}
	return map[string]any{"schedule": schedule, "retention_days": m.RetentionDays.ValueInt64(), "full_backup_interval_days": m.FullBackupIntervalDays.ValueInt64(), "incremental_enabled": m.IncrementalEnabled.ValueBool(), "requested_by": "terraform"}
}
func (r *vmBackupPolicyResource) refresh(ctx context.Context, m *vmBackupPolicyModel) (bool, error) {
	id := m.VmID.ValueString()
	if id == "" {
		id = m.ID.ValueString()
	}
	var p vmBackupPolicyAPI
	if err := r.client.do(ctx, http.MethodGet, r.route(id, "policy"), nil, &p); err != nil {
		return false, err
	}
	if p.VmID != id {
		return false, fmt.Errorf("backup policy response has unexpected VM identity")
	}
	if p.Enabled == nil {
		return false, fmt.Errorf("backup policy response omitted enabled; refusing to remove state")
	}
	if !*p.Enabled {
		return false, nil
	}
	if p.PolicyID == "" || p.Schedule.Frequency == "" || p.Schedule.Timezone == "" || p.RetentionDays < 1 {
		return false, fmt.Errorf("incomplete backup policy response")
	}
	if err := hydrateRecoveryCatalog(&m.BillingCatalog, p.BillingCatalog); err != nil {
		return false, err
	}
	m.ID = types.StringValue(id)
	m.VmID = types.StringValue(id)
	m.PolicyID = types.StringValue(p.PolicyID)
	m.Frequency = types.StringValue(p.Schedule.Frequency)
	m.Timezone = types.StringValue(p.Schedule.Timezone)
	m.Hour = types.Int64Value(p.Schedule.Hour)
	m.Minute = types.Int64Value(p.Schedule.Minute)
	m.DayOfWeek = types.Int64PointerValue(p.Schedule.DayOfWeek)
	m.WindowMinutes = types.Int64Value(p.Schedule.WindowMinutes)
	m.RetentionDays = types.Int64Value(p.RetentionDays)
	m.FullBackupIntervalDays = types.Int64Value(p.FullBackupIntervalDays)
	m.IncrementalEnabled = types.BoolValue(p.IncrementalEnabled)
	m.NextRunAt = types.StringPointerValue(p.NextRunAt)
	return true, nil
}
func (r *vmBackupPolicyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m vmBackupPolicyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := validateBackupSchedule(m, nil); err != nil {
		resp.Diagnostics.AddError("Invalid backup schedule", err.Error())
		return
	}
	catalog, err := prepareRecoveryCatalog(ctx, r.client, r.vmType, m.VmID.ValueString(), m.BillingCatalog, "backup_storage")
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("billing_catalog"), "Invalid backup billing catalog", err.Error())
		return
	}
	if err := admitRecoveryCatalog(ctx, r.client, catalog); err != nil {
		resp.Diagnostics.AddError("Backup billing eligibility denied", err.Error())
		return
	}
	var p vmBackupPolicyAPI
	body := m.body()
	body["billing_catalog"] = catalog
	if err := r.client.do(ctx, http.MethodPost, r.route(m.VmID.ValueString(), "enable"), body, &p); err != nil {
		resp.Diagnostics.AddError("Failed to enable VM backups", err.Error())
		return
	}
	m.ID = m.VmID
	m.PolicyID = types.StringValue(p.PolicyID)
	m.NextRunAt = types.StringPointerValue(p.NextRunAt)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	enabled, err := r.refresh(ctx, &m)
	if err != nil {
		resp.Diagnostics.AddError("Failed to refresh backup policy", err.Error())
		return
	}
	if !enabled {
		resp.Diagnostics.AddError("Backups were not enabled", "API returned a disabled policy after enable.")
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *vmBackupPolicyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m vmBackupPolicyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	enabled, err := r.refresh(ctx, &m)
	if IsNotFound(err) || (err == nil && !enabled) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read backup policy", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func backupPolicyIncreases(old, next vmBackupPolicyModel) bool {
	rates := map[string]int{"weekly": 1, "daily": 7, "hourly": 168}
	return next.RetentionDays.ValueInt64() > old.RetentionDays.ValueInt64() || next.FullBackupIntervalDays.ValueInt64() < old.FullBackupIntervalDays.ValueInt64() || rates[next.Frequency.ValueString()] > rates[old.Frequency.ValueString()] || (old.IncrementalEnabled.ValueBool() && !next.IncrementalEnabled.ValueBool())
}
func (r *vmBackupPolicyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m, old vmBackupPolicyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := validateBackupSchedule(m, &old); err != nil {
		resp.Diagnostics.AddError("Invalid backup schedule", err.Error())
		return
	}
	body := m.body()
	if backupScheduleEqual(old, m) {
		delete(body, "schedule")
	}
	catalogChanged := !recoveryCatalogSelectionUnchanged(old.BillingCatalog, m.BillingCatalog)
	if catalogChanged {
		catalog, err := prepareRecoveryCatalog(ctx, r.client, r.vmType, m.VmID.ValueString(), m.BillingCatalog, "backup_storage")
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("billing_catalog"), "Invalid backup billing catalog", err.Error())
			return
		}
		if err := admitRecoveryCatalog(ctx, r.client, catalog); err != nil {
			resp.Diagnostics.AddError("Backup billing eligibility denied", err.Error())
			return
		}
		body["billing_catalog"] = catalog
	} else if backupPolicyIncreases(old, m) {
		if err := r.client.requireBillingEligibility(ctx, "", nil); err != nil {
			resp.Diagnostics.AddError("Backup billing eligibility denied", err.Error())
			return
		}
	}
	if catalogChanged || !backupScheduleEqual(old, m) || !old.RetentionDays.Equal(m.RetentionDays) || !old.FullBackupIntervalDays.Equal(m.FullBackupIntervalDays) || !old.IncrementalEnabled.Equal(m.IncrementalEnabled) {
		if err := r.client.do(ctx, http.MethodPatch, r.route(m.VmID.ValueString(), "policy"), body, nil); err != nil {
			resp.Diagnostics.AddError("Failed to update backup policy", err.Error())
			return
		}
	}
	enabled, err := r.refresh(ctx, &m)
	if err != nil {
		resp.Diagnostics.AddError("Failed to refresh backup policy", err.Error())
		return
	}
	if !enabled {
		resp.Diagnostics.AddError("Backup policy is disabled", "Refresh the resource and apply to re-enable backups.")
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *vmBackupPolicyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m vmBackupPolicyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var p vmBackupPolicyAPI
	err := r.client.do(ctx, http.MethodPost, r.route(m.VmID.ValueString(), "disable"), map[string]any{"requested_by": "terraform"}, &p)
	if IsNotFound(err) {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to disable backups", err.Error())
		return
	}
	if p.VmID != m.VmID.ValueString() || p.Enabled == nil || *p.Enabled {
		resp.Diagnostics.AddError("Backup disable unconfirmed", "API did not confirm a disabled policy for the requested VM; resource remains tracked.")
	}
}
func (r *vmBackupPolicyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("vm_id"), req.ID)...)
}
