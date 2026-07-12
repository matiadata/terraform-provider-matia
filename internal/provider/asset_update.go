package provider

import (
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/matiadata/terraform-provider-matia/internal/provider/client"
)

func buildAssetUpdateRequest(
	planName, stateName types.String,
	planDescription, stateDescription types.String,
	planAuthMethod, stateAuthMethod types.String,
	planConnectionConfig, stateConnectionConfig types.String,
	planConnectionSecrets, configConnectionSecrets types.String,
) (client.UpdateAssetRequest, bool, diag.Diagnostics) {
	var diags diag.Diagnostics
	var req client.UpdateAssetRequest
	changed := false

	if !planName.Equal(stateName) && !planName.IsNull() {
		req.Name = planName.ValueString()
		changed = true
	}

	if !planDescription.Equal(stateDescription) {
		if planDescription.IsNull() {
			req.Description = ""
		} else {
			req.Description = planDescription.ValueString()
		}
		changed = true
	}

	if !planAuthMethod.Equal(stateAuthMethod) && !planAuthMethod.IsNull() {
		req.AuthMethod = planAuthMethod.ValueString()
		changed = true
	}

	connectionChanged := !planConnectionConfig.Equal(stateConnectionConfig)

	// Write-only secrets live in config/plan, not state; include them when present so
	// password rotations and config+secret updates reach the API.
	secretsSource := configConnectionSecrets
	if !planConnectionSecrets.IsNull() && !planConnectionSecrets.IsUnknown() {
		secretsSource = planConnectionSecrets
	}
	includeSecrets := !secretsSource.IsNull() && !secretsSource.IsUnknown()

	if connectionChanged || includeSecrets {
		mergedConnection, mergeDiags := mergeConnectionFields(planConnectionConfig, secretsSource)
		diags.Append(mergeDiags...)
		if diags.HasError() {
			return req, false, diags
		}

		req.Connection = mergedConnection
		changed = true
	}

	return req, changed, diags
}
