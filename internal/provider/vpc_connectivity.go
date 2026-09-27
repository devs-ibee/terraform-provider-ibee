package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type vpcNATGatewayAPI struct {
	ID     string `json:"nat_gateway_id"`
	Status string `json:"status"`
}

func validVpcConnectivity(value string) bool {
	return value == "public" || value == "private" || value == "nat_gateway"
}

func (r *vpcResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config vpcResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !config.ConnectivityType.IsNull() && !config.ConnectivityType.IsUnknown() && !validVpcConnectivity(config.ConnectivityType.ValueString()) {
		resp.Diagnostics.AddError("Invalid VPC connectivity", "connectivity_type must be public, private or nat_gateway")
	}
}

func populateVpcCreatedState(plan *vpcResourceModel, out vpcAPI) {
	plan.ID = types.StringValue(out.identifier())
	if out.Cidr != "" {
		plan.Cidr = types.StringValue(out.Cidr)
	} else if plan.Cidr.IsUnknown() {
		plan.Cidr = types.StringNull()
	}
	plan.Status = types.StringValue(out.Status)
	if out.Description != nil {
		plan.Description = types.StringValue(*out.Description)
	} else {
		plan.Description = types.StringValue("")
	}
	plan.DefaultSubnetID, plan.OwnedDefaultSubnetID = types.StringNull(), types.StringNull()
	plan.DefaultNATGatewayID, plan.OwnedDefaultNATID = types.StringNull(), types.StringNull()
	if plan.CreateDefaultSubnet.ValueBool() && len(out.Subnets) == 1 && out.Subnets[0].identifier() != "" {
		plan.DefaultSubnetID = types.StringValue(out.Subnets[0].identifier())
		plan.OwnedDefaultSubnetID = plan.DefaultSubnetID
	}
	if plan.ConnectivityType.ValueString() == "nat_gateway" && len(out.NATGateways) == 1 && out.NATGateways[0].ID != "" {
		plan.DefaultNATGatewayID = types.StringValue(out.NATGateways[0].ID)
		plan.OwnedDefaultNATID = plan.DefaultNATGatewayID
	}
}

func refreshVpcNATOwnership(state *vpcResourceModel, out vpcAPI) error {
	if out.NATGateways == nil && (state.ConnectivityType.ValueString() == "nat_gateway" || state.OwnedDefaultNATID.ValueString() != "") {
		return fmt.Errorf("API omitted nat_gateways; preserving recorded ownership")
	}
	if len(out.NATGateways) > 1 {
		return fmt.Errorf("API returned multiple NAT gateways for a single-gateway VPC")
	}
	state.DefaultNATGatewayID = types.StringNull()
	found := false
	for _, gateway := range out.NATGateways {
		if gateway.ID == "" {
			return fmt.Errorf("NAT gateway omitted its ID")
		}
		state.DefaultNATGatewayID = types.StringValue(gateway.ID)
		if gateway.ID == state.OwnedDefaultNATID.ValueString() {
			found = true
		}
	}
	if !found {
		state.OwnedDefaultNATID = types.StringNull()
	}
	return nil
}

func (r *vpcResource) vpcCreationInventory(ctx context.Context, plan vpcResourceModel) (map[string]bool, error) {
	var items []vpcAPI
	if err := r.client.do(ctx, http.MethodGet, "/networking/vpcs", nil, &items); err != nil {
		return nil, err
	}
	if items == nil {
		return nil, fmt.Errorf("VPC list was not a non-null array")
	}
	ids := map[string]bool{}
	for _, item := range items {
		if item.identifier() == "" || item.Name == "" || item.SiteID == "" {
			return nil, fmt.Errorf("VPC inventory omitted identity fields")
		}
		if item.Name == strings.TrimSpace(plan.Name.ValueString()) {
			return nil, fmt.Errorf("a VPC with this name already exists; import it and its children separately instead of creating another")
		}
		ids[item.identifier()] = true
	}
	return ids, nil
}

// The public API has no idempotency key or operation ID for compound creation.
// Use a unique name and a single writer. Identify only a candidate for manual
// reconciliation, never an ownership proof. Even a new matching ID could have
// been created concurrently by another actor after our inventory read.
func (r *vpcResource) reconcileVpcCreation(ctx context.Context, plan vpcResourceModel, before map[string]bool) (*vpcAPI, error) {
	var items []vpcAPI
	if err := r.client.do(ctx, http.MethodGet, "/networking/vpcs", nil, &items); err != nil {
		return nil, err
	}
	if items == nil {
		return nil, fmt.Errorf("reconciliation VPC list was not a non-null array")
	}
	var candidate *vpcAPI
	for _, item := range items {
		if item.identifier() == "" {
			return nil, fmt.Errorf("reconciliation list omitted VPC identity")
		}
		if before[item.identifier()] || item.Name != strings.TrimSpace(plan.Name.ValueString()) || item.SiteID != plan.SiteID.ValueString() {
			continue
		}
		if candidate != nil {
			return nil, fmt.Errorf("multiple new VPCs match the requested name and site")
		}
		copy := item
		candidate = &copy
	}
	if candidate == nil {
		return nil, nil
	}
	var detail vpcAPI
	if err := r.client.do(ctx, http.MethodGet, "/networking/vpcs/"+url.PathEscape(candidate.identifier()), nil, &detail); err != nil {
		return nil, err
	}
	if detail.identifier() != candidate.identifier() || detail.Name != strings.TrimSpace(plan.Name.ValueString()) || detail.SiteID != plan.SiteID.ValueString() || detail.Cidr == "" || detail.ConnectivityType == nil || *detail.ConnectivityType != plan.ConnectivityType.ValueString() {
		return nil, fmt.Errorf("new VPC detail did not match the requested identity and connectivity")
	}
	if detail.Description == nil || *detail.Description != strings.TrimSpace(plan.Description.ValueString()) {
		return nil, fmt.Errorf("new VPC description did not match this create request")
	}
	if !plan.Cidr.IsNull() && !plan.Cidr.IsUnknown() && plan.Cidr.ValueString() != detail.Cidr {
		return nil, fmt.Errorf("new VPC CIDR did not match this create request")
	}
	return &detail, nil
}

func (r *vpcResource) deleteOwnedVpcNAT(ctx context.Context, state vpcResourceModel) error {
	base := "/networking/vpcs/" + url.PathEscape(state.ID.ValueString()) + "/nat-gateways"
	var gateways []vpcNATGatewayAPI
	if err := r.client.do(ctx, http.MethodGet, base, nil, &gateways); err != nil {
		return err
	}
	if gateways == nil {
		return fmt.Errorf("API omitted NAT gateway inventory; no children were deleted")
	}
	ownedFound := false
	for _, gateway := range gateways {
		if gateway.ID == "" {
			return fmt.Errorf("NAT gateway inventory omitted identity; no children were deleted")
		}
		if gateway.ID != state.OwnedDefaultNATID.ValueString() {
			return fmt.Errorf("VPC contains a separately managed NAT gateway; import and destroy it separately before deleting the VPC")
		}
		ownedFound = true
	}
	if !ownedFound {
		return nil
	}
	item := base + "/" + url.PathEscape(state.OwnedDefaultNATID.ValueString())
	if err := checkNATChildrenEmpty(ctx, r.client, state.ID.ValueString(), state.OwnedDefaultNATID.ValueString()); err != nil {
		return err
	}
	if err := r.client.do(ctx, http.MethodDelete, item, nil, nil); err != nil && !IsNotFound(err) {
		return err
	}
	gateways = nil
	if err := r.client.do(ctx, http.MethodGet, base, nil, &gateways); err != nil {
		return err
	}
	if gateways == nil || len(gateways) != 0 {
		return fmt.Errorf("NAT gateway deletion did not result in an empty inventory; VPC state was preserved")
	}
	return nil
}

func checkNATChildrenEmpty(ctx context.Context, client *Client, vpcID, gatewayID string) error {
	base := "/networking/vpcs/" + url.PathEscape(vpcID)
	// The service downgrades attached VM allocations and cascades forwarding
	// rules during NAT deletion. Neither is owned by the gateway itself.
	var nodes []map[string]any
	if err := client.do(ctx, http.MethodGet, base+"/nodes", nil, &nodes); err != nil {
		return err
	}
	if nodes == nil {
		return fmt.Errorf("API omitted VM attachment inventory; refusing NAT deletion")
	}
	if len(nodes) > 0 {
		return fmt.Errorf("VPC still contains VM attachments; detach them before deleting its NAT gateway")
	}
	var rules []map[string]any
	if err := client.do(ctx, http.MethodGet, base+"/nat-gateways/"+url.PathEscape(gatewayID)+"/port-forwarding-rules", nil, &rules); err != nil {
		return err
	}
	if rules == nil {
		return fmt.Errorf("API omitted forwarding-rule inventory; refusing to delete the owned NAT gateway")
	}
	if len(rules) != 0 {
		return fmt.Errorf("owned NAT gateway still contains separately managed forwarding rules; remove them before deleting the VPC")
	}
	return nil
}
