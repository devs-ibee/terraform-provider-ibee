package provider

// computeBillingTerm is the term the VM read reports in its billing_catalog.
type computeBillingTerm struct {
	BillingInterval string `json:"billing_interval"`
}

// computeBillingTermFields are the term fields of a billing_options entry.
// They are cleared from the copied catalog before the chosen entry is merged
// in, so no term of another option is sent.
var computeBillingTermFields = []string{"billing_interval", "committed", "commitment_period", "commitment_months", "committed_hours", "discount_percent", "price_unit", "unit_price_minor"}

// selectBillingTerm builds the ComputeBillingSelection VM create sends: a copy
// of the plan's billing_catalog (which keeps price_currency) with the whole
// billing_options entry for interval merged in. Every value comes from the
// catalog; the API validates the selection. An empty interval, or one the
// catalog lists no option for, leaves the catalog unchanged.
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
		catalog := make(map[string]any, len(p.BillingCatalog)+len(option))
		for k, v := range p.BillingCatalog {
			catalog[k] = v
		}
		for _, k := range computeBillingTermFields {
			delete(catalog, k)
		}
		for k, v := range option {
			if v != nil {
				catalog[k] = v
			}
		}
		p.BillingCatalog = catalog
		p.BillingInterval = interval
		return nil
	}
	// No matching option: the catalog is sent unchanged and the API answers.
	return nil
}
