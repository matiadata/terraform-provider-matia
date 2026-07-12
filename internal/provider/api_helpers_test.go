package provider

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeSchemaConfigJSON(t *testing.T) {
	_, diags := prepareSchemaConfigForAPI(`{"schemas":{}}`)
	require.False(t, diags.HasError(), "expected valid config, got %v", diags)

	_, diags = prepareSchemaConfigForAPI(`{}`)
	require.True(t, diags.HasError(), "expected error for missing schemas key")
}

func TestCanonicalizeSchemaConfigForState(t *testing.T) {
	apiConfig := `{
		"schemas": {
			"public": {
				"nameInDestination": "public",
				"tables": {
					"users": {
						"nameInDestination": "users",
						"enabled": true,
						"enabledPatchSettings": {"allowed": true},
						"syncMode": "CHANGE_STREAM",
						"columns": {
							"id": {
								"enabled": true,
								"hashed": false,
								"isPrimaryKey": true,
								"nameInDestination": "id"
							}
						}
					}
				}
			}
		}
	}`

	got, diags := canonicalizeSchemaConfigForState(apiConfig)
	require.False(t, diags.HasError(), "unexpected error: %v", diags)

	want := `{"schemas":{"public":{"tables":{"users":{"enabled":true,"syncMode":"change_stream"}}}}}`
	require.Equal(t, want, got)
}

func TestPrepareSchemaConfigForAPIResolvesSyncMode(t *testing.T) {
	got, diags := prepareSchemaConfigForAPI(`{"schemas":{"public":{"tables":{"users":{"syncMode":"incremental"}}}}}`)
	require.False(t, diags.HasError(), "unexpected error: %v", diags)

	want := `{"schemas":{"public":{"tables":{"users":{"syncMode":"Incremental"}}}}}`
	require.Equal(t, want, string(got))
}
