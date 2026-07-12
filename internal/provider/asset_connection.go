package provider

import (
	"encoding/json"
	"maps"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/matiadata/terraform-provider-matia/internal/provider/client"
)

func mergeConnectionFields(configJSON, secretsJSON types.String) (map[string]any, diag.Diagnostics) {
	config, diags := parseOptionalJSONObject(configJSON, "connection_config")
	if diags.HasError() {
		return nil, diags
	}

	secrets, secretDiags := parseOptionalJSONObject(secretsJSON, "connection_secrets")
	diags.Append(secretDiags...)
	if diags.HasError() {
		return nil, diags
	}

	merged := map[string]any{}
	maps.Copy(merged, config)
	maps.Copy(merged, secrets)
	return merged, diags
}

func mergeAssetConnectionState(
	asset *client.Asset,
	existingConfig, existingSecrets, configSecrets types.String,
) (types.String, types.String, diag.Diagnostics) {
	var diags diag.Diagnostics

	configValues, configDiags := parseOptionalJSONObject(existingConfig, "connection_config")
	diags.Append(configDiags...)
	if diags.HasError() {
		return types.StringNull(), types.StringNull(), diags
	}

	secretValues := map[string]any{}
	secretKeys := map[string]struct{}{}

	mergeSecretJSON := func(secrets types.String) {
		parsed, parseDiags := parseOptionalJSONObject(secrets, "connection_secrets")
		diags.Append(parseDiags...)
		for key, value := range parsed {
			secretKeys[key] = struct{}{}
			secretValues[key] = value
		}
	}

	mergeSecretJSON(existingSecrets)
	if diags.HasError() {
		return types.StringNull(), types.StringNull(), diags
	}
	mergeSecretJSON(configSecrets)
	if diags.HasError() {
		return types.StringNull(), types.StringNull(), diags
	}

	for key := range secretKeys {
		delete(configValues, key)
	}

	for key := range configValues {
		if _, isSecret := secretKeys[key]; isSecret {
			continue
		}
		if raw, ok := asset.Connection[key]; ok {
			configValues[key] = raw
		}
	}

	configForState, configStateDiags := connectionJSONValue(configValues)
	diags.Append(configStateDiags...)
	secretsForState, secretsStateDiags := connectionJSONValue(secretValues)
	diags.Append(secretsStateDiags...)
	if diags.HasError() {
		return types.StringNull(), types.StringNull(), diags
	}

	return configForState, secretsForState, diags
}

func connectionJSONValue(values map[string]any) (types.String, diag.Diagnostics) {
	var diags diag.Diagnostics
	if len(values) == 0 {
		return types.StringNull(), diags
	}

	encoded, err := json.Marshal(sortJSONKeys(values))
	if err != nil {
		diags.AddError("Failed to encode connection JSON", err.Error())
		return types.StringNull(), diags
	}

	return types.StringValue(string(encoded)), diags
}
