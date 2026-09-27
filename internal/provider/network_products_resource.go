package provider

import (
	"context"
	"fmt"
	"net"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const networkBillingDescription = " Before purchase the provider checks account billing status. This API exposes no catalog SKU or quote; product pricing, affordability, entitlement and quota enforcement remain authoritative on the server."

func networkOptionalString(def string, replace bool) schema.StringAttribute {
	a := schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(def)}
	if replace {
		a.PlanModifiers = []planmodifier.String{stringplanmodifier.RequiresReplace()}
	}
	return a
}
func networkOptionalReference() schema.StringAttribute {
	return schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()}}
}
func networkPath(p string) func(networkValues) string { return func(networkValues) string { return p } }
func networkVpcPath(v networkValues) string           { return "/networking/vpcs/" + v.segment("vpc_id") }
func networkNATPath(v networkValues) string {
	return networkVpcPath(v) + "/nat-gateways/" + v.segment("nat_gateway_id")
}
func networkReservedPath(v networkValues) string {
	return "/networking/reserved-ips/" + v.segment("id")
}

func NewReservedIPResource() resource.Resource {
	r := &networkResource{name: "reserved_ip", description: "An independently reserved public IP. Attachments are managed separately. Import using the public IP ID." + networkBillingDescription, billable: true, idField: "public_ip_id", importFields: []string{"id"},
		attributes:    map[string]schema.Attribute{"id": networkIDAttribute(), "site_id": networkRequired(true), "label": networkOptionalString("", false), "reverse_dns": networkOptionalString("", false), "address": schema.StringAttribute{Computed: true}, "status": schema.StringAttribute{Computed: true}},
		requestFields: networkIdentityFields("site_id", "label", "reverse_dns"), responseFields: networkIdentityFields("site_id", "label", "reverse_dns", "address", "status"), createOnlyFields: map[string]bool{"site_id": true},
		createPath: networkPath("/networking/reserved-ips"), readPath: networkReservedPath, updatePath: networkReservedPath, deletePath: networkReservedPath}
	// reverse_dns is only supported by PATCH, not ReserveIpRequest.
	r.createResultID = func(v networkValues, out map[string]any) (string, error) {
		id, _ := out["public_ip_id"].(string)
		return id, nil
	}
	return &reservedIPResource{networkResource: r}
}

// Reverse DNS is an explicit second mutation after reserve. Retain the resource
// identity if that mutation fails so Terraform can retry the update safely.
type reservedIPResource struct{ *networkResource }

func (r *reservedIPResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var object types.Object
	resp.Diagnostics.Append(req.Plan.Get(ctx, &object)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v := networkObject(object)
	if err := r.client.requireBillingEligibility(ctx, "", nil); err != nil {
		resp.Diagnostics.AddError("Billing eligibility denied", err.Error())
		return
	}
	var out map[string]any
	body := map[string]any{"site_id": v.str("site_id"), "label": v.str("label")}
	if err := r.client.do(ctx, http.MethodPost, "/networking/reserved-ips", body, &out); err != nil {
		resp.Diagnostics.AddError("Failed to reserve IP", err.Error())
		return
	}
	id, _ := out["public_ip_id"].(string)
	if id == "" {
		resp.Diagnostics.AddError("Invalid reserve response", "Missing public_ip_id; inspect the portal before retrying.")
		return
	}
	v["id"] = types.StringValue(id)
	for k, a := range v {
		if a.IsUnknown() {
			v[k], _ = networkValue(r.attributes[k].GetType(), nil)
		}
	}
	resp.Diagnostics.Append(r.set(ctx, &resp.State, v)...)
	if v.str("reverse_dns") != "" {
		if err := r.client.do(ctx, http.MethodPatch, networkReservedPath(v), map[string]any{"reverse_dns": v.str("reverse_dns")}, nil); err != nil {
			resp.Diagnostics.AddError("IP reserved but reverse DNS update failed", err.Error())
			return
		}
	}
	if err := r.refresh(ctx, v, false); err != nil {
		resp.Diagnostics.AddError("Failed to read reserved IP", err.Error())
		return
	}
	resp.Diagnostics.Append(r.set(ctx, &resp.State, v)...)
}

func NewReservedIPAttachmentResource() resource.Resource {
	base := func(v networkValues) string { return "/networking/reserved-ips/" + v.segment("reserved_ip_id") }
	return &networkResource{name: "reserved_ip_attachment", description: "Attaches an existing reserved IP to a VM. Import as reserved_ip_id/vm_id. Detach refuses to affect an IP moved to another VM outside Terraform.", idField: "public_ip_id", readIdentity: "reserved_ip_id", importFields: []string{"reserved_ip_id", "vm_id"},
		attributes:    map[string]schema.Attribute{"id": networkIDAttribute(), "reserved_ip_id": networkRequired(true), "vm_id": networkRequired(true), "vpc_id": networkOptionalReference(), "subnet_id": networkOptionalReference()},
		requestFields: networkIdentityFields("vm_id", "vpc_id", "subnet_id"), responseFields: map[string]string{"vm_id": "attached_resource_id", "vpc_id": "attached_vpc_id", "subnet_id": "attached_subnet_id"},
		createPath: func(v networkValues) string { return base(v) + "/attach" }, readPath: base, deletePath: func(v networkValues) string { return base(v) + "/detach" }, deleteMethod: http.MethodPost,
		createResultID: func(v networkValues, _ map[string]any) (string, error) { return v.str("vm_id"), nil },
		readTransform: func(v networkValues, out map[string]any) (map[string]any, error) {
			if _, ok := out["attached_resource_id"]; !ok {
				return nil, fmt.Errorf("API omitted attached_resource_id; cannot determine attachment ownership")
			}
			if out["attached_resource_id"] != v.str("vm_id") {
				return nil, &apiError{Status: http.StatusNotFound, Body: "reserved IP is no longer attached to the managed VM"}
			}
			return out, nil
		},
		beforeDelete: func(ctx context.Context, c *Client, v networkValues) error {
			var out map[string]any
			if err := c.do(ctx, http.MethodGet, base(v), nil, &out); err != nil {
				return err
			}
			if out["public_ip_id"] != v.str("reserved_ip_id") {
				return fmt.Errorf("API returned another reserved IP; refusing detach")
			}
			if _, ok := out["attached_resource_id"]; !ok {
				return fmt.Errorf("API omitted attached_resource_id; refusing detach")
			}
			if out["attached_resource_id"] == nil || out["attached_resource_id"] == "" {
				return &apiError{Status: 404, Body: "already detached"}
			}
			if out["attached_resource_id"] != v.str("vm_id") {
				return fmt.Errorf("reserved IP is attached to another VM; refresh state before destroying")
			}
			return nil
		},
		validate: func(v networkValues) error {
			if (v.str("vpc_id") == "") != (v.str("subnet_id") == "") {
				return fmt.Errorf("vpc_id and subnet_id must be supplied together")
			}
			return nil
		}}
}

func NewFirewallAttachmentResource() resource.Resource {
	base := func(v networkValues) string {
		return "/networking/firewall-groups/" + v.segment("firewall_group_id") + "/attachments"
	}
	return &networkResource{name: "firewall_attachment", description: "Attaches a VM to a firewall group. Import as firewall_group_id/vm_id.", idField: "vm_id", listIdentity: "vm_id", list: true, listPage: true, importFields: []string{"firewall_group_id", "vm_id"},
		attributes:    map[string]schema.Attribute{"id": networkIDAttribute(), "firewall_group_id": networkRequired(true), "vm_id": networkRequired(true), "network_id": schema.StringAttribute{Computed: true}},
		requestFields: networkIdentityFields("vm_id"), responseFields: networkIdentityFields("vm_id", "network_id"), createPath: base, readPath: base, deletePath: func(v networkValues) string { return base(v) + "/" + v.segment("vm_id") },
		createResultID: func(v networkValues, _ map[string]any) (string, error) { return v.str("vm_id"), nil }}
}
func NewNATGatewayResource() resource.Resource {
	base := func(v networkValues) string { return networkVpcPath(v) + "/nat-gateways" }
	return &networkResource{name: "nat_gateway", description: "A NAT gateway in a VPC. Import as vpc_id/nat_gateway_id. Manage port-forwarding rules separately." + networkBillingDescription, idField: "nat_gateway_id", list: true, billable: true, waitReady: true, importFields: []string{"vpc_id", "id"},
		attributes:    map[string]schema.Attribute{"id": networkIDAttribute(), "vpc_id": networkRequired(true), "name": networkOptionalString("NAT Gateway", true), "subnet_id": networkOptionalReference(), "reserved_public_ip_id": networkOptionalReference(), "public_ip_id": schema.StringAttribute{Computed: true}, "public_ip": schema.StringAttribute{Computed: true}, "status": schema.StringAttribute{Computed: true}, "site_id": schema.StringAttribute{Computed: true}},
		requestFields: networkIdentityFields("name", "subnet_id", "reserved_public_ip_id"), responseFields: map[string]string{"name": "name", "subnet_id": "subnet_id", "public_ip_id": "public_ip_id", "public_ip": "public_ip", "status": "status", "site_id": "site_id", "reserved_public_ip_id": "public_ip_id"},
		createPath: base, readPath: base, deletePath: func(v networkValues) string { return base(v) + "/" + v.segment("id") }}
}
func NewNATPortForwardingRuleResource() resource.Resource {
	base := func(v networkValues) string { return networkNATPath(v) + "/port-forwarding-rules" }
	item := func(v networkValues) string { return base(v) + "/" + v.segment("id") }
	return &networkResource{name: "nat_port_forwarding_rule", description: "A NAT port-forwarding rule. Import as vpc_id/nat_gateway_id/port_forward_rule_id.", idField: "port_forward_rule_id", list: true, waitReady: true, importFields: []string{"vpc_id", "nat_gateway_id", "id"},
		attributes:    map[string]schema.Attribute{"id": networkIDAttribute(), "vpc_id": networkRequired(true), "nat_gateway_id": networkRequired(true), "name": networkRequired(false), "protocol": networkOptionalString("tcp", false), "external_port": schema.Int64Attribute{Required: true}, "internal_ip": networkRequired(false), "internal_port": schema.Int64Attribute{Required: true}, "note": networkOptionalString("", false), "enabled": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true)}, "status": schema.StringAttribute{Computed: true}},
		requestFields: networkIdentityFields("name", "protocol", "external_port", "internal_ip", "internal_port", "note", "enabled"), responseFields: networkIdentityFields("name", "protocol", "external_port", "internal_ip", "internal_port", "note", "enabled", "status"),
		createPath: base, readPath: base, updatePath: item, deletePath: item,
		validate: func(v networkValues) error {
			if v.str("protocol") != "tcp" && v.str("protocol") != "udp" {
				return fmt.Errorf("protocol must be tcp or udp")
			}
			for _, k := range []string{"external_port", "internal_port"} {
				n := v[k].(types.Int64).ValueInt64()
				if n < 1 || n > 65535 {
					return fmt.Errorf("%s must be between 1 and 65535", k)
				}
			}
			if net.ParseIP(v.str("internal_ip")) == nil {
				return fmt.Errorf("internal_ip must be an IP address")
			}
			return nil
		}}
}

func networkBackendAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"target": networkRequired(false), "port": schema.Int64Attribute{Required: true}, "type": networkOptionalString("service", false), "weight": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(100)}, "tls": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false)},
	}
}
func NewL4LoadBalancerResource() resource.Resource { return newNetworkLB("l4") }
func NewL7LoadBalancerResource() resource.Resource { return newNetworkLB("l7") }
func newNetworkLB(layer string) resource.Resource {
	base := "/networking/load-balancers"
	attrs := map[string]schema.Attribute{"id": networkIDAttribute(), "name": networkRequired(false), "protocol": networkRequired(true), "backends": schema.ListNestedAttribute{Required: true, NestedObject: schema.NestedAttributeObject{Attributes: networkBackendAttributes()}}, "status": schema.StringAttribute{Computed: true}, "endpoint_url": schema.StringAttribute{Computed: true}, "url": schema.StringAttribute{Computed: true}, "endpoint": schema.SingleNestedAttribute{Computed: true, Attributes: map[string]schema.Attribute{"host": schema.StringAttribute{Computed: true}, "port": schema.Int64Attribute{Computed: true}}}}
	request := networkIdentityFields("name", "protocol", "backends")
	response := networkIdentityFields("name", "protocol", "backends", "status", "endpoint_url", "url", "endpoint")
	if layer == "l7" {
		attrs["rules"] = schema.ListNestedAttribute{Optional: true, Computed: true, NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{"priority": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(1)}, "path_prefix": networkOptionalString("/", false), "headers": schema.MapAttribute{Optional: true, ElementType: types.StringType}, "backends": schema.ListNestedAttribute{Optional: true, NestedObject: schema.NestedAttributeObject{Attributes: networkBackendAttributes()}}}}}
		request["rules"] = "rules"
		response["rules"] = "rules"
	}
	return &networkResource{name: "load_balancer_" + layer, description: "An IBEE " + layer + " load balancer. Import using the load balancer ID. Supports readable backend and routing-rule fields. The public read contract does not expose custom-domain, routing-policy or TLS configuration, so those settings cannot yet be safely managed." + networkBillingDescription,
		attributes: attrs, requestFields: request, responseFields: response, createOnlyFields: map[string]bool{"protocol": true}, idField: "lb_id", importFields: []string{"id"}, billable: true, waitReady: true, waitDelete: true,
		createPath: networkPath(base + "/" + layer), readPath: func(v networkValues) string { return base + "/" + v.segment("id") }, updatePath: func(v networkValues) string { return base + "/" + layer + "/" + v.segment("id") }, deletePath: func(v networkValues) string { return base + "/" + v.segment("id") },
		readTransform: func(_ networkValues, out map[string]any) (map[string]any, error) {
			if out["layer"] != layer {
				return nil, fmt.Errorf("load balancer is %v, expected %s", out["layer"], layer)
			}
			return out, nil
		},
		validate: func(v networkValues) error {
			p := v.str("protocol")
			if (layer == "l4" && p != "tcp" && p != "tls_passthrough") || (layer == "l7" && p != "http" && p != "https") {
				return fmt.Errorf("unsupported %s protocol %q", layer, p)
			}
			if err := validateNetworkBackends(v["backends"], true); err != nil {
				return err
			}
			if rules, ok := v["rules"].(types.List); ok && !rules.IsUnknown() {
				for _, r := range rules.Elements() {
					a := r.(types.Object).Attributes()
					if err := validateNetworkBackends(a["backends"], false); err != nil {
						return err
					}
				}
			}
			return nil
		}}
}
func validateNetworkBackends(v attr.Value, required bool) error {
	b, ok := v.(types.List)
	if !ok || b.IsNull() {
		if required {
			return fmt.Errorf("at least one backend is required")
		}
		return nil
	}
	if required && len(b.Elements()) == 0 {
		return fmt.Errorf("at least one backend is required")
	}
	for _, entry := range b.Elements() {
		a := entry.(types.Object).Attributes()
		port := a["port"].(types.Int64).ValueInt64()
		if port < 1 || port > 65535 {
			return fmt.Errorf("backend port must be between 1 and 65535")
		}
		weight := a["weight"].(types.Int64).ValueInt64()
		if weight < 1 || weight > 1000 {
			return fmt.Errorf("backend weight must be between 1 and 1000")
		}
		kind := a["type"].(types.String).ValueString()
		if kind != "service" && kind != "ip" && kind != "hostname" {
			return fmt.Errorf("backend type must be service, ip or hostname")
		}
	}
	return nil
}
