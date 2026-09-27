package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type billingEligibilityDataSource struct{ client *Client }

func NewBillingEligibilityDataSource() datasource.DataSource { return &billingEligibilityDataSource{} }

var _ datasource.DataSourceWithConfigure = (*billingEligibilityDataSource)(nil)
var _ datasource.DataSourceWithValidateConfig = (*billingEligibilityDataSource)(nil)

func (d *billingEligibilityDataSource) ValidateConfig(ctx context.Context, req datasource.ValidateConfigRequest, resp *datasource.ValidateConfigResponse) {
	var config billingEligibilityModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !config.SKUCode.IsNull() && !config.SKUCode.IsUnknown() {
		sku := strings.TrimSpace(config.SKUCode.ValueString())
		if sku == "" || len(sku) > 64 {
			resp.Diagnostics.AddAttributeError(path.Root("sku_code"), "Invalid SKU", "Specify a nonempty SKU up to 64 characters, or omit sku_code for an account-status check.")
		}
	}
	if !config.EstimatedCostMinor.IsNull() && !config.EstimatedCostMinor.IsUnknown() && config.EstimatedCostMinor.ValueInt64() < 0 {
		resp.Diagnostics.AddAttributeError(path.Root("estimated_cost_minor"), "Invalid cost estimate", "Use a nonnegative amount in currency minor units.")
	}
}

type billingEligibilityModel struct {
	SKUCode               types.String `tfsdk:"sku_code"`
	EstimatedCostMinor    types.Int64  `tfsdk:"estimated_cost_minor"`
	OrganizationID        types.String `tfsdk:"organization_id"`
	Allowed               types.Bool   `tfsdk:"allowed"`
	Reason                types.String `tfsdk:"reason"`
	BillingMode           types.String `tfsdk:"billing_mode"`
	BillingState          types.String `tfsdk:"billing_state"`
	Currency              types.String `tfsdk:"currency"`
	EffectiveBalanceMinor types.Int64  `tfsdk:"effective_balance_minor"`
	CreditHeadroomMinor   types.Int64  `tfsdk:"credit_headroom_minor"`
	EvaluatedAt           types.String `tfsdk:"evaluated_at"`
}

func (d *billingEligibilityDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_billing_eligibility"
}
func (d *billingEligibilityDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "Read the organization's current billing admission decision. Requires billing.read. This does not reserve funds, purchase credits, or guarantee later provisioning; creates check again and the backend remains authoritative.", Attributes: map[string]schema.Attribute{
		"sku_code":                schema.StringAttribute{Optional: true, Description: "Optional active catalog SKU to evaluate. Omit for an account-status check."},
		"estimated_cost_minor":    schema.Int64Attribute{Optional: true, Description: "Optional nonnegative estimated cost in currency minor units. An estimate is advisory, not an override of backend pricing."},
		"organization_id":         schema.StringAttribute{Computed: true, Description: "Organization resolved by the public gateway."},
		"allowed":                 schema.BoolAttribute{Computed: true, Description: "Whether the billing service currently permits the requested purchase."},
		"reason":                  schema.StringAttribute{Computed: true, Description: "Machine-readable reason for the decision."},
		"billing_mode":            schema.StringAttribute{Computed: true, Description: "PREPAID or POSTPAID."},
		"billing_state":           schema.StringAttribute{Computed: true, Description: "Current billing account state."},
		"currency":                schema.StringAttribute{Computed: true, Description: "Wallet currency."},
		"effective_balance_minor": schema.Int64Attribute{Computed: true, Sensitive: true, Description: "Effective prepaid balance, if returned, in currency minor units."},
		"credit_headroom_minor":   schema.Int64Attribute{Computed: true, Sensitive: true, Description: "Remaining postpaid credit, if returned, in currency minor units."},
		"evaluated_at":            schema.StringAttribute{Computed: true, Description: "Server evaluation timestamp."},
	}}
}
func (d *billingEligibilityDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *Client, got %T", req.ProviderData))
		return
	}
	d.client = client
}
func (d *billingEligibilityDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state billingEligibilityModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	request := billingEligibilityRequest{SKUCode: state.SKUCode.ValueString()}
	if !state.EstimatedCostMinor.IsNull() && !state.EstimatedCostMinor.IsUnknown() {
		cost := state.EstimatedCostMinor.ValueInt64()
		request.EstimatedCostMinor = &cost
	}
	decision, err := d.client.checkBillingEligibility(ctx, request)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read billing eligibility", err.Error())
		return
	}
	state.OrganizationID = types.StringValue(decision.OrganizationID)
	state.Allowed = types.BoolValue(*decision.Allowed)
	state.Reason = types.StringValue(decision.Reason)
	state.BillingMode = types.StringValue(decision.BillingMode)
	state.BillingState = types.StringValue(decision.BillingState)
	state.Currency = types.StringValue(decision.Currency)
	state.EvaluatedAt = types.StringValue(decision.EvaluatedAt)
	state.EffectiveBalanceMinor = types.Int64PointerValue(decision.EffectiveBalanceMinor)
	state.CreditHeadroomMinor = types.Int64PointerValue(decision.CreditHeadroomMinor)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
