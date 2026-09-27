package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"reflect"
	"strings"
	"sync"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
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
	resp.Schema = schema.Schema{
		Description: "A rule inside an IBEE firewall group.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"firewall_group_id": schema.StringAttribute{Required: true, PlanModifiers: replace},
			"direction": schema.StringAttribute{
				Optional: true, Computed: true,
				Default:       stringdefault.StaticString("ingress"),
				PlanModifiers: replace,
			},
			"protocol": schema.StringAttribute{
				Optional: true, Computed: true,
				Default:       stringdefault.StaticString("tcp"),
				Description:   "tcp, udp, icmp, or any.",
				PlanModifiers: replace,
			},
			"port_start": schema.Int64Attribute{
				Optional:      true,
				PlanModifiers: nil,
			},
			"port_end": schema.Int64Attribute{
				Optional: true, Computed: true,
			},
			"remote_targets": schema.ListAttribute{
				ElementType: types.StringType,
				Optional:    true, Computed: true, Default: listdefault.StaticValue(types.ListValueMust(types.StringType, []attr.Value{types.StringValue("0.0.0.0/0")})),
				Description: `CIDR blocks, e.g. ["0.0.0.0/0"].`,
			},
			"action": schema.StringAttribute{
				Optional: true, Computed: true,
				Default:       stringdefault.StaticString("allow"),
				Description:   "allow or drop.",
				PlanModifiers: replace,
			},
			"description": schema.StringAttribute{Optional: true},
			"priority":    schema.Int64Attribute{Optional: true, Computed: true},
			"enabled": schema.BoolAttribute{
				Optional: true, Computed: true,
				Default:     booldefault.StaticBool(true),
				Description: "The public create API initially enables rules. When false, the provider immediately disables the rule via PATCH; attach the group only after its rules are created.",
			},
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

func (r *firewallRuleResource) ruleBody(ctx context.Context, m *firewallRuleModel) map[string]any {
	b := map[string]any{"direction": m.Direction.ValueString(), "protocol": m.Protocol.ValueString(), "action": m.Action.ValueString(), "port_start": nil, "port_end": nil, "description": nil, "priority": nil}
	if !m.PortStart.IsNull() && !m.PortStart.IsUnknown() {
		b["port_start"] = m.PortStart.ValueInt64()
	}
	if !m.PortEnd.IsNull() && !m.PortEnd.IsUnknown() {
		b["port_end"] = m.PortEnd.ValueInt64()
	}
	if !m.Description.IsNull() {
		b["description"] = m.Description.ValueString()
	}
	if !m.Priority.IsNull() && !m.Priority.IsUnknown() {
		b["priority"] = m.Priority.ValueInt64()
	}
	var targets []string
	m.RemoteTargets.ElementsAs(ctx, &targets, false)
	b["remote_targets"] = targets
	return b
}
func firewallValidate(ctx context.Context, m *firewallRuleModel) error {
	if m.Direction.ValueString() != "ingress" && m.Direction.ValueString() != "egress" {
		return fmt.Errorf("direction must be ingress or egress")
	}
	if m.Action.ValueString() != "allow" && m.Action.ValueString() != "drop" {
		return fmt.Errorf("action must be allow or drop")
	}
	p := m.Protocol.ValueString()
	if p != "tcp" && p != "udp" && p != "icmp" && p != "any" {
		return fmt.Errorf("protocol must be tcp, udp, icmp or any")
	}
	if p == "tcp" || p == "udp" {
		if m.PortStart.IsNull() || m.PortStart.ValueInt64() < 1 || m.PortStart.ValueInt64() > 65535 {
			return fmt.Errorf("tcp/udp port_start must be between 1 and 65535")
		}
		if !m.PortEnd.IsNull() && !m.PortEnd.IsUnknown() && (m.PortEnd.ValueInt64() < m.PortStart.ValueInt64() || m.PortEnd.ValueInt64() > 65535) {
			return fmt.Errorf("port_end must be between port_start and 65535")
		}
	} else if !m.PortStart.IsNull() || (!m.PortEnd.IsNull() && !m.PortEnd.IsUnknown()) {
		return fmt.Errorf("icmp/any rules cannot specify ports")
	}
	var targets []string
	if d := m.RemoteTargets.ElementsAs(ctx, &targets, false); d.HasError() {
		return fmt.Errorf("remote_targets are invalid")
	}
	if len(targets) == 0 {
		return fmt.Errorf("remote_targets must not be empty; use 0.0.0.0/0 explicitly")
	}
	seen := map[string]bool{}
	for _, target := range targets {
		if seen[target] {
			return fmt.Errorf("duplicate remote target %q", target)
		}
		seen[target] = true
		if p, err := netip.ParsePrefix(target); err == nil {
			if p.Masked().String() != target {
				return fmt.Errorf("remote target %q must be a canonical CIDR (%s)", target, p.Masked())
			}
		}
	}
	return nil
}
func firewallRuleMatches(ctx context.Context, rule firewallRuleAPI, m *firewallRuleModel) bool {
	if rule.SystemManaged || rule.RuleID == "" || rule.Direction != m.Direction.ValueString() || rule.Protocol != m.Protocol.ValueString() || rule.Action != m.Action.ValueString() {
		return false
	}
	if (rule.PortStart == nil) != m.PortStart.IsNull() {
		return false
	}
	if rule.PortStart != nil && *rule.PortStart != m.PortStart.ValueInt64() {
		return false
	}
	expectedEnd := m.PortEnd
	if expectedEnd.IsUnknown() || expectedEnd.IsNull() {
		expectedEnd = m.PortStart
	}
	if (rule.PortEnd == nil) != expectedEnd.IsNull() {
		return false
	}
	if rule.PortEnd != nil && *rule.PortEnd != expectedEnd.ValueInt64() {
		return false
	}
	if (rule.Description == nil) != m.Description.IsNull() {
		return false
	}
	if rule.Description != nil && *rule.Description != m.Description.ValueString() {
		return false
	}
	if !m.Priority.IsNull() && !m.Priority.IsUnknown() && (rule.Priority == nil || *rule.Priority != m.Priority.ValueInt64()) {
		return false
	}
	var targets []string
	m.RemoteTargets.ElementsAs(ctx, &targets, false)
	return reflect.DeepEqual(rule.RemoteTargets, targets)
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
	if err := firewallValidate(ctx, &plan); err != nil {
		resp.Diagnostics.AddError("Invalid firewall rule", err.Error())
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
	if err = r.client.do(ctx, http.MethodPost, "/networking/firewall-groups/"+url.PathEscape(plan.GroupID.ValueString())+"/rules", r.ruleBody(ctx, &plan), &group); err != nil {
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
	desiredEnabled := plan.Enabled.ValueBool()
	plan.ID = types.StringValue(rule.RuleID)
	resp.Diagnostics.Append(firewallRuleState(ctx, &plan, *rule)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if rule.Enabled != desiredEnabled {
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
	if err := firewallValidate(ctx, &plan); err != nil {
		resp.Diagnostics.AddError("Invalid firewall rule", err.Error())
		return
	}
	firewallMutationMu.Lock()
	defer firewallMutationMu.Unlock()
	body := r.ruleBody(ctx, &plan)
	body["enabled"] = plan.Enabled.ValueBool()
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
