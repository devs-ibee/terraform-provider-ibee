package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

type billingEligibilityRequest struct {
	SKUCode            string `json:"sku_code,omitempty"`
	Operation          string `json:"operation,omitempty"`
	EstimatedCostMinor *int64 `json:"estimated_cost_minor,omitempty"`
}

type billingEligibility struct {
	OrganizationID        string  `json:"organization_id"`
	Allowed               *bool   `json:"allowed"`
	Reason                string  `json:"reason"`
	BillingMode           string  `json:"billing_mode"`
	BillingState          string  `json:"billing_state"`
	Currency              string  `json:"currency"`
	SKUCode               *string `json:"sku_code"`
	EstimatedCostMinor    *int64  `json:"estimated_cost_minor"`
	EffectiveBalanceMinor *int64  `json:"effective_balance_minor"`
	CreditHeadroomMinor   *int64  `json:"credit_headroom_minor"`
	EvaluatedAt           string  `json:"evaluated_at"`
}

// This read-only POST is deliberately not cached. It does not reserve funds.
func (c *Client) checkBillingEligibility(ctx context.Context, request billingEligibilityRequest) (*billingEligibility, error) {
	request.SKUCode = strings.TrimSpace(request.SKUCode)
	request.Operation = strings.ToUpper(strings.TrimSpace(request.Operation))
	if len(request.SKUCode) > 64 {
		return nil, fmt.Errorf("billing SKU must be at most 64 characters")
	}
	if request.EstimatedCostMinor != nil && *request.EstimatedCostMinor < 0 {
		return nil, fmt.Errorf("billing estimate cannot be negative")
	}
	// The decision is relayed as Billing returns it; errors are the API's answer.
	var decision billingEligibility
	if err := c.do(ctx, http.MethodPost, "/billing/resource-eligibility", request, &decision); err != nil {
		return nil, err
	}
	if c.organizationID != "" && decision.OrganizationID != c.organizationID {
		return nil, fmt.Errorf("billing decision does not match the configured organization_id")
	}
	if request.SKUCode != "" && (decision.SKUCode == nil || !strings.EqualFold(*decision.SKUCode, request.SKUCode)) {
		return nil, fmt.Errorf("billing decision does not match the requested SKU")
	}
	if request.EstimatedCostMinor != nil && decision.EstimatedCostMinor != nil && *request.EstimatedCostMinor != *decision.EstimatedCostMinor {
		return nil, fmt.Errorf("billing decision does not match the requested cost estimate")
	}
	return &decision, nil
}
