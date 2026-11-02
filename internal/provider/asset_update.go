package provider

import (
	"maps"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/matiadata/terraform-provider-matia/internal/provider/client"
)

// applyAssetMetadataUpdate copies the changed name, description and auth method
// into a PATCH body and reports whether anything changed. A removed description
// is sent as "" so the API clears it.
func applyAssetMetadataUpdate(
	req *client.UpdateAssetRequest,
	planName, stateName types.String,
	planDescription, stateDescription types.String,
	planAuthMethod, stateAuthMethod types.String,
) bool {
	changed := false

	if !planName.Equal(stateName) && !planName.IsNull() {
		req.Name = planName.ValueString()
		changed = true
	}

	if !planDescription.Equal(stateDescription) {
		description := planDescription.ValueString()
		req.Description = &description
		changed = true
	}

	if !planAuthMethod.Equal(stateAuthMethod) && !planAuthMethod.IsNull() {
		req.AuthMethod = planAuthMethod.ValueString()
		changed = true
	}

	return changed
}

func buildAssetUpdateRequest(
	planName, stateName types.String,
	planDescription, stateDescription types.String,
	planAuthMethod, stateAuthMethod types.String,
	planConnectionConfig, stateConnectionConfig types.String,
	planConnectionSecrets, configConnectionSecrets types.String,
) (client.UpdateAssetRequest, bool, diag.Diagnostics) {
	var diags diag.Diagnostics
	var req client.UpdateAssetRequest

	changed := applyAssetMetadataUpdate(
		&req,
		planName, stateName,
		planDescription, stateDescription,
		planAuthMethod, stateAuthMethod,
	)

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

type storedConnectionShape struct {
	names    map[string]string
	defaults map[string]any
}

var storedConnectionShapes = map[string]storedConnectionShape{
	"postgres": {
		names: map[string]string{
			"replicationSlot": "slot",
			"sshHost":         "ssh_host",
			"sshPort":         "ssh_port",
			"sshUser":         "ssh_user",
		},
		defaults: map[string]any{"ssl": false},
	},
	"mysql": {
		names: map[string]string{
			"sshHost": "ssh_host",
			"sshPort": "ssh_port",
			"sshUser": "ssh_user",
		},
		defaults: map[string]any{"flavor": "mysql", "database": ""},
	},
}

var storedPortFields = []string{"port", "ssh_port", "vstreamPort"}

// toStoredConnection maps a Postgres or MySQL connection from the field names
// asset creation takes to the ones asset updates take. An update replaces the
// stored connection, so a field left under its create name would be dropped.
func toStoredConnection(assetType string, connection map[string]any) map[string]any {
	shape, mapped := storedConnectionShapes[assetType]
	if !mapped || connection == nil {
		return connection
	}

	stored := maps.Clone(connection)
	for from, to := range shape.names {
		if value, ok := stored[from]; ok {
			stored[to] = value
			delete(stored, from)
		}
	}
	for key, value := range shape.defaults {
		if _, set := stored[key]; !set {
			stored[key] = value
		}
	}
	for _, key := range storedPortFields {
		if port, isNumber := stored[key].(float64); isNumber {
			stored[key] = strconv.FormatFloat(port, 'f', -1, 64)
		}
	}
	return stored
}
