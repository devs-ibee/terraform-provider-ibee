package provider

import (
	"context"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const networkBillingDescription = " Mutations use upstream billing and lifecycle admission without client eligibility probes. Pricing, affordability, entitlement and quota enforcement remain authoritative on the server."

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
	r := &networkResource{name: "reserved_ip", description: "An independently reserved public IP. Attachments are managed separately. Import using the public IP ID." + networkBillingDescription, idField: "public_ip_id", importFields: []string{"id"},
		attributes:    map[string]schema.Attribute{"id": networkIDAttribute(), "site_id": networkRequired(true), "label": networkOptional(false), "reverse_dns": networkOptional(false), "address": schema.StringAttribute{Computed: true}, "status": schema.StringAttribute{Computed: true}},
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
	var out map[string]any
	body := map[string]any{"site_id": v.str("site_id")}
	if label := v["label"]; known(label) {
		body["label"] = v.str("label")
	}
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
	if reverse := v["reverse_dns"]; known(reverse) {
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
	r := &networkResource{name: "reserved_ip_attachment", description: "Attaches an existing reserved IP to a VM (POST .../attach). A change of vm_id, vpc_id or subnet_id is sent to the move operation. Destroy calls detach. Import as reserved_ip_id/vm_id.", idField: "public_ip_id", readIdentity: "reserved_ip_id", importFields: []string{"reserved_ip_id", "vm_id"},
		attributes:    map[string]schema.Attribute{"id": networkIDAttribute(), "reserved_ip_id": networkRequired(true), "vm_id": networkRequired(false), "vpc_id": schema.StringAttribute{Optional: true, Computed: true}, "subnet_id": schema.StringAttribute{Optional: true, Computed: true}},
		requestFields: networkIdentityFields("vm_id", "vpc_id", "subnet_id"), responseFields: map[string]string{"vm_id": "attached_resource_id", "vpc_id": "attached_vpc_id", "subnet_id": "attached_subnet_id"},
		createPath: func(v networkValues) string { return base(v) + "/attach" }, readPath: base, updatePath: func(v networkValues) string { return base(v) + "/move" }, updateMethod: http.MethodPost, deletePath: func(v networkValues) string { return base(v) + "/detach" }, deleteMethod: http.MethodPost,
		createResultID: func(v networkValues, _ map[string]any) (string, error) { return v.str("vm_id"), nil },
		readTransform: func(v networkValues, out map[string]any) (map[string]any, error) {
			// An IP attached to no VM, or to another one, means this attachment is gone.
			if out["attached_resource_id"] != v.str("vm_id") {
				return nil, &apiError{Status: http.StatusNotFound, Body: "reserved IP is no longer attached to the managed VM"}
			}
			return out, nil
		}}
	return r
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
	return &networkResource{name: "nat_gateway", description: "A NAT gateway in a VPC. A VPC created with connectivity_type nat_gateway already has one (ibee_vpc.default_nat_gateway_id); use this resource to import a gateway or to create one, and the API decides whether it is allowed. Import as vpc_id/nat_gateway_id." + networkBillingDescription, idField: "nat_gateway_id", list: true, waitReady: true, importFields: []string{"vpc_id", "id"},
		attributes:    map[string]schema.Attribute{"id": networkIDAttribute(), "vpc_id": networkRequired(true), "name": networkOptional(true), "subnet_id": networkOptionalReference(), "reserved_public_ip_id": networkOptionalReference(), "public_ip_id": schema.StringAttribute{Computed: true}, "public_ip": schema.StringAttribute{Computed: true}, "status": schema.StringAttribute{Computed: true}, "site_id": schema.StringAttribute{Computed: true}},
		requestFields: networkIdentityFields("name", "subnet_id", "reserved_public_ip_id"), responseFields: map[string]string{"name": "name", "subnet_id": "subnet_id", "public_ip_id": "public_ip_id", "public_ip": "public_ip", "status": "status", "site_id": "site_id", "reserved_public_ip_id": "public_ip_id"},
		createPath: base, readPath: base, deletePath: func(v networkValues) string { return base(v) + "/" + v.segment("id") },
	}
}
func NewNATPortForwardingRuleResource() resource.Resource {
	base := func(v networkValues) string { return networkNATPath(v) + "/port-forwarding-rules" }
	item := func(v networkValues) string { return base(v) + "/" + v.segment("id") }
	return &networkResource{name: "nat_port_forwarding_rule", description: "A NAT port-forwarding rule. Import as vpc_id/nat_gateway_id/port_forward_rule_id.", idField: "port_forward_rule_id", list: true, waitReady: true, importFields: []string{"vpc_id", "nat_gateway_id", "id"},
		attributes:    map[string]schema.Attribute{"id": networkIDAttribute(), "vpc_id": networkRequired(true), "nat_gateway_id": networkRequired(true), "name": networkRequired(false), "protocol": networkOptional(false), "external_port": schema.Int64Attribute{Required: true}, "internal_ip": networkRequired(false), "internal_port": schema.Int64Attribute{Required: true}, "note": networkOptional(false), "enabled": schema.BoolAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}}, "status": schema.StringAttribute{Computed: true}},
		requestFields: networkIdentityFields("name", "protocol", "external_port", "internal_ip", "internal_port", "note", "enabled"), responseFields: networkIdentityFields("name", "protocol", "external_port", "internal_ip", "internal_port", "note", "enabled", "status"),
		createPath: base, readPath: base, updatePath: item, deletePath: item}
}

func networkBackendAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"target": networkRequired(false), "port": schema.Int64Attribute{Required: true}, "type": networkOptional(false), "weight": schema.Int64Attribute{Optional: true, Computed: true}, "tls": schema.BoolAttribute{Optional: true, Computed: true},
	}
}
func NewL4LoadBalancerResource() resource.Resource { return newNetworkLB("l4") }
func NewL7LoadBalancerResource() resource.Resource { return newNetworkLB("l7") }
func newNetworkLB(layer string) resource.Resource {
	base := "/networking/load-balancers"
	attrs := map[string]schema.Attribute{"id": networkIDAttribute(), "name": networkRequired(false), "protocol": networkRequired(true), "backends": schema.ListNestedAttribute{Required: true, NestedObject: schema.NestedAttributeObject{Attributes: networkBackendAttributes()}}, "status": schema.StringAttribute{Computed: true}, "endpoint_url": schema.StringAttribute{Computed: true}, "url": schema.StringAttribute{Computed: true}, "endpoint": schema.SingleNestedAttribute{Computed: true, Attributes: map[string]schema.Attribute{"host": schema.StringAttribute{Computed: true}, "port": schema.Int64Attribute{Computed: true}}}}
	attrs["tls"] = schema.SingleNestedAttribute{Optional: true, PlanModifiers: []planmodifier.Object{objectplanmodifier.RequiresReplace()}, Description: "TLS settings of the listener, sent with create (the API has no read of them). A change replaces the load balancer.", Attributes: map[string]schema.Attribute{
		"mode":               schema.StringAttribute{Required: true, Description: "terminate or passthrough."},
		"certificate_source": schema.StringAttribute{Optional: true, Description: "managed or custom. Omit to use the API's default."},
		"cert_pem":           schema.StringAttribute{Optional: true, Sensitive: true, Description: "PEM certificate chain, with certificate_source custom."},
		"key_pem":            schema.StringAttribute{Optional: true, Sensitive: true, Description: "PEM private key, with certificate_source custom."},
	}}
	request := networkIdentityFields("name", "protocol", "backends", "tls")
	response := networkIdentityFields("name", "protocol", "backends", "status", "endpoint_url", "url", "endpoint")
	if layer == "l7" {
		attrs["rules"] = schema.ListNestedAttribute{Optional: true, Computed: true, NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{"priority": schema.Int64Attribute{Optional: true, Computed: true}, "path_prefix": networkOptional(false), "headers": schema.MapAttribute{Optional: true, ElementType: types.StringType}, "backends": schema.ListNestedAttribute{Optional: true, NestedObject: schema.NestedAttributeObject{Attributes: networkBackendAttributes()}}}}}
		request["rules"] = "rules"
		response["rules"] = "rules"
	}
	return &networkResource{name: "load_balancer_" + layer, description: "An IBEE " + layer + " load balancer. Import using the load balancer ID. Sends name, protocol, backends (and rules for l7); the API decides defaults and what it accepts. TLS, routing-policy and health-check settings are not managed by this resource." + networkBillingDescription,
		attributes: attrs, requestFields: request, responseFields: response, createOnlyFields: map[string]bool{"protocol": true, "tls": true}, idField: "lb_id", importFields: []string{"id"}, waitReady: true, waitDelete: true,
		createPath: networkPath(base + "/" + layer), readPath: func(v networkValues) string { return base + "/" + v.segment("id") }, updatePath: func(v networkValues) string { return base + "/" + layer + "/" + v.segment("id") }, deletePath: func(v networkValues) string { return base + "/" + v.segment("id") },
	}
}
