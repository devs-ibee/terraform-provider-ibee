package provider

// Regional load balancers (contract 0.11.0): one full specification per load balancer.
// Create, update and delete answer 202 with an operation; every change carries an
// Idempotency-Key. The service decides defaults, what it accepts and what each
// node-hour costs; the provider sends the specification and relays the answer.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const loadBalancersPath = "/networking/load-balancers"

// loadBalancerSpecFields are the LoadBalancerSpec fields the resource sends, in the
// API's names (the Terraform attributes have the same names).
var loadBalancerSpecFields = []string{"name", "region_id", "vpc_id", "scope", "traffic_type", "capacity_mode", "visibility", "node_count", "listeners", "target_groups", "logging"}

type loadBalancerResource struct{ client *Client }

func NewLoadBalancerResource() resource.Resource { return &loadBalancerResource{} }

func optionalComputedString(description string) schema.StringAttribute {
	return schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, Description: description}
}

func optionalComputedInt64(description string) schema.Int64Attribute {
	return schema.Int64Attribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}, Description: description}
}

func loadBalancerAttributes() map[string]schema.Attribute {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	return map[string]schema.Attribute{
		"id":            schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: "Load balancer ID."},
		"name":          schema.StringAttribute{Required: true, Description: "Load balancer name."},
		"region_id":     schema.StringAttribute{Required: true, Description: "Region, from the ibee_load_balancer_regions data source."},
		"vpc_id":        schema.StringAttribute{Optional: true, Description: "VPC whose private addresses the targets use."},
		"scope":         optionalComputedString("regional or global. The API default applies when omitted."),
		"traffic_type":  schema.StringAttribute{Required: true, Description: "http (layer 7) or network (layer 4)."},
		"capacity_mode": optionalComputedString("fixed or elastic. The API default applies when omitted."),
		"visibility":    optionalComputedString("external or internal. The API default applies when omitted."),
		"node_count":    optionalComputedInt64("Number of nodes; each is billed per hour. The API default applies when omitted."),
		"listeners": schema.ListNestedAttribute{Required: true, Description: "Ports the load balancer listens on.", NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"name":            schema.StringAttribute{Required: true},
			"protocol":        schema.StringAttribute{Required: true, Description: "HTTP, HTTPS, TCP or UDP."},
			"port":            schema.Int64Attribute{Required: true},
			"target_group":    schema.StringAttribute{Required: true, Description: "Name of a target group in target_groups."},
			"hostname":        schema.StringAttribute{Optional: true},
			"certificate_ref": schema.StringAttribute{Optional: true, Description: "Certificate reference for HTTPS. The provider never takes a private key."},
			"rules": schema.ListNestedAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()}, Description: "HTTP routing rules.", NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
				"target_group": schema.StringAttribute{Required: true},
				"path_prefix":  schema.StringAttribute{Optional: true},
				"headers":      schema.MapAttribute{Optional: true, ElementType: types.StringType},
			}}},
		}}},
		"target_groups": schema.ListNestedAttribute{Required: true, Description: "Groups of targets that listeners send traffic to.", NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"name":      schema.StringAttribute{Required: true},
			"protocol":  schema.StringAttribute{Required: true, Description: "HTTP, TCP or UDP."},
			"algorithm": optionalComputedString("round_robin or least_request. The API default applies when omitted."),
			"targets": schema.ListNestedAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()}, NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
				"kind":        schema.StringAttribute{Required: true, Description: "vm or ip."},
				"port":        schema.Int64Attribute{Required: true},
				"resource_id": schema.StringAttribute{Optional: true, Description: "VM ID, with kind vm."},
				"address":     schema.StringAttribute{Optional: true, Description: "Private IP address, with kind ip."},
			}}},
			"health_check": schema.SingleNestedAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.Object{objectplanmodifier.UseStateForUnknown()}, Attributes: map[string]schema.Attribute{
				"protocol":            schema.StringAttribute{Optional: true, Description: "HTTP or TCP."},
				"path":                schema.StringAttribute{Optional: true},
				"interval_seconds":    schema.Int64Attribute{Optional: true},
				"timeout_seconds":     schema.Int64Attribute{Optional: true},
				"unhealthy_threshold": schema.Int64Attribute{Optional: true},
				"healthy_threshold":   schema.Int64Attribute{Optional: true},
			}},
		}}},
		"logging": schema.SingleNestedAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.Object{objectplanmodifier.UseStateForUnknown()}, Attributes: map[string]schema.Attribute{
			"enabled":    schema.BoolAttribute{Optional: true},
			"dataset_id": schema.StringAttribute{Optional: true},
		}},
		"version":        schema.Int64Attribute{Computed: true, Description: "Specification version the API last applied; sent back on update."},
		"managed_by":     schema.StringAttribute{Computed: true, Description: "Who manages the load balancer. Ones a Kubernetes cluster's cloud controller made are changed through the Kubernetes Service, not here."},
		"observed_state": schema.StringAttribute{Computed: true, Description: "State the service observes."},
	}
}

func (r *loadBalancerResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_load_balancer"
}

func (r *loadBalancerResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A regional load balancer, declared as one specification. Create and update send the whole specification (update with the API's current version) and wait for the operation to finish; destroy deletes the load balancer and waits. The service decides defaults, what it accepts and the node-hour price, and the provider relays its answer. Targets are private addresses or VMs in your VPC. Import with the load balancer ID." + networkBillingDescription,
		Attributes:  loadBalancerAttributes(),
	}
}

func (r *loadBalancerResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResourceClient(req, resp)
}

func (r *loadBalancerResource) attributeTypes() map[string]attr.Type {
	types := map[string]attr.Type{}
	for name, attribute := range loadBalancerAttributes() {
		types[name] = attribute.GetType()
	}
	return types
}

// spec is the LoadBalancerSpec the configuration describes.
func (r *loadBalancerResource) spec(values map[string]attr.Value) (map[string]any, error) {
	spec := map[string]any{}
	for _, field := range loadBalancerSpecFields {
		value := values[field]
		if value == nil || value.IsNull() || value.IsUnknown() {
			continue
		}
		raw, err := networkJSON(value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", field, err)
		}
		spec[field] = raw
	}
	return spec, nil
}

// read returns the load balancer's attributes, or false when it is gone.
func (r *loadBalancerResource) read(ctx context.Context, id string) (map[string]attr.Value, bool, error) {
	var out map[string]any
	if err := r.client.do(ctx, http.MethodGet, loadBalancersPath+"/"+url.PathEscape(id), nil, &out); err != nil {
		if IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if deleted, _ := out["deleted_at"].(string); deleted != "" {
		return nil, false, nil
	}
	spec, _ := out["spec"].(map[string]any)
	values := map[string]attr.Value{}
	for name, t := range r.attributeTypes() {
		var raw any
		switch name {
		case "listeners", "target_groups", "logging":
			raw = spec[name]
		default:
			raw = out[name]
		}
		value, err := networkValue(t, raw)
		if err != nil {
			return nil, false, fmt.Errorf("read %s: %w", name, err)
		}
		values[name] = value
	}
	return values, true, nil
}

// waitOperation polls GET /networking/load-balancers/operations/{id} until it ends.
func (r *loadBalancerResource) waitOperation(ctx context.Context, accepted map[string]any) error {
	operationID := findString(accepted, "id")
	if operationID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	for {
		var op struct {
			Status string  `json:"status"`
			Reason *string `json:"reason"`
		}
		if err := r.client.do(ctx, http.MethodGet, loadBalancersPath+"/operations/"+url.PathEscape(operationID), nil, &op); err != nil {
			return fmt.Errorf("read load balancer operation %s: %w", operationID, err)
		}
		switch strings.ToLower(op.Status) {
		case "succeeded", "completed", "done":
			return nil
		case "failed", "cancelled", "canceled", "rejected", "error":
			reason := ""
			if op.Reason != nil {
				reason = *op.Reason
			}
			return fmt.Errorf("load balancer operation %s %s: %s", operationID, op.Status, reason)
		}
		if err := waitContext(ctx, r.client.pollDelay()); err != nil {
			return fmt.Errorf("load balancer operation %s did not finish: %w", operationID, err)
		}
	}
}

func (r *loadBalancerResource) mutate(ctx context.Context, method, requestPath string, body any) (map[string]any, error) {
	var accepted map[string]any
	if err := r.client.doH(ctx, method, requestPath, map[string]string{"Idempotency-Key": idempotencyKey()}, body, &accepted); err != nil {
		return nil, err
	}
	return accepted, r.waitOperation(ctx, accepted)
}

func (r *loadBalancerResource) setFromAPI(ctx context.Context, id string, diags interface {
	AddError(string, string)
}, set func(map[string]attr.Value)) {
	values, found, err := r.read(ctx, id)
	if err != nil || !found {
		diags.AddError("Failed to read load balancer", fmt.Sprint(err))
		return
	}
	set(values)
}

func (r *loadBalancerResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan types.Object
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	spec, err := r.spec(plan.Attributes())
	if err != nil {
		resp.Diagnostics.AddError("Invalid load balancer specification", err.Error())
		return
	}
	accepted, err := r.mutate(ctx, http.MethodPost, loadBalancersPath, spec)
	id := findString(accepted, "lb_id")
	if err != nil {
		if id != "" {
			resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
		}
		resp.Diagnostics.AddError("Failed to create load balancer", err.Error())
		return
	}
	if id == "" {
		resp.Diagnostics.AddError("Invalid created load balancer response", "The operation carried no lb_id.")
		return
	}
	r.setFromAPI(ctx, id, &resp.Diagnostics, func(values map[string]attr.Value) {
		resp.Diagnostics.Append(resp.State.Set(ctx, types.ObjectValueMust(r.attributeTypes(), values))...)
	})
}

func (r *loadBalancerResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state types.Object
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.Attributes()["id"].(types.String).ValueString()
	values, found, err := r.read(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read load balancer", err.Error())
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, types.ObjectValueMust(r.attributeTypes(), values))...)
}

func (r *loadBalancerResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state types.Object
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.Attributes()["id"].(types.String).ValueString()
	spec, err := r.spec(plan.Attributes())
	if err != nil {
		resp.Diagnostics.AddError("Invalid load balancer specification", err.Error())
		return
	}
	body := map[string]any{"version": state.Attributes()["version"].(types.Int64).ValueInt64(), "specification": spec}
	if _, err := r.mutate(ctx, http.MethodPut, loadBalancersPath+"/"+url.PathEscape(id), body); err != nil {
		resp.Diagnostics.AddError("Failed to update load balancer", err.Error())
		return
	}
	r.setFromAPI(ctx, id, &resp.Diagnostics, func(values map[string]attr.Value) {
		resp.Diagnostics.Append(resp.State.Set(ctx, types.ObjectValueMust(r.attributeTypes(), values))...)
	})
}

func (r *loadBalancerResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state types.Object
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.Attributes()["id"].(types.String).ValueString()
	if _, err := r.mutate(ctx, http.MethodDelete, loadBalancersPath+"/"+url.PathEscape(id), nil); err != nil && !IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to delete load balancer", err.Error())
	}
}

func (r *loadBalancerResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	values, found, err := r.read(ctx, req.ID)
	if err != nil || !found {
		resp.Diagnostics.AddError("Failed to import load balancer", fmt.Sprintf("load balancer %s: %v", req.ID, err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, types.ObjectValueMust(r.attributeTypes(), values))...)
}

// ---------------------------------------------------------------- ibee_load_balancer_regions

type loadBalancerRegionsDataSource struct{ client *Client }

func NewLoadBalancerRegionsDataSource() datasource.DataSource {
	return &loadBalancerRegionsDataSource{}
}

func (d *loadBalancerRegionsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_load_balancer_regions"
}

func (d *loadBalancerRegionsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dschema.Schema{
		Description: "Regions where load balancers can be created, as the API lists them.",
		Attributes: map[string]dschema.Attribute{
			"ids":     dschema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "Region IDs, for region_id of ibee_load_balancer."},
			"regions": dschema.ListAttribute{Computed: true, ElementType: types.MapType{ElemType: types.StringType}, Description: "Each region's fields as the API returns them, as strings."},
		},
	}
}

func (d *loadBalancerRegionsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *loadBalancerRegionsDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	var regions []map[string]any
	if err := d.client.do(ctx, http.MethodGet, loadBalancersPath+"/regions", nil, &regions); err != nil {
		resp.Diagnostics.AddError("Failed to list load balancer regions", err.Error())
		return
	}
	ids := []string{}
	rows := []map[string]string{}
	for _, region := range regions {
		row := map[string]string{}
		for key, value := range region {
			if value != nil {
				row[key] = fmt.Sprint(value)
			}
		}
		if id := findString(region, "id", "region_id"); id != "" {
			ids = append(ids, id)
		}
		rows = append(rows, row)
	}
	idList, diags := types.ListValueFrom(ctx, types.StringType, ids)
	resp.Diagnostics.Append(diags...)
	rowList, diags := types.ListValueFrom(ctx, types.MapType{ElemType: types.StringType}, rows)
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("ids"), idList)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("regions"), rowList)...)
}
