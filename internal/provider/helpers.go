package provider

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
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
	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("operation %s did not complete within %s", operationID, timeout)
		}
		var op struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		}
		err := c.do(ctx, http.MethodGet, "/compute/operations/"+operationID, nil, &op)
		if err == nil {
			switch op.Status {
			case "succeeded", "success", "completed":
				return nil
			case "failed", "error":
				if op.Error != "" {
					return fmt.Errorf("operation %s failed: %s", operationID, op.Error)
				}
				return fmt.Errorf("operation %s failed", operationID)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
}

// computePlan mirrors the public plans response entries we consume.
type computePlan struct {
	PlanID           string         `json:"plan_id"`
	Name             string         `json:"name"`
	Cpu              int64          `json:"cpu"`
	RamMb            int64          `json:"ram_mb"`
	DiskGb           int64          `json:"disk_gb"`
	HourlyPriceMinor int64          `json:"hourly_price_minor"`
	Currency         string         `json:"currency"`
	Selectable       bool           `json:"selectable"`
	GpuCount         int64          `json:"gpu_count"`
	SiteID           string         `json:"site_id"`
	BillingCatalog   map[string]any `json:"billing_catalog"`
}

func (c *Client) listComputePlans(ctx context.Context, vmType, siteID string) ([]computePlan, error) {
	path := "/compute/plans?vm_type=" + vmType
	if siteID != "" {
		path += "&site_id=" + siteID
	}
	var out struct {
		Plans []computePlan `json:"plans"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Plans, nil
}

// findPlan resolves a plan by ID; the plan carries the billing_catalog the
// create endpoints require (the portal copies it the same way).
func (c *Client) findPlan(ctx context.Context, vmType, siteID, planID string) (*computePlan, error) {
	plans, err := c.listComputePlans(ctx, vmType, siteID)
	if err != nil {
		return nil, err
	}
	for i := range plans {
		if plans[i].PlanID == planID {
			return &plans[i], nil
		}
	}
	return nil, fmt.Errorf("plan %q not found for vm_type=%s site_id=%s", planID, vmType, siteID)
}
