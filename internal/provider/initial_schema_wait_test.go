package provider

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/matiadata/terraform-provider-matia/internal/provider/client"
)

func TestWaitForInitialSchemaCatalogDelayedDiscovery(t *testing.T) {
	calls := 0
	ready := `{"schemas":{"schema":{"tables":{"customers":{"enabled":true}}}}}`
	catalog, diags := waitForInitialSchemaCatalog(t.Context(), func(context.Context) (string, error) {
		calls++
		switch calls {
		case 1:
			return "", client.ErrIntegrationInCreation
		case 2:
			return `{}`, nil
		case 3:
			return `{"schemas":{"schema":{"tables":{}}}}`, nil
		default:
			return ready, nil
		}
	}, time.Millisecond)
	require.Empty(t, diags)
	require.Equal(t, ready, catalog)
	require.Equal(t, 4, calls)
}

func TestWaitForInitialSchemaCatalogStopsOnErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		err        error
	}{
		{name: "authorization", err: errors.New("403 forbidden")},
		{name: "missing integration", err: client.ErrIntegrationNotFound},
		{name: "creation failed", err: client.ErrIntegrationFailed},
		{name: "malformed response", body: `not json`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			_, diags := waitForInitialSchemaCatalog(t.Context(), func(context.Context) (string, error) {
				calls++
				return tc.body, tc.err
			}, time.Millisecond)
			require.True(t, diags.HasError())
			require.Equal(t, 1, calls)
		})
	}
}

func TestWaitForInitialSchemaCatalogDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	calls := 0
	_, diags := waitForInitialSchemaCatalog(ctx, func(context.Context) (string, error) {
		calls++
		return `{"schemas":{}}`, nil
	}, time.Millisecond)
	require.True(t, diags.HasError())
	require.Equal(t, "Timed Out Waiting for Schema Discovery", diags.Errors()[0].Summary())
	require.Positive(t, calls)
}

func TestWaitForInitialSchemaCatalogCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	calls := 0
	_, diags := waitForInitialSchemaCatalog(ctx, func(context.Context) (string, error) {
		calls++
		cancel()
		return `{"schemas":{}}`, nil
	}, time.Hour)
	require.Equal(t, 1, calls)
	require.Equal(t, "Schema Discovery Wait Canceled", diags.Errors()[0].Summary())
}

func TestWaitForInitialSchemaCatalogDeadlineReachesRequest(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	_, diags := waitForInitialSchemaCatalog(ctx, func(requestCtx context.Context) (string, error) {
		<-requestCtx.Done()
		return "", requestCtx.Err()
	}, time.Hour)
	require.Equal(t, "Timed Out Waiting for Schema Discovery", diags.Errors()[0].Summary())
}
