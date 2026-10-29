package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"reflect"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
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
		Frequency     *string `json:"frequency"`
		Timezone      *string `json:"timezone"`
		Hour          *int64  `json:"hour"`
		Minute        *int64  `json:"minute"`
		DayOfWeek     *int64  `json:"day_of_week"`
		WindowMinutes *int64  `json:"window_minutes"`
	} `json:"schedule"`
	RetentionDays          *int64  `json:"retention_days"`
	FullBackupIntervalDays *int64  `json:"full_backup_interval_days"`
	IncrementalEnabled     *bool   `json:"incremental_enabled"`
	NextRunAt              *string `json:"next_run_at"`
}

func (r *vmBackupPolicyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.vmType + "_vm_backup_policy"
}
func (r *vmBackupPolicyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	str := func(description string) schema.StringAttribute {
		return schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, Description: description}
	}
	integer := func(description string) schema.Int64Attribute {
		return schema.Int64Attribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}, Description: description}
	}
	resp.Schema = schema.Schema{Description: "Enables (backups/enable) and updates (PATCH backups/policy) automated backups of a VM. Settings that are not configured are not sent, so the API's defaults apply, and are read back from the API. Destroy disables backups (backups/disable); existing recovery points are kept and may continue to be billed. Import using the VM ID.", Attributes: map[string]schema.Attribute{
		"id":                        schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, Description: "VM ID, used as the identity of its policy."},
		"vm_id":                     schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"policy_id":                 schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"billing_catalog":           recoveryCatalogAttribute("backup_storage (see ibee_backup_storage_plans)", true),
		"frequency":                 str("Schedule frequency, such as daily or weekly."),
		"timezone":                  str("IANA time zone of the schedule."),
		"hour":                      integer("Hour of the day in timezone."),
		"minute":                    integer("Minute of the hour."),
		"day_of_week":               integer("Day of the week for weekly schedules (Monday=0)."),
		"window_minutes":            integer("Length of the backup window in minutes."),
		"retention_days":            integer("Days recovery points are kept."),
		"full_backup_interval_days": integer("Days between full backups."),
		"incremental_enabled":       schema.BoolAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}},
		"next_run_at":               schema.StringAttribute{Computed: true},
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
func (r *vmBackupPolicyResource) route(vmID string, action string) string {
	return "/compute/" + r.vmType + "-vms/" + url.PathEscape(vmID) + "/backups/" + action
}

func known(v attr.Value) bool { return !v.IsNull() && !v.IsUnknown() }

// body sends the schedule and retention settings that have a value.
func (m *vmBackupPolicyModel) body() map[string]any {
	schedule := map[string]any{}
	for key, value := range map[string]types.String{"frequency": m.Frequency, "timezone": m.Timezone} {
		if known(value) {
			schedule[key] = value.ValueString()
		}
	}
	for key, value := range map[string]types.Int64{"hour": m.Hour, "minute": m.Minute, "day_of_week": m.DayOfWeek, "window_minutes": m.WindowMinutes} {
		if known(value) {
			schedule[key] = value.ValueInt64()
		}
	}
	body := map[string]any{}
	if len(schedule) > 0 {
		body["schedule"] = schedule
	}
	for key, value := range map[string]types.Int64{"retention_days": m.RetentionDays, "full_backup_interval_days": m.FullBackupIntervalDays} {
		if known(value) {
			body[key] = value.ValueInt64()
		}
	}
	if known(m.IncrementalEnabled) {
		body["incremental_enabled"] = m.IncrementalEnabled.ValueBool()
	}
	return body
}

// refresh reads the policy; it reports false when backups are disabled.
func (r *vmBackupPolicyResource) refresh(ctx context.Context, m *vmBackupPolicyModel) (bool, error) {
	id := m.VmID.ValueString()
	if id == "" {
		id = m.ID.ValueString()
	}
	var p vmBackupPolicyAPI
	if err := r.client.do(ctx, http.MethodGet, r.route(id, "policy"), nil, &p); err != nil {
		return false, err
	}
	if p.Enabled != nil && !*p.Enabled {
		return false, nil
	}
	if err := hydrateRecoveryCatalog(&m.BillingCatalog, p.BillingCatalog); err != nil {
		return false, err
	}
	m.ID = types.StringValue(id)
	m.VmID = types.StringValue(id)
	m.PolicyID = types.StringValue(p.PolicyID)
	for _, field := range []struct {
		target *types.String
		value  *string
	}{{&m.Frequency, p.Schedule.Frequency}, {&m.Timezone, p.Schedule.Timezone}, {&m.NextRunAt, p.NextRunAt}} {
		*field.target = types.StringPointerValue(field.value)
	}
	for _, field := range []struct {
		target *types.Int64
		value  *int64
	}{{&m.Hour, p.Schedule.Hour}, {&m.Minute, p.Schedule.Minute}, {&m.DayOfWeek, p.Schedule.DayOfWeek}, {&m.WindowMinutes, p.Schedule.WindowMinutes}, {&m.RetentionDays, p.RetentionDays}, {&m.FullBackupIntervalDays, p.FullBackupIntervalDays}} {
		*field.target = types.Int64PointerValue(field.value)
	}
	m.IncrementalEnabled = types.BoolPointerValue(p.IncrementalEnabled)
	return true, nil
}
func (r *vmBackupPolicyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m vmBackupPolicyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	catalog, err := recoveryCatalogBody(m.BillingCatalog)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("billing_catalog"), "Invalid backup billing catalog", err.Error())
		return
	}
	body := m.body()
	if catalog != nil {
		body["billing_catalog"] = catalog
	}
	var p vmBackupPolicyAPI
	if err := r.client.do(ctx, http.MethodPost, r.route(m.VmID.ValueString(), "enable"), body, &p); err != nil {
		resp.Diagnostics.AddError("Failed to enable VM backups", err.Error())
		return
	}
	if _, err := r.refresh(ctx, &m); err != nil {
		resp.Diagnostics.AddError("Failed to refresh backup policy", err.Error())
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

// Update sends the settings that changed: the whole schedule when any part of
// it changed, and each retention setting that changed.
func (r *vmBackupPolicyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m, old vmBackupPolicyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, before := m.body(), old.body()
	for key, value := range body {
		if reflect.DeepEqual(before[key], value) {
			delete(body, key)
		}
	}
	if err := r.client.do(ctx, http.MethodPatch, r.route(m.VmID.ValueString(), "policy"), body, nil); err != nil {
		resp.Diagnostics.AddError("Failed to update backup policy", err.Error())
		return
	}
	if _, err := r.refresh(ctx, &m); err != nil {
		resp.Diagnostics.AddError("Failed to refresh backup policy", err.Error())
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
	// Contract 0.7.1: send an empty JSON object; the virtualization engine refuses a request with no body.
	err := r.client.do(ctx, http.MethodPost, r.route(m.VmID.ValueString(), "disable"), map[string]any{}, nil)
	if err != nil && !IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to disable backups", err.Error())
	}
}
func (r *vmBackupPolicyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("vm_id"), req.ID)...)
}
