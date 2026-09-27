package provider

// This adapter implements the common lifecycle of the public networking APIs.
// Resource definitions below are intentionally limited to fields that the API
// can read back; undocumented portal-only endpoints are never used.
import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

type networkValues map[string]attr.Value

func (v networkValues) str(k string) string {
	if s, ok := v[k].(types.String); ok {
		return s.ValueString()
	}
	return ""
}
func (v networkValues) segment(k string) string { return url.PathEscape(v.str(k)) }

type networkResource struct {
	client            *Client
	name, description string
	attributes        map[string]schema.Attribute
	// requestFields and responseFields map Terraform attributes to JSON fields.
	requestFields, responseFields                map[string]string
	createPath, readPath, updatePath, deletePath func(networkValues) string
	idField                                      string
	readIdentity                                 string
	importFields                                 []string
	list                                         bool
	// listIdentity is the input attribute used to find an entry in a list.
	listIdentity          string
	listPage              bool
	billable              bool
	waitReady, waitDelete bool
	createResultID        func(networkValues, map[string]any) (string, error)
	readTransform         func(networkValues, map[string]any) (map[string]any, error)
	beforeDelete          func(context.Context, *Client, networkValues) error
	beforeCreate          func(context.Context, *Client, networkValues) error
	validate              func(networkValues) error
	deleteMethod          string
	createMethod          string
	updateMethod          string
	createOnlyFields      map[string]bool
	requestTransform      func(networkValues, map[string]any, bool) error
}

func (r *networkResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.name
}
func (r *networkResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{Description: r.description, Attributes: r.attributes}
}
func (r *networkResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	var ok bool
	r.client, ok = req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *Client, got %T", req.ProviderData))
	}
}
func (r *networkResource) attributeTypes() map[string]attr.Type {
	m := map[string]attr.Type{}
	for k, a := range r.attributes {
		m[k] = a.GetType()
	}
	return m
}
func (r *networkResource) set(ctx context.Context, state *tfsdk.State, v networkValues) diag.Diagnostics {
	return state.Set(ctx, types.ObjectValueMust(r.attributeTypes(), v))
}
func networkObject(v types.Object) networkValues { return networkValues(v.Attributes()) }

func (r *networkResource) body(v networkValues, update bool) (map[string]any, error) {
	b := map[string]any{}
	for a, j := range r.requestFields {
		if update && r.createOnlyFields[a] {
			continue
		}
		val := v[a]
		if val == nil || val.IsUnknown() {
			continue
		}
		// Null mutable values deliberately clear a previously configured field.
		if val.IsNull() {
			if update {
				b[j] = nil
			}
			continue
		}
		out, err := networkJSON(val)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", a, err)
		}
		b[j] = out
	}
	if r.requestTransform != nil {
		if err := r.requestTransform(v, b, update); err != nil {
			return nil, err
		}
	}
	return b, nil
}
func networkJSON(v attr.Value) (any, error) {
	if v.IsNull() {
		return nil, nil
	}
	if v.IsUnknown() {
		return nil, fmt.Errorf("value is unknown")
	}
	switch x := v.(type) {
	case types.String:
		return x.ValueString(), nil
	case types.Int64:
		return x.ValueInt64(), nil
	case types.Bool:
		return x.ValueBool(), nil
	case types.List:
		a := []any{}
		for _, v := range x.Elements() {
			j, e := networkJSON(v)
			if e != nil {
				return nil, e
			}
			a = append(a, j)
		}
		return a, nil
	case types.Map:
		m := map[string]any{}
		for k, v := range x.Elements() {
			j, e := networkJSON(v)
			if e != nil {
				return nil, e
			}
			m[k] = j
		}
		return m, nil
	case types.Object:
		m := map[string]any{}
		for k, v := range x.Attributes() {
			if v.IsNull() {
				continue
			}
			j, e := networkJSON(v)
			if e != nil {
				return nil, e
			}
			m[k] = j
		}
		return m, nil
	}
	return nil, fmt.Errorf("unsupported value type %T", v)
}
func networkValue(t attr.Type, raw any) (attr.Value, error) {
	switch x := t.(type) {
	case basetypes.StringType:
		if raw == nil {
			return types.StringNull(), nil
		}
		v, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("expected string, got %T", raw)
		}
		return types.StringValue(v), nil
	case basetypes.Int64Type:
		if raw == nil {
			return types.Int64Null(), nil
		}
		v, ok := raw.(float64)
		if !ok || float64(int64(v)) != v {
			return nil, fmt.Errorf("expected integer, got %v", raw)
		}
		return types.Int64Value(int64(v)), nil
	case basetypes.BoolType:
		if raw == nil {
			return types.BoolNull(), nil
		}
		v, ok := raw.(bool)
		if !ok {
			return nil, fmt.Errorf("expected boolean, got %T", raw)
		}
		return types.BoolValue(v), nil
	case types.ListType:
		if raw == nil {
			return types.ListNull(x.ElemType), nil
		}
		items, ok := raw.([]any)
		if !ok {
			return nil, fmt.Errorf("expected array, got %T", raw)
		}
		out := []attr.Value{}
		for _, item := range items {
			v, e := networkValue(x.ElemType, item)
			if e != nil {
				return nil, e
			}
			out = append(out, v)
		}
		return types.ListValueMust(x.ElemType, out), nil
	case types.MapType:
		if raw == nil {
			return types.MapNull(x.ElemType), nil
		}
		items, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("expected map, got %T", raw)
		}
		out := map[string]attr.Value{}
		for k, item := range items {
			v, e := networkValue(x.ElemType, item)
			if e != nil {
				return nil, e
			}
			out[k] = v
		}
		return types.MapValueMust(x.ElemType, out), nil
	case types.ObjectType:
		if raw == nil {
			return types.ObjectNull(x.AttrTypes), nil
		}
		items, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("expected object, got %T", raw)
		}
		out := map[string]attr.Value{}
		for k, t := range x.AttrTypes {
			v, e := networkValue(t, items[k])
			if e != nil {
				return nil, fmt.Errorf("%s: %w", k, e)
			}
			out[k] = v
		}
		return types.ObjectValueMust(x.AttrTypes, out), nil
	}
	return nil, fmt.Errorf("unsupported attribute type %T", t)
}
func (r *networkResource) merge(v networkValues, out map[string]any) error {
	for a, j := range r.responseFields {
		raw, exists := out[j]
		if (!exists || raw == nil) && r.attributes[a].IsRequired() {
			return fmt.Errorf("API response omitted required field %q", j)
		}
		value, err := networkValue(r.attributes[a].GetType(), raw)
		if err != nil {
			return fmt.Errorf("invalid %s: %w", j, err)
		}
		v[a] = value
	}
	// Request-only optional fields must be resolved even when omitted from config.
	for a, val := range v {
		if val.IsUnknown() {
			value, err := networkValue(r.attributes[a].GetType(), nil)
			if err != nil {
				return err
			}
			v[a] = value
		}
	}
	return nil
}
func (r *networkResource) fetch(ctx context.Context, v networkValues) (map[string]any, error) {
	p := r.readPath(v)
	if r.list {
		ident := v.str("id")
		if r.listIdentity != "" {
			ident = v.str(r.listIdentity)
		}
		for skip := 0; ; skip += 100 {
			target := p
			if r.listPage {
				target = fmt.Sprintf("%s?limit=100&skip=%d", p, skip)
			}
			var raw json.RawMessage
			if err := r.client.do(ctx, http.MethodGet, target, nil, &raw); err != nil {
				return nil, err
			}
			var items []map[string]any
			if len(raw) == 0 || string(raw) == "null" {
				return nil, fmt.Errorf("API returned an empty list response")
			}
			if err := json.Unmarshal(raw, &items); err != nil {
				return nil, fmt.Errorf("invalid list response: %w", err)
			}
			for _, out := range items {
				if id, ok := out[r.idField].(string); !ok || id == "" {
					return nil, fmt.Errorf("list entry omitted %s", r.idField)
				}
				if out[r.idField] == ident {
					return out, nil
				}
			}
			if !r.listPage || len(items) < 100 {
				break
			}
		}
		return nil, &apiError{Status: http.StatusNotFound, Body: "managed attachment or resource no longer exists"}
	}
	var out map[string]any
	if err := r.client.do(ctx, http.MethodGet, p, nil, &out); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, fmt.Errorf("API returned an empty object")
	}
	if id, ok := out[r.idField].(string); !ok || id == "" {
		return nil, fmt.Errorf("API response omitted %s", r.idField)
	}
	expectedID := v.str("id")
	if r.readIdentity != "" {
		expectedID = v.str(r.readIdentity)
	}
	if out[r.idField] != expectedID {
		return nil, fmt.Errorf("API returned %s %q for requested ID %q", r.idField, out[r.idField], expectedID)
	}
	if r.readTransform != nil {
		return r.readTransform(v, out)
	}
	return out, nil
}
func (r *networkResource) refresh(ctx context.Context, v networkValues, wait bool) error {
	ctx, cancel := context.WithTimeout(ctx, r.client.computeTimeout())
	defer cancel()
	for {
		out, err := r.fetch(ctx, v)
		if err != nil {
			return err
		}
		if err = r.merge(v, out); err != nil {
			return err
		}
		status, _ := out["status"].(string)
		if !wait {
			return nil
		}
		switch status {
		case "error", "failed":
			return fmt.Errorf("resource entered %s: %v", status, out["error_message"])
		case "provisioning", "deleting":
			if err = r.client.computePoll(ctx); err != nil {
				return err
			}
			continue
		case "available", "active":
			return nil
		default:
			return fmt.Errorf("unexpected networking readiness status %q", status)
		}
	}
}

func (r *networkResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var object types.Object
	resp.Diagnostics.Append(req.Plan.Get(ctx, &object)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v := networkObject(object)
	if r.validate != nil {
		if err := r.validate(v); err != nil {
			resp.Diagnostics.AddError("Invalid networking configuration", err.Error())
			return
		}
	}
	b, err := r.body(v, false)
	if err != nil {
		resp.Diagnostics.AddError("Invalid networking configuration", err.Error())
		return
	}
	if r.billable && (r.name != "vpc_node_attachment" || v.str("connectivity") != "private") {
		if err := r.client.requireBillingEligibility(ctx, "", nil); err != nil {
			resp.Diagnostics.AddError("Billing eligibility denied", err.Error())
			return
		}
	}
	if r.beforeCreate != nil {
		if err := r.beforeCreate(ctx, r.client, v); err != nil {
			resp.Diagnostics.AddError("Cannot safely create "+r.name, err.Error())
			return
		}
	}
	var out map[string]any
	method := r.createMethod
	if method == "" {
		method = http.MethodPost
	}
	if err = r.client.do(ctx, method, r.createPath(v), b, &out); err != nil {
		resp.Diagnostics.AddError("Failed to create "+r.name, err.Error())
		return
	}
	id, _ := out[r.idField].(string)
	if r.createResultID != nil {
		id, err = r.createResultID(v, out)
	}
	if err != nil || id == "" {
		resp.Diagnostics.AddError("Invalid create response", fmt.Sprintf("API did not identify the created %s (%v). Inspect the portal before retrying to avoid creating duplicates.", r.name, err))
		return
	}
	v["id"] = types.StringValue(id)
	// Save identity before polling so a failed operation remains recoverable.
	for a, val := range v {
		if val.IsUnknown() {
			v[a], _ = networkValue(r.attributes[a].GetType(), nil)
		}
	}
	resp.Diagnostics.Append(r.set(ctx, &resp.State, v)...)
	if err = r.refresh(ctx, v, r.waitReady); err != nil {
		resp.Diagnostics.Append(r.set(ctx, &resp.State, v)...)
		resp.Diagnostics.AddError("Failed to refresh created "+r.name, err.Error())
		return
	}
	resp.Diagnostics.Append(r.set(ctx, &resp.State, v)...)
}
func (r *networkResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var object types.Object
	resp.Diagnostics.Append(req.State.Get(ctx, &object)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v := networkObject(object)
	if err := r.refresh(ctx, v, false); err != nil {
		if IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read "+r.name, err.Error())
		return
	}
	resp.Diagnostics.Append(r.set(ctx, &resp.State, v)...)
}
func (r *networkResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var object types.Object
	resp.Diagnostics.Append(req.Plan.Get(ctx, &object)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v := networkObject(object)
	if r.updatePath == nil {
		resp.Diagnostics.AddError("Unsupported update", "This resource must be replaced.")
		return
	}
	if r.validate != nil {
		if err := r.validate(v); err != nil {
			resp.Diagnostics.AddError("Invalid networking configuration", err.Error())
			return
		}
	}
	b, err := r.body(v, true)
	if err != nil {
		resp.Diagnostics.AddError("Invalid update", err.Error())
		return
	}
	// Load-balancer topology updates can increase backend capacity; the contract
	// exposes no quote or SKU, so this is an account-status preflight only.
	if r.billable && strings.HasPrefix(r.name, "load_balancer_") {
		if err = r.client.requireBillingEligibility(ctx, "", nil); err != nil {
			resp.Diagnostics.AddError("Billing eligibility denied", err.Error())
			return
		}
	}
	method := r.updateMethod
	if method == "" {
		method = http.MethodPatch
	}
	if err = r.client.do(ctx, method, r.updatePath(v), b, nil); err != nil {
		resp.Diagnostics.AddError("Failed to update "+r.name, err.Error())
		return
	}
	if err = r.refresh(ctx, v, r.waitReady); err != nil {
		resp.Diagnostics.AddError("Failed to refresh updated "+r.name, err.Error())
		return
	}
	resp.Diagnostics.Append(r.set(ctx, &resp.State, v)...)
}
func (r *networkResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var object types.Object
	resp.Diagnostics.Append(req.State.Get(ctx, &object)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v := networkObject(object)
	if r.beforeDelete != nil {
		if err := r.beforeDelete(ctx, r.client, v); err != nil {
			if IsNotFound(err) {
				return
			}
			resp.Diagnostics.AddError("Cannot delete "+r.name, err.Error())
			return
		}
	}
	method := r.deleteMethod
	if method == "" {
		method = http.MethodDelete
	}
	if err := r.client.do(ctx, method, r.deletePath(v), nil, nil); err != nil {
		if IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to delete "+r.name, err.Error())
		return
	}
	if r.waitDelete {
		ctx, cancel := context.WithTimeout(ctx, r.client.computeTimeout())
		defer cancel()
		for {
			out, err := r.fetch(ctx, v)
			if IsNotFound(err) {
				return
			}
			if err != nil {
				resp.Diagnostics.AddError("Failed to confirm deletion", err.Error())
				return
			}
			if out["status"] == "deleted" {
				return
			}
			if out["status"] == "failed" || out["status"] == "error" {
				resp.Diagnostics.AddError("Deletion failed", fmt.Sprint(out["error_message"]))
				return
			}
			if err = r.client.computePoll(ctx); err != nil {
				resp.Diagnostics.AddError("Timed out waiting for deletion", err.Error())
				return
			}
		}
	}
}
func (r *networkResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != len(r.importFields) {
		resp.Diagnostics.AddError("Invalid import ID", "Expected "+strings.Join(r.importFields, "/"))
		return
	}
	for i, k := range r.importFields {
		if parts[i] == "" {
			resp.Diagnostics.AddError("Invalid import ID", "ID segments must be nonempty")
			return
		}
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(k), parts[i])...)
	}
	if !containsNetwork(r.importFields, "id") {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[len(parts)-1])...)
	}
}
func containsNetwork(a []string, v string) bool {
	for _, s := range a {
		if s == v {
			return true
		}
	}
	return false
}
func networkIDAttribute() schema.StringAttribute {
	return schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}}
}
func networkRequired(replace bool) schema.StringAttribute {
	a := schema.StringAttribute{Required: true}
	if replace {
		a.PlanModifiers = []planmodifier.String{stringplanmodifier.RequiresReplace()}
	}
	return a
}
func networkIdentityFields(names ...string) map[string]string {
	m := map[string]string{}
	for _, n := range names {
		m[n] = n
	}
	return m
}
