package provider

import "fmt"

// computeBillingTerm is the term the VM read reports in its billing_catalog.
type computeBillingTerm struct {
	BillingInterval string `json:"billing_interval"`
}

var computeBillingTermFields = []string{"billing_interval", "committed", "commitment_period", "commitment_months", "committed_hours", "discount_percent", "price_unit", "unit_price_minor"}

// Temporary: VM create and resize compose the plan's billing_catalog until the API resolves the SKU itself.
// The chosen billing option's fields are merged in; an empty interval sends the catalog unchanged.
func (p *computePlan) selectBillingTerm(interval string) error {
	if interval == "" {
		return nil
	}
	options, _ := p.BillingCatalog["billing_options"].([]any)
	for _, raw := range options {
		option, ok := raw.(map[string]any)
		if !ok || option["billing_interval"] != interval {
			continue
		}
		catalog := make(map[string]any, len(p.BillingCatalog)+len(computeBillingTermFields))
		for k, v := range p.BillingCatalog {
			catalog[k] = v
		}
		for _, k := range computeBillingTermFields {
			delete(catalog, k)
			if v, ok := option[k]; ok && v != nil {
				catalog[k] = v
			}
		}
		p.BillingCatalog = catalog
		p.BillingInterval = interval
		return nil
	}
	return fmt.Errorf("plan %q lists no %s option in its billing_catalog.billing_options", p.PlanID, interval)
}
