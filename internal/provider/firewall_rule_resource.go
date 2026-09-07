package provider

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
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
	SystemManaged bool     `json:"system_managed"`
	CreatedAt     string   `json:"created_at"`
}

type firewallGroupWithRules struct {
	Rules []firewallRuleAPI `json:"rules"`
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
				Optional: true,
			},
			"remote_targets": schema.ListAttribute{
				ElementType: types.StringType,
				Optional:    true,
				Description: `CIDR blocks, e.g. ["0.0.0.0/0"].`,
			},
			"action": schema.StringAttribute{
				Optional: true, Computed: true,
				Default:       stringdefault.StaticString("allow"),
				Description:   "allow or drop.",
				PlanModifiers: replace,
			},
			"description": schema.StringAttribute{Optional: true},
			"enabled": schema.BoolAttribute{
				Optional: true, Computed: true,
				Default: booldefault.StaticBool(true),
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

func (r *firewallRuleResource) ruleBody(ctx context.Context, m *firewallRuleModel) map[string]any {
	body := map[string]any{
		"direction": m.Direction.ValueString(),
		"protocol":  m.Protocol.ValueString(),
		"action":    m.Action.ValueString(),
	}
	if !m.PortStart.IsNull() {
		body["port_start"] = m.PortStart.ValueInt64()
	}
	if !m.PortEnd.IsNull() {
		body["port_end"] = m.PortEnd.ValueInt64()
	}
	if !m.Description.IsNull() {
		body["description"] = m.Description.ValueString()
	}
	if !m.RemoteTargets.IsNull() && !m.RemoteTargets.IsUnknown() {
		var targets []string
		m.RemoteTargets.ElementsAs(ctx, &targets, false)
		body["remote_targets"] = targets
	}
	return body
}

// matchRule finds our rule in the group's rules array: the create endpoint
// returns the parent group, so the new rule must be located by its fields.
func (r *firewallRuleResource) matchRule(rules []firewallRuleAPI, m *firewallRuleModel) *firewallRuleAPI {
	var best *firewallRuleAPI
	for i := range rules {
		rule := &rules[i]
		if rule.SystemManaged {
			continue
		}
		if rule.Direction != m.Direction.ValueString() || rule.Protocol != m.Protocol.ValueString() {
			continue
		}
		if !m.PortStart.IsNull() && (rule.PortStart == nil || *rule.PortStart != m.PortStart.ValueInt64()) {
			continue
		}
		if !m.Description.IsNull() && (rule.Description == nil || *rule.Description != m.Description.ValueString()) {
			continue
		}
		if best == nil || rule.CreatedAt > best.CreatedAt {
			best = rule
		}
	}
	return best
}

func (r *firewallRuleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan firewallRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var group firewallGroupWithRules
	err := r.client.do(ctx, http.MethodPost,
		"/networking/firewall-groups/"+plan.GroupID.ValueString()+"/rules",
		r.ruleBody(ctx, &plan), &group)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create firewall rule", err.Error())
		return
	}

	rule := r.matchRule(group.Rules, &plan)
	if rule == nil {
		resp.Diagnostics.AddError("Rule created but not found in group response",
			"could not locate the new rule in the returned rules array")
		return
	}
	plan.ID = types.StringValue(rule.RuleID)
	plan.Enabled = types.BoolValue(rule.Enabled)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *firewallRuleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state firewallRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var group firewallGroupWithRules
	err := r.client.do(ctx, http.MethodGet,
		"/networking/firewall-groups/"+state.GroupID.ValueString(), nil, &group)
	if err != nil {
		if IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read firewall group", err.Error())
		return
	}
	for i := range group.Rules {
		if group.Rules[i].RuleID == state.ID.ValueString() {
			rule := group.Rules[i]
			state.Enabled = types.BoolValue(rule.Enabled)
			if rule.Description != nil {
				state.Description = types.StringValue(*rule.Description)
			}
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

	body := r.ruleBody(ctx, &plan)
	body["enabled"] = plan.Enabled.ValueBool()
	err := r.client.do(ctx, http.MethodPatch,
		"/networking/firewall-groups/"+plan.GroupID.ValueString()+"/rules/"+plan.ID.ValueString(),
		body, nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update firewall rule", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *firewallRuleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state firewallRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.do(ctx, http.MethodDelete,
		"/networking/firewall-groups/"+state.GroupID.ValueString()+"/rules/"+state.ID.ValueString(), nil, nil)
	if err != nil && !IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to delete firewall rule", err.Error())
	}
}
