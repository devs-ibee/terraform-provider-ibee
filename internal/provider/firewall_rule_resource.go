package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource              = (*firewallRuleResource)(nil)
	_ resource.ResourceWithConfigure = (*firewallRuleResource)(nil)
)

type firewallRuleResource struct {
	client *Client
}

func NewFirewallRuleResource() resource.Resource { return &firewallRuleResource{} }

type firewallRuleModel struct {
	ID            types.String `tfsdk:"id"`
	GroupID       types.String `tfsdk:"firewall_group_id"`
	Direction     types.String `tfsdk:"direction"`
	Protocol      types.String `tfsdk:"protocol"`
	PortStart     types.Int64  `tfsdk:"port_start"`
	PortEnd       types.Int64  `tfsdk:"port_end"`
	RemoteTargets types.List   `tfsdk:"remote_targets"`
	Action        types.String `tfsdk:"action"`
	Description   types.String `tfsdk:"description"`
	Enabled       types.Bool   `tfsdk:"enabled"`
	Priority      types.Int64  `tfsdk:"priority"`
}

type firewallRuleAPI struct {
	RuleID        string   `json:"rule_id"`
	Direction     string   `json:"direction"`
	Protocol      string   `json:"protocol"`
	PortStart     *int64   `json:"port_start"`
	PortEnd       *int64   `json:"port_end"`
	RemoteTargets []string `json:"remote_targets"`
	Action        string   `json:"action"`
	Description   *string  `json:"description"`
	Enabled       bool     `json:"enabled"`
	Priority      *int64   `json:"priority"`
	SystemManaged bool     `json:"system_managed"`
	CreatedAt     string   `json:"created_at"`
}

type firewallGroupWithRules struct {
	Rules *[]firewallRuleAPI `json:"rules"`
}

func (r *firewallRuleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_firewall_rule"
}

func (r *firewallRuleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "A rule inside an IBEE firewall group. Settings that are not configured are not sent and the API's defaults apply. direction, protocol and action replace the rule; other changes are sent with PATCH. Import as firewall_group_id/rule_id.",
		Attributes: map[string]schema.Attribute{
			"id":                schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"firewall_group_id": schema.StringAttribute{Required: true, PlanModifiers: replace},
			"direction":         schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keep, Description: "ingress or egress."},
			"protocol":          schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keep, Description: "tcp, udp, icmp or any."},
			"port_start":        schema.Int64Attribute{Optional: true},
			"port_end":          schema.Int64Attribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}},
			"remote_targets":    schema.ListAttribute{ElementType: types.StringType, Optional: true, Computed: true, PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()}, Description: `CIDR blocks, for example ["0.0.0.0/0"].`},
			"action":            schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keep, Description: "allow or drop."},
			"description":       schema.StringAttribute{Optional: true},
			"priority":          schema.Int64Attribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}},
			"enabled":           schema.BoolAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}, Description: "The create operation has no enabled field, so false is sent in a PATCH after create."},
		},
	}
}

func (r *firewallRuleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *Client, got %T", req.ProviderData))
		return
	}
	r.client = client
}

// The API returns the complete group after create, not a rule identifier. A
// process-wide lock serializes local creates, and before/after IDs plus complete
// field equality disambiguate the new rule. Concurrent external identical writes
// fail explicitly; the provider never adopts the newest-looking rule.
var firewallMutationMu sync.Mutex

// ruleBody sends the rule fields that have a value. On update a removed
// description, port or priority is sent as null.
func (r *firewallRuleResource) ruleBody(ctx context.Context, m *firewallRuleModel, update bool) map[string]any {
	b := map[string]any{}
	for key, value := range map[string]types.String{"direction": m.Direction, "protocol": m.Protocol, "action": m.Action, "description": m.Description} {
		if known(value) {
			b[key] = value.ValueString()
		} else if update && value.IsNull() {
			b[key] = nil
		}
	}
	for key, value := range map[string]types.Int64{"port_start": m.PortStart, "port_end": m.PortEnd, "priority": m.Priority} {
		if known(value) {
			b[key] = value.ValueInt64()
		} else if update && value.IsNull() {
			b[key] = nil
		}
	}
	if known(m.RemoteTargets) {
		var targets []string
		m.RemoteTargets.ElementsAs(ctx, &targets, false)
		b["remote_targets"] = targets
	}
	return b
}

// firewallRuleMatches reports whether a rule in the group has every value the
// plan knows. The create response is the whole group, not the new rule.
func firewallRuleMatches(ctx context.Context, rule firewallRuleAPI, m *firewallRuleModel) bool {
	if rule.SystemManaged || rule.RuleID == "" {
		return false
	}
	for _, field := range []struct {
		want types.String
		got  string
	}{{m.Direction, rule.Direction}, {m.Protocol, rule.Protocol}, {m.Action, rule.Action}} {
		if known(field.want) && field.want.ValueString() != field.got {
			return false
		}
	}
	for _, field := range []struct {
		want types.Int64
		got  *int64
	}{{m.PortStart, rule.PortStart}, {m.PortEnd, rule.PortEnd}, {m.Priority, rule.Priority}} {
		if known(field.want) && (field.got == nil || *field.got != field.want.ValueInt64()) {
			return false
		}
	}
	if known(m.Description) && (rule.Description == nil || *rule.Description != m.Description.ValueString()) {
		return false
	}
	if known(m.RemoteTargets) {
		var targets []string
		m.RemoteTargets.ElementsAs(ctx, &targets, false)
		if !reflect.DeepEqual(rule.RemoteTargets, targets) {
			return false
		}
	}
	return true
}
func identifyNewFirewallRule(ctx context.Context, before, after []firewallRuleAPI, m *firewallRuleModel) (*firewallRuleAPI, error) {
	existing := map[string]bool{}
	for _, rule := range before {
		existing[rule.RuleID] = true
	}
	var found *firewallRuleAPI
	for i := range after {
		rule := &after[i]
		if existing[rule.RuleID] || !firewallRuleMatches(ctx, *rule, m) {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("multiple newly created identical rules; inspect/import the intended rule before retrying")
		}
		found = rule
	}
	if found == nil {
		return nil, fmt.Errorf("no unique new rule in response; inspect the firewall group before retrying")
	}
	return found, nil
}
func (r *firewallRuleResource) group(ctx context.Context, id string) ([]firewallRuleAPI, error) {
	var group firewallGroupWithRules
	if err := r.client.do(ctx, http.MethodGet, "/networking/firewall-groups/"+url.PathEscape(id), nil, &group); err != nil {
		return nil, err
	}
	if group.Rules == nil {
		return nil, fmt.Errorf("API response omitted rules array")
	}
	return *group.Rules, nil
}
func firewallRuleState(ctx context.Context, m *firewallRuleModel, rule firewallRuleAPI) diag.Diagnostics {
	m.Direction = types.StringValue(rule.Direction)
	m.Protocol = types.StringValue(rule.Protocol)
	m.Action = types.StringValue(rule.Action)
	m.Enabled = types.BoolValue(rule.Enabled)
	m.PortStart = types.Int64PointerValue(rule.PortStart)
	m.PortEnd = types.Int64PointerValue(rule.PortEnd)
	m.Priority = types.Int64PointerValue(rule.Priority)
	m.Description = types.StringPointerValue(rule.Description)
	var d diag.Diagnostics
	m.RemoteTargets, d = types.ListValueFrom(ctx, types.StringType, rule.RemoteTargets)
	return d
}
func (r *firewallRuleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan firewallRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	firewallMutationMu.Lock()
	defer firewallMutationMu.Unlock()
	before, err := r.group(ctx, plan.GroupID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to read firewall group", err.Error())
		return
	}
	var group firewallGroupWithRules
	if err = r.client.do(ctx, http.MethodPost, "/networking/firewall-groups/"+url.PathEscape(plan.GroupID.ValueString())+"/rules", r.ruleBody(ctx, &plan, false), &group); err != nil {
		resp.Diagnostics.AddError("Failed to create firewall rule", err.Error())
		return
	}
	if group.Rules == nil {
		resp.Diagnostics.AddError("Invalid firewall group response", "Create response omitted rules; inspect the group before retrying.")
		return
	}
	rule, err := identifyNewFirewallRule(ctx, before, *group.Rules, &plan)
	if err != nil {
		resp.Diagnostics.AddError("Cannot identify created firewall rule", err.Error())
		return
	}
	desired := plan.Enabled
	plan.ID = types.StringValue(rule.RuleID)
	resp.Diagnostics.Append(firewallRuleState(ctx, &plan, *rule)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if known(desired) && rule.Enabled != desired.ValueBool() {
		desiredEnabled := desired.ValueBool()
		if err = r.client.do(ctx, http.MethodPatch, "/networking/firewall-groups/"+url.PathEscape(plan.GroupID.ValueString())+"/rules/"+url.PathEscape(plan.ID.ValueString()), map[string]any{"enabled": desiredEnabled}, nil); err != nil {
			resp.Diagnostics.AddError("Rule created but enabled state could not be set", err.Error())
			return
		}
		plan.Enabled = types.BoolValue(desiredEnabled)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}
func (r *firewallRuleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state firewallRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rules, err := r.group(ctx, state.GroupID.ValueString())
	if err != nil {
		if IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read firewall group", err.Error())
		return
	}
	for _, rule := range rules {
		if rule.RuleID == state.ID.ValueString() {
			resp.Diagnostics.Append(firewallRuleState(ctx, &state, rule)...)
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			return
		}
	}
	resp.State.RemoveResource(ctx)
}
func (r *firewallRuleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan firewallRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	firewallMutationMu.Lock()
	defer firewallMutationMu.Unlock()
	body := r.ruleBody(ctx, &plan, true)
	if known(plan.Enabled) {
		body["enabled"] = plan.Enabled.ValueBool()
	}
	if err := r.client.do(ctx, http.MethodPatch, "/networking/firewall-groups/"+url.PathEscape(plan.GroupID.ValueString())+"/rules/"+url.PathEscape(plan.ID.ValueString()), body, nil); err != nil {
		resp.Diagnostics.AddError("Failed to update firewall rule", err.Error())
		return
	}
	rules, err := r.group(ctx, plan.GroupID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to refresh updated firewall rule", err.Error())
		return
	}
	for _, rule := range rules {
		if rule.RuleID == plan.ID.ValueString() {
			resp.Diagnostics.Append(firewallRuleState(ctx, &plan, rule)...)
			resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
			return
		}
	}
	resp.Diagnostics.AddError("Updated firewall rule missing", "API no longer returns the managed rule")
}
func (r *firewallRuleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state firewallRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	firewallMutationMu.Lock()
	defer firewallMutationMu.Unlock()
	if err := r.client.do(ctx, http.MethodDelete, "/networking/firewall-groups/"+url.PathEscape(state.GroupID.ValueString())+"/rules/"+url.PathEscape(state.ID.ValueString()), nil, nil); err != nil && !IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to delete firewall rule", err.Error())
	}
}
func (r *firewallRuleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError("Invalid import ID", "Expected firewall_group_id/rule_id")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("firewall_group_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
}
