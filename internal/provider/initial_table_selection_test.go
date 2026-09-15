package provider

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInitialTableSelection(t *testing.T) {
	config := `{"schemas":{"public":{"tables":{"customers":{"enabled":true,"columns":{"email":{"enabled":false}}},"explicit_off":{"enabled":false},"mode_only":{"syncMode":"cdc"}}}}}`
	catalog := `{"schemas":{"public":{"tables":{"customers":{"enabled":true},"explicit_off":{"enabled":true},"mode_only":{"enabled":true},"orders":{"enabled":true},"already_off":{"enabled":false},"locked":{"enabled":true,"enabledPatchSettings":{"allowed":false}}}},"other":{"tables":{"events":{"enabled":true}}}}}`
	got, diags := buildInitialSchemaSelection(config, catalog, "enableColumnChanges")
	require.False(t, diags.HasError(), "%v", diags)
	require.Len(t, diags.Warnings(), 1)
	require.Contains(t, diags.Warnings()[0].Detail(), `"public"."locked"`)
	require.JSONEq(
		t,
		`{"schemas":{"public":{"tables":{"customers":{"enabled":true,"columns":{"email":{"enabled":false}}},"explicit_off":{"enabled":false},"mode_only":{"syncMode":"cdc"},"orders":{"enabled":false}}},"other":{"tables":{"events":{"enabled":false}}}}}`,
		got.config,
	)
}

func TestInitialTableSelectionRejectsUnavailableCatalog(t *testing.T) {
	for _, catalog := range []string{`{}`, `{"schemas":{}}`, `{"schemas":{"public":{"tables":{}}}}`, `invalid`} {
		t.Run(catalog, func(t *testing.T) {
			_, diags := buildInitialSchemaSelection(`{"schemas":{}}`, catalog, "enableColumnChanges")
			require.True(t, diags.HasError())
		})
	}
}

func TestInitialTableSelectionEmptySelectionDisablesAll(t *testing.T) {
	got, diags := buildInitialSchemaSelection(
		`{"schemas":{}}`,
		`{"schemas":{"public":{"tables":{"customers":{"enabled":true},"orders":{"enabled":true}}}}}`,
		"enableColumnChanges",
	)
	require.False(t, diags.HasError())
	require.JSONEq(
		t,
		`{"schemas":{"public":{"tables":{"customers":{"enabled":false},"orders":{"enabled":false}}}}}`,
		got.config,
	)
}
