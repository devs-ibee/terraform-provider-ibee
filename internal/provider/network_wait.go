package provider

import (
	"context"
	"fmt"
)

func waitNetworkStatus(ctx context.Context, client *Client, read func(context.Context) (string, error)) error {
	waitCtx, cancel := context.WithTimeout(ctx, client.computeTimeout())
	defer cancel()
	for {
		status, err := read(waitCtx)
		if err != nil {
			return err
		}
		switch status {
		case "available", "active":
			return nil
		case "error", "failed":
			return fmt.Errorf("network resource entered %s", status)
		case "provisioning":
			if err := client.computePoll(waitCtx); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unexpected networking readiness status %q", status)
		}
	}
}
