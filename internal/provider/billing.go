package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type billingEligibilityRequest struct {
	SKUCode            string `json:"sku_code,omitempty"`
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
	if len(request.SKUCode) > 64 {
		return nil, fmt.Errorf("billing SKU must be at most 64 characters")
	}
	if request.EstimatedCostMinor != nil && *request.EstimatedCostMinor < 0 {
		return nil, fmt.Errorf("billing estimate cannot be negative")
	}
	var decision billingEligibility
	if err := c.do(ctx, http.MethodPost, "/billing/resource-eligibility", request, &decision); err != nil {
		return nil, fmt.Errorf("billing eligibility could not be verified (the API token requires billing.read): %w", err)
	}
	if decision.Allowed == nil || strings.TrimSpace(decision.OrganizationID) == "" || strings.TrimSpace(decision.Reason) == "" || decision.Currency == "" || decision.BillingState == "" || (decision.BillingMode != "PREPAID" && decision.BillingMode != "POSTPAID") {
		return nil, fmt.Errorf("billing returned an incomplete eligibility decision; provisioning was not authorized")
	}
	if _, err := time.Parse(time.RFC3339Nano, decision.EvaluatedAt); err != nil {
		return nil, fmt.Errorf("billing returned an invalid evaluation timestamp; provisioning was not authorized")
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

func (c *Client) requireBillingEligibility(ctx context.Context, skuCode string, estimatedCostMinor *int64) error {
	return c.requireBillingEligibilityForCurrency(ctx, skuCode, estimatedCostMinor, "")
}

func (c *Client) requireBillingEligibilityForCurrency(ctx context.Context, skuCode string, estimatedCostMinor *int64, currency string) error {
	decision, err := c.checkBillingEligibility(ctx, billingEligibilityRequest{SKUCode: skuCode, EstimatedCostMinor: estimatedCostMinor})
	if err != nil {
		return err
	}
	if currency != "" && !strings.EqualFold(decision.Currency, currency) {
		return fmt.Errorf("billing decision currency does not match the trusted catalog price currency; refresh the catalog before retrying")
	}
	return billingAdmissionError(decision)
}

func billingAdmissionError(decision *billingEligibility) error {
	if decision == nil || decision.Allowed == nil {
		return fmt.Errorf("billing returned no explicit admission decision")
	}
	if *decision.Allowed {
		return nil
	}
	remedy := "Review the organization's billing status and permissions in the IBEE portal, then retry."
	switch decision.Reason {
	case "initial_topup_required", "insufficient_balance":
		remedy = "Use Add Credits in the organization's Billing page, wait for payment confirmation, then run terraform apply again."
	case "credit_limit_exceeded":
		remedy = "Settle outstanding usage or request an approved credit-limit change in the portal, then retry."
	case "unknown_sku", "inactive_sku":
		remedy = "Refresh the product catalog and choose an active plan or SKU."
	case "dunning_active", "dunning_grace_expired", "billing_limit_exhausted":
		remedy = "Resolve the outstanding billing case in the portal, then retry."
	}
	return fmt.Errorf("billing denied the purchase: %s (organization %s, %s, %s). %s", decision.Reason, decision.OrganizationID, decision.BillingMode, decision.BillingState, remedy)
}
