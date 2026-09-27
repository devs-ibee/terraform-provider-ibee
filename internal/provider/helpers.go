package provider

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func idempotencyKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "tf-" + hex.EncodeToString(b)
}

// operationAccepted is the lean 202 body compute mutations return.
type operationAccepted struct {
	OperationID string `json:"operation_id"`
	VmID        string `json:"vm_id"`
	Status      string `json:"status"`
}

// waitOperation polls a compute operation until it reaches a terminal state.
func (c *Client) waitOperation(ctx context.Context, operationID string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = c.computeTimeout()
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		var op struct {
			Status       string `json:"status"`
			Error        string `json:"error"`
			ErrorCode    string `json:"error_code"`
			ErrorMessage string `json:"error_message"`
		}
		err := c.do(ctx, http.MethodGet, "/compute/operations/"+url.PathEscape(operationID), nil, &op)
		if err == nil {
			switch strings.ToLower(op.Status) {
			case "succeeded", "success", "completed":
				return nil
			case "failed", "error", "cancelled", "canceled", "timed_out":
				return fmt.Errorf("operation %s %s: %s %s %s", operationID, op.Status, op.ErrorCode, op.ErrorMessage, op.Error)
			case "accepted", "running", "waiting", "compensating", "queued", "pending":
			default:
				return fmt.Errorf("operation %s returned unsupported status %q", operationID, op.Status)
			}
		} else if !retryableComputeRead(err) {
			return fmt.Errorf("read operation %s: %w", operationID, err)
		}
		if err := c.computePoll(ctx); err != nil {
			return fmt.Errorf("operation %s did not complete: %w", operationID, err)
		}
	}
}

func (c *Client) computeTimeout() time.Duration {
	if c.operationTimeout > 0 {
		return c.operationTimeout
	}
	return 20 * time.Minute
}

func (c *Client) computePoll(ctx context.Context) error {
	delay := c.pollInterval
	if delay <= 0 {
		delay = 2 * time.Second
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryableComputeRead(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && (ae.Status == http.StatusTooManyRequests || ae.Status == http.StatusRequestTimeout || ae.Status >= 500)
}

// computePlan mirrors the public plans response entries we consume.
type computePlan struct {
	PlanID                string         `json:"plan_id"`
	Name                  string         `json:"name"`
	Code                  string         `json:"code"`
	Cpu                   int64          `json:"cpu"`
	RamMb                 int64          `json:"ram_mb"`
	DiskGb                int64          `json:"disk_gb"`
	HourlyPriceMinor      *int64         `json:"hourly_price_minor"`
	MonthlyPriceMinor     *int64         `json:"monthly_price_minor"`
	PricingStatus         string         `json:"pricing_status"`
	BillingInterval       string         `json:"billing_interval"`
	Currency              string         `json:"currency"`
	Selectable            bool           `json:"selectable"`
	GpuCount              int64          `json:"gpu_count"`
	GpuModel              string         `json:"gpu_model"`
	SiteID                string         `json:"site_id"`
	BillingCatalog        map[string]any `json:"billing_catalog"`
	SelectedTermCostMinor *int64         `json:"-"`
}

func (c *Client) listComputePlans(ctx context.Context, vmType, siteID string) ([]computePlan, error) {
	return c.listComputePlansFiltered(ctx, vmType, siteID, "", "")
}

func (c *Client) listComputePlansFiltered(ctx context.Context, vmType, siteID, currency, interval string) ([]computePlan, error) {
	if vmType != "cloud" && vmType != "gpu" {
		return nil, fmt.Errorf("vm_type must be cloud or gpu")
	}
	query := url.Values{"vm_type": {vmType}}
	if currency != "" {
		query.Set("currency", currency)
	}
	if interval != "" {
		query.Set("billing_interval", interval)
	}
	if siteID != "" {
		query.Set("site_id", siteID)
	}
	var out struct {
		Plans *[]computePlan `json:"plans"`
	}
	if err := c.do(ctx, http.MethodGet, "/compute/plans?"+query.Encode(), nil, &out); err != nil {
		return nil, err
	}
	if out.Plans == nil {
		return nil, fmt.Errorf("compute catalog omitted plans array")
	}
	return *out.Plans, nil
}

// findPlan resolves a plan by ID; the plan carries the billing_catalog the
// create endpoints require (the portal copies it the same way).
func (c *Client) findPlan(ctx context.Context, vmType, siteID, planID string) (*computePlan, error) {
	return c.findPlanForCurrency(ctx, vmType, siteID, planID, "")
}

func (c *Client) findPlanForCurrency(ctx context.Context, vmType, siteID, planID, currency string) (*computePlan, error) {
	return c.findPlanForTerm(ctx, vmType, siteID, planID, currency, "MONTHLY")
}

func (c *Client) findPlanForTerm(ctx context.Context, vmType, siteID, planID, currency, interval string) (*computePlan, error) {
	plans, err := c.listComputePlansFiltered(ctx, vmType, siteID, currency, interval)
	if err != nil {
		return nil, err
	}
	for i := range plans {
		if plans[i].PlanID == planID {
			if currency != "" && !strings.EqualFold(plans[i].Currency, currency) {
				return nil, fmt.Errorf("catalog currency %q does not match organization currency %q", plans[i].Currency, currency)
			}
			if plans[i].SiteID != "" && plans[i].SiteID != siteID {
				return nil, fmt.Errorf("catalog plan belongs to a different site")
			}
			return &plans[i], nil
		}
	}
	return nil, fmt.Errorf("plan %q not found for vm_type=%s site_id=%s", planID, vmType, siteID)
}
