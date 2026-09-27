package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// A schema default runs before prior state is considered, so it must not be used
// for this immutable contractual field: it could replace imported monthly VMs.
type computeBillingIntervalDefault struct{}

func (computeBillingIntervalDefault) Description(context.Context) string {
	return "Preserve the existing billing term when omitted; use HOURLY for a new VM."
}
func (m computeBillingIntervalDefault) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}
func (computeBillingIntervalDefault) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if !req.ConfigValue.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	if !req.StateValue.IsNull() && !req.StateValue.IsUnknown() {
		resp.PlanValue = req.StateValue
		return
	}
	resp.PlanValue = types.StringValue("HOURLY")
}

type computeBillingTerm struct {
	BillingInterval  string   `json:"billing_interval"`
	Committed        *bool    `json:"committed"`
	CommitmentPeriod string   `json:"commitment_period"`
	CommitmentMonths *int64   `json:"commitment_months"`
	CommittedHours   *int64   `json:"committed_hours"`
	UnitPriceMinor   *int64   `json:"unit_price_minor"`
	PriceUnit        string   `json:"price_unit"`
	DiscountPercent  *float64 `json:"discount_percent"`
}

// selectBillingTerm mirrors portal billingCatalogForTerm. Catalog display prices
// alone do not select a purchased term, and a monthly headline is not an hourly quote.
func (p *computePlan) selectBillingTerm(interval string) error {
	if interval != "HOURLY" && interval != "MONTHLY" {
		return fmt.Errorf("unsupported VM billing interval %q", interval)
	}
	raw, err := json.Marshal(p.BillingCatalog["billing_options"])
	if err != nil {
		return fmt.Errorf("invalid billing options: %w", err)
	}
	var options []computeBillingTerm
	if err := json.Unmarshal(raw, &options); err != nil {
		return fmt.Errorf("invalid billing options: %w", err)
	}
	var selected *computeBillingTerm
	for i := range options {
		if options[i].BillingInterval == interval {
			if selected != nil {
				return fmt.Errorf("catalog contains duplicate %s billing terms", interval)
			}
			selected = &options[i]
		}
	}
	if selected == nil {
		return fmt.Errorf("plan %q does not advertise a canonical %s billing option", p.PlanID, interval)
	}
	term := *selected
	cost, err := term.periodCost()
	if err != nil {
		return fmt.Errorf("plan %q: %w", p.PlanID, err)
	}
	catalog := make(map[string]any, len(p.BillingCatalog)+8)
	for k, v := range p.BillingCatalog {
		catalog[k] = v
	}
	for _, k := range []string{"billing_interval", "committed", "commitment_period", "commitment_months", "committed_hours", "discount_percent", "price_unit", "unit_price_minor"} {
		delete(catalog, k)
	}
	catalog["billing_interval"] = term.BillingInterval
	catalog["committed"] = *term.Committed
	catalog["commitment_period"] = term.CommitmentPeriod
	catalog["unit_price_minor"] = *term.UnitPriceMinor
	if term.CommitmentMonths != nil {
		catalog["commitment_months"] = *term.CommitmentMonths
	}
	if term.CommittedHours != nil {
		catalog["committed_hours"] = *term.CommittedHours
	}
	if term.DiscountPercent != nil {
		catalog["discount_percent"] = *term.DiscountPercent
	}
	if term.PriceUnit != "" {
		catalog["price_unit"] = term.PriceUnit
	}
	p.BillingCatalog = catalog
	p.BillingInterval = interval
	p.SelectedTermCostMinor = &cost
	return nil
}

// The same selected-contract validation is used on creation and canonical reads.
// In particular MONTHLY is not enough to distinguish a one-, three- or six-month term.
func (term computeBillingTerm) periodCost() (int64, error) {
	if term.Committed == nil || term.UnitPriceMinor == nil || *term.UnitPriceMinor < 0 || term.CommitmentPeriod != term.BillingInterval {
		return 0, fmt.Errorf("incomplete or inconsistent %s billing terms", term.BillingInterval)
	}
	cost := *term.UnitPriceMinor
	switch term.BillingInterval {
	case "HOURLY":
		if *term.Committed || (term.PriceUnit != "" && term.PriceUnit != "HOUR") || (term.CommitmentMonths != nil && *term.CommitmentMonths != 0) || (term.CommittedHours != nil && *term.CommittedHours != 0) {
			return 0, fmt.Errorf("hourly VM billing option must be uncommitted and priced per hour")
		}
	case "MONTHLY":
		if !*term.Committed || term.CommitmentMonths == nil || *term.CommitmentMonths != 1 {
			return 0, fmt.Errorf("monthly VM billing option must define a one-month commitment")
		}
		switch term.PriceUnit {
		case "MONTH":
		case "", "HOUR":
			if term.CommittedHours == nil || *term.CommittedHours <= 0 {
				return 0, fmt.Errorf("monthly hourly-rate option omitted committed_hours")
			}
			if cost > math.MaxInt64 / *term.CommittedHours {
				return 0, fmt.Errorf("billing commitment estimate overflows")
			}
			cost *= *term.CommittedHours
		default:
			return 0, fmt.Errorf("unsupported monthly billing price unit %q", term.PriceUnit)
		}
	default:
		return 0, fmt.Errorf("unsupported VM billing interval %q", term.BillingInterval)
	}
	return cost, nil
}
