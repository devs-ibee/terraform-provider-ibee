package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// SKU identifiers can be numeric. Default map decoding uses float64 and would
// corrupt identifiers larger than 2^53 during refresh/import.
type recoveryCatalog map[string]any

func (c *recoveryCatalog) UnmarshalJSON(raw []byte) error {
	if string(raw) == "null" {
		*c = nil
		return nil
	}
	value, err := decodeRecoveryCatalog(string(raw))
	if err == nil {
		*c = value
	}
	return err
}

// Temporary: snapshots, backups and attachments pass billing_catalog through unchanged until the API resolves these SKUs itself.
func recoveryCatalogAttribute(product string, immutable bool) schema.StringAttribute {
	behavior := "Changes are sent with the next policy update."
	if immutable {
		behavior = "Adding a previously absent value only records configuration. Changing a known value requires replacement."
	}
	return schema.StringAttribute{
		Optional: true, Computed: true,
		Description:   "Billing catalog for " + product + " as a JSON object (use jsonencode), passed to the API unchanged; the API decides whether it is valid. Attachments read it from the volume when omitted. Omitted values preserve prior state. " + behavior,
		Validators:    []validator.String{recoveryCatalogValidator{}},
		PlanModifiers: []planmodifier.String{recoveryCatalogPlan{immutable: immutable}},
	}
}

type recoveryCatalogValidator struct{}

func (recoveryCatalogValidator) Description(context.Context) string {
	return "must be one JSON object"
}
func (v recoveryCatalogValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (v recoveryCatalogValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if _, err := decodeRecoveryCatalog(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid billing catalog", err.Error())
	}
}

type recoveryCatalogPlan struct{ immutable bool }

func (recoveryCatalogPlan) Description(context.Context) string {
	return "Preserve an omitted catalog; changing a known immutable catalog requires replacement."
}
func (m recoveryCatalogPlan) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}
func (m recoveryCatalogPlan) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	if req.ConfigValue.IsNull() {
		if !req.State.Raw.IsNull() {
			resp.PlanValue = req.StateValue
		}
		return
	}
	if !req.State.Raw.IsNull() && !req.StateValue.IsNull() && !req.StateValue.IsUnknown() {
		// Unknown explicit inputs must be treated conservatively. Waiting until
		// apply to discover a changed immutable selection is too late to plan
		// replacement and would produce an inconsistent final plan.
		resp.RequiresReplace = m.immutable && (req.PlanValue.IsUnknown() || !recoveryCatalogSelectionUnchanged(req.StateValue, req.PlanValue))
	}
}

func decodeRecoveryCatalog(raw string) (map[string]any, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var catalog map[string]any
	if err := dec.Decode(&catalog); err != nil || catalog == nil {
		return nil, fmt.Errorf("billing_catalog must contain one JSON object")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("billing_catalog must contain one JSON object")
	}
	return catalog, nil
}

func recoveryCatalogEqual(a, b types.String) bool {
	if a.Equal(b) {
		return true
	}
	left, le := decodeRecoveryCatalog(a.ValueString())
	right, re := decodeRecoveryCatalog(b.ValueString())
	return le == nil && re == nil && reflect.DeepEqual(left, right)
}

// Read projections can enrich selections with default terms or metadata. An
// imported catalog narrowed to the same explicitly selected fields is a local
// state convergence, not authorization to repurchase or replace a resource.
func recoveryCatalogSelectionUnchanged(previous, next types.String) bool {
	if recoveryCatalogEqual(previous, next) {
		return true
	}
	prior, pe := decodeRecoveryCatalog(previous.ValueString())
	selected, se := decodeRecoveryCatalog(next.ValueString())
	return pe == nil && se == nil && recoveryCatalogContains(prior, selected)
}

// recoveryCatalogBody returns the configured catalog to send, or nil to omit it.
func recoveryCatalogBody(value types.String) (map[string]any, error) {
	if value.IsNull() || value.IsUnknown() {
		return nil, nil
	}
	return decodeRecoveryCatalog(value.ValueString())
}

// A read projection may omit the historical catalog. Preserve recorded inputs;
// imports without it remain readable and deletable. Prices never come from a VM plan.
func hydrateRecoveryCatalog(current *types.String, catalog map[string]any) error {
	if catalog == nil {
		if current.IsUnknown() {
			*current = types.StringNull()
		}
		return nil
	}
	encoded, err := json.Marshal(catalog)
	if err != nil {
		return fmt.Errorf("invalid billing catalog response")
	}
	if !current.IsNull() && !current.IsUnknown() {
		// The server may enrich the selection with default terms/metadata. Keep
		// the exact submitted selection when all of its fields are unchanged.
		prior, err := decodeRecoveryCatalog(current.ValueString())
		canonical, _ := decodeRecoveryCatalog(string(encoded))
		if err == nil && recoveryCatalogContains(canonical, prior) {
			return nil
		}
	}
	*current = types.StringValue(string(encoded))
	return nil
}

func recoveryCatalogContains(actual, selected map[string]any) bool {
	for key, value := range selected {
		if nested, ok := value.(map[string]any); ok {
			next, ok := actual[key].(map[string]any)
			if !ok || !recoveryCatalogContains(next, nested) {
				return false
			}
		} else if !reflect.DeepEqual(actual[key], value) {
			return false
		}
	}
	return true
}
