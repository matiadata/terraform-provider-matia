package provider

import (
	"context"
	"errors"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"

	"github.com/matiadata/terraform-provider-matia/internal/provider/client"
)

const (
	// Bound the initial discovery wait so an apply does not stall on a missing catalog.
	initialSchemaDiscoveryTimeout = 3 * time.Minute
	initialSchemaPollInterval     = 2 * time.Second
)

// An integration GET can succeed before asynchronous catalog discovery finishes.
// Bound both sleeps and HTTP calls, and never PATCH while the catalog is empty.
func waitForInitialSchemaCatalog(
	ctx context.Context,
	getCatalog func(context.Context) (string, error),
	interval time.Duration,
) (string, diag.Diagnostics) {
	ctx, cancel := context.WithTimeout(ctx, initialSchemaDiscoveryTimeout)
	defer cancel()
	var diags diag.Diagnostics
	for ctx.Err() == nil {
		catalog, err := getCatalog(ctx)
		if ctx.Err() != nil {
			break
		}
		if err != nil && !errors.Is(err, client.ErrIntegrationInCreation) {
			diags.AddError("Failed to read integration schema for initial selection", err.Error())
			return "", diags
		}
		if err == nil {
			schemas, parseDiags := parseSchemaCatalog(catalog)
			diags.Append(parseDiags...)
			if diags.HasError() {
				return "", diags
			}
			for _, schema := range schemas {
				if len(schemaTables(schema)) > 0 {
					return catalog, diags
				}
			}
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		diags.AddError(
			"Timed Out Waiting for Schema Discovery",
			"Discovery did not return tables before the deadline (at most 3 minutes). No selection PATCH was sent. Keep the integration paused, check discovery in Matia, and retry after it succeeds.",
		)
	} else {
		diags.AddError(
			"Schema Discovery Wait Canceled",
			"The operation was canceled before discovery returned tables. No selection PATCH was sent.",
		)
	}
	return "", diags
}
