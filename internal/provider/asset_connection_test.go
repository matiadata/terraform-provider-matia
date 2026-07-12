package provider

import (
	"encoding/json"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/require"

	"github.com/matiadata/terraform-provider-matia/internal/provider/client"
)

func TestMergeAssetConnectionState(t *testing.T) {
	existingConfig := types.StringValue(`{"hostname":"old-host","port":"5432"}`)
	existingSecrets := types.StringValue(`{"password":"secret"}`)
	configSecrets := types.StringValue(`{"username":"postgres"}`)

	asset := &client.Asset{
		Connection: map[string]any{
			"hostname": "new-host",
			"port":     "5432",
			"password": "should-not-appear",
			"username": "should-not-appear",
		},
	}

	config, secrets, diags := mergeAssetConnectionState(
		asset,
		existingConfig,
		existingSecrets,
		configSecrets,
	)
	require.False(t, diags.HasError(), "diags = %v", diags)

	configValues := map[string]any{}
	require.NoError(t, json.Unmarshal([]byte(config.ValueString()), &configValues))
	require.Equal(t, "new-host", configValues["hostname"])
	require.Equal(t, "5432", configValues["port"])
	_, hasPassword := configValues["password"]
	require.False(t, hasPassword, "password should not be in config")
	_, hasUsername := configValues["username"]
	require.False(t, hasUsername, "username should not be in config")

	secretValues := map[string]any{}
	require.NoError(t, json.Unmarshal([]byte(secrets.ValueString()), &secretValues))
	require.Equal(t, "secret", secretValues["password"])
	require.Equal(t, "postgres", secretValues["username"])
}

func TestMergeAssetConnectionStateDoesNotAddNewConfigKeys(t *testing.T) {
	existingConfig := types.StringValue(`{"hostname":"host"}`)
	configSecrets := types.StringValue(`{"password":"secret"}`)

	asset := &client.Asset{
		Connection: map[string]any{
			"hostname": "api-host",
			"port":     "5432",
		},
	}

	config, _, diags := mergeAssetConnectionState(
		asset,
		existingConfig,
		types.StringNull(),
		configSecrets,
	)
	require.False(t, diags.HasError(), "diags = %v", diags)

	configValues := map[string]any{}
	require.NoError(t, json.Unmarshal([]byte(config.ValueString()), &configValues))
	require.Equal(t, "api-host", configValues["hostname"])
	_, hasPort := configValues["port"]
	require.False(t, hasPort, "port should not be added from API when absent from connection_config")
}

func TestMergeAssetConnectionStatePreservesBoolValues(t *testing.T) {
	existingConfig := types.StringValue(`{"hostname":"host","ssl":false}`)

	asset := &client.Asset{
		Connection: map[string]any{
			"hostname": "api-host",
			"ssl":      true,
		},
	}

	config, _, diags := mergeAssetConnectionState(
		asset,
		existingConfig,
		types.StringNull(),
		types.StringNull(),
	)
	require.False(t, diags.HasError(), "diags = %v", diags)

	configValues := map[string]any{}
	require.NoError(t, json.Unmarshal([]byte(config.ValueString()), &configValues))
	ssl, ok := configValues["ssl"].(bool)
	require.True(t, ok)
	require.True(t, ssl)
}

func TestMergeAssetConnectionStateInvalidSecretsJSON(t *testing.T) {
	existingConfig := types.StringValue(`{"hostname":"host"}`)
	invalidSecrets := types.StringValue(`{not-json}`)

	config, secrets, diags := mergeAssetConnectionState(
		&client.Asset{Connection: map[string]any{"hostname": "api-host"}},
		existingConfig,
		invalidSecrets,
		types.StringNull(),
	)
	require.True(t, diags.HasError(), "expected parse error for invalid connection_secrets JSON")
	require.True(t, config.IsNull())
	require.True(t, secrets.IsNull())
}
