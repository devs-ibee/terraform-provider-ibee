package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
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

// These are purchase inputs, not a price quote. Preserve the complete canonical
// object, including optional pricing/term metadata; never synthesize a SKU or rate.
func recoveryCatalogAttribute(product string, immutable bool) schema.StringAttribute {
	behavior := "Explicit changes are sent as a replacement backup catalog after structural catalog checks; upstream decides admission."
	if immutable {
		behavior = "Adding a previously absent creation input only records configuration. Changing a known selection requires replacement."
	}
	return schema.StringAttribute{
		Optional: true, Computed: true,
		Description:   "Canonical " + product + " BillingCatalogSelection as JSON (use jsonencode). Supply sku_id and uppercase sku_code from an authoritative catalog, with matching product_code, site_id and currency when present. Required for new snapshots/backups; attachments resolve it from the volume when omitted. Prices are never invented. Omitted values preserve prior state. Legacy imports may omit this creation input. " + behavior,
		Validators:    []validator.String{recoveryCatalogValidator{product}},
		PlanModifiers: []planmodifier.String{recoveryCatalogPlan{immutable: immutable, resolveVolume: product == "block_storage"}},
	}
}

type recoveryCatalogValidator struct{ product string }

func (v recoveryCatalogValidator) Description(context.Context) string {
	return "must be canonical Billing catalog JSON for " + v.product
}
func (v recoveryCatalogValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (v recoveryCatalogValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if _, err := parseRecoveryCatalog(req.ConfigValue, v.product); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid billing catalog", err.Error())
	}
}

type recoveryCatalogPlan struct{ immutable, resolveVolume bool }

func (recoveryCatalogPlan) Description(context.Context) string {
	return "Preserve omitted purchase inputs; changing a known immutable catalog requires replacement."
}
func (m recoveryCatalogPlan) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}
func (m recoveryCatalogPlan) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	if req.ConfigValue.IsNull() {
		if m.resolveVolume && req.State.Raw.IsNull() {
			return
		}
		// A creation input may be absent on a legacy import, but it cannot be
		// absent when that resource will be created again. Catch this at plan
		// time, before Terraform can delete the existing recovery point.
		if !m.resolveVolume && (req.StateValue.IsNull() || req.StateValue.IsUnknown()) {
			creating := req.State.Raw.IsNull()
			if !creating {
				var planned, previous map[string]tftypes.Value
				if err := req.Plan.Raw.As(&planned); err != nil {
					resp.Diagnostics.AddError("Invalid catalog plan", "Cannot inspect planned purchase inputs.")
					return
				}
				if err := req.State.Raw.As(&previous); err != nil {
					resp.Diagnostics.AddError("Invalid catalog state", "Cannot inspect prior purchase inputs.")
					return
				}
				fields := []string{"vm_id"}
				if m.immutable {
					fields = append(fields, "name", "description", "mode", "selected_data_volume_ids")
				}
				for _, field := range fields {
					next, nextOK := planned[field]
					old, oldOK := previous[field]
					if nextOK && oldOK && !next.Equal(old) {
						creating = true
						break
					}
				}
			}
			if creating {
				resp.Diagnostics.AddAttributeError(path.Root("billing_catalog"), "Missing billing catalog for creation", "Supply an authoritative billing_catalog before creating or replacing this resource. A legacy resource without a catalog can remain unchanged or be destroyed, but cannot safely be replaced without its required purchase input.")
				return
			}
		}
		resp.PlanValue = req.StateValue
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

var recoveryCurrency = regexp.MustCompile(`^[A-Z]{3}$`)

func parseRecoveryCatalog(value types.String, product string) (map[string]any, error) {
	if value.IsNull() || value.IsUnknown() {
		return nil, fmt.Errorf("billing_catalog is required before creation; supply the canonical %s SKU from an existing resource in the same site/currency or obtain it from IBEE. The public API cannot list snapshot/backup catalogs; do not invent IDs or prices", product)
	}
	catalog, err := decodeRecoveryCatalog(value.ValueString())
	if err != nil {
		return nil, err
	}
	if err := validateRecoveryCatalog(catalog, product); err != nil {
		return nil, err
	}
	return catalog, nil
}

func validateRecoveryCatalog(catalog map[string]any, product string) error {
	// Require canonical keys, so alias collisions cannot change what is purchased.
	for _, alias := range []string{"skuId", "skuCode", "id", "code", "productCode", "siteId", "price_currency", "attachedSkus", "linked_skus", "linkedSkus", "add_ons", "addons", "addOns", "linkedAddOns"} {
		if _, ok := catalog[alias]; ok {
			return fmt.Errorf("billing_catalog must use canonical snake_case fields, not %s", alias)
		}
	}
	switch id := catalog["sku_id"].(type) {
	case string:
		if id == "" || strings.TrimSpace(id) != id {
			return fmt.Errorf("billing_catalog.sku_id must be nonblank without surrounding whitespace")
		}
	case json.Number:
		if _, ok := new(big.Rat).SetString(string(id)); !ok {
			return fmt.Errorf("billing_catalog.sku_id must be a valid identifier")
		}
	default:
		return fmt.Errorf("billing_catalog.sku_id must be a string or numeric identifier")
	}
	code, ok := catalog["sku_code"].(string)
	if !ok || code == "" || len(code) > 64 || code != strings.TrimSpace(code) || code != strings.ToUpper(code) || strings.HasPrefix(code, "ROOTDISK-") {
		return fmt.Errorf("billing_catalog.sku_code must be a canonical uppercase SKU, at most 64 characters; root disks are included in the VM plan")
	}
	if p, exists := catalog["product_code"]; exists && p != nil && p != product {
		return fmt.Errorf("billing_catalog.product_code must match %s", product)
	}
	for _, key := range []string{"site_id", "currency"} {
		if raw, exists := catalog[key]; exists && raw != nil {
			s, ok := raw.(string)
			if !ok || s == "" || s != strings.TrimSpace(s) {
				return fmt.Errorf("billing_catalog.%s must be nonblank canonical text", key)
			}
			if key == "currency" && !recoveryCurrency.MatchString(s) {
				return fmt.Errorf("billing_catalog.currency must be a three-letter uppercase currency code")
			}
		}
	}
	for _, key := range []string{"unit_price_minor", "hourly_price_minor", "monthly_price_minor", "yearly_price_minor"} {
		if raw, exists := catalog[key]; exists && raw != nil {
			n, ok := raw.(json.Number)
			rat, valid := new(big.Rat).SetString(string(n))
			if !ok || !valid || rat.Sign() < 0 || !rat.IsInt() {
				return fmt.Errorf("billing_catalog.%s must be a nonnegative integer in minor currency units", key)
			}
		}
	}
	if attached, exists := catalog["attached_skus"]; exists && attached != nil {
		items, ok := attached.(map[string]any)
		if !ok {
			return fmt.Errorf("billing_catalog.attached_skus must be an object")
		}
		for key, raw := range items {
			if key == "" || key != strings.ToLower(strings.ReplaceAll(strings.TrimSpace(key), "-", "_")) {
				return fmt.Errorf("attached_skus keys must be canonical snake_case")
			}
			switch key {
			case "rootdisk", "root_disk", "root_disk_storage", "rootvolume", "root_volume", "root_storage":
				return fmt.Errorf("root disk must not be sent as an attached SKU")
			}
			sku, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("attached_skus.%s must be a SKU object", key)
			}
			p, _ := sku["product_code"].(string)
			if err := validateRecoveryCatalog(sku, p); err != nil {
				return fmt.Errorf("attached_skus.%s: %w", key, err)
			}
		}
	}
	return nil
}

// Site checks use the actual target VM, not an inferred SKU/site naming rule.
func validateRecoveryCatalogSite(catalog map[string]any, site string) error {
	if selected, _ := catalog["site_id"].(string); selected != "" && selected != site {
		return fmt.Errorf("billing_catalog.site_id does not match the VM site (or the VM omitted its site); obtain a matching canonical catalog")
	}
	return nil
}

func prepareRecoveryCatalog(ctx context.Context, client *Client, kind, vmID string, value types.String, product string) (map[string]any, error) {
	catalog, err := parseRecoveryCatalog(value, product)
	if err != nil {
		return nil, err
	}
	if site, _ := catalog["site_id"].(string); site != "" {
		var vm cloudVmAPI
		if err := client.do(ctx, http.MethodGet, "/compute/"+kind+"-vms/"+url.PathEscape(vmID), nil, &vm); err != nil {
			return nil, err
		}
		if vm.identifier() != vmID {
			return nil, fmt.Errorf("VM response identity does not match catalog target")
		}
		vmSite := ""
		if vm.SiteID != nil {
			vmSite = *vm.SiteID
		}
		if err := validateRecoveryCatalogSite(catalog, vmSite); err != nil {
			return nil, err
		}
	}
	return catalog, nil
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
