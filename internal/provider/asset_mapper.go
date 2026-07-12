package provider

import (
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/matiadata/terraform-provider-matia/internal/provider/client"
)

func buildCreateAssetRequest(
	name, assetType, description, authMethod string,
	connection map[string]any,
	connectionType string,
	tagIDs []string,
) client.CreateAssetRequest {
	req := client.CreateAssetRequest{
		Name:           name,
		Type:           assetType,
		Connection:     connection,
		ConnectionType: connectionType,
		Owners:         []string{},
	}
	if description != "" {
		req.Description = description
	}
	if authMethod != "" {
		req.AuthMethod = authMethod
	}
	if len(tagIDs) > 0 {
		req.Tags = tagIDs
	}
	return req
}

func effectiveAssetTemplate(plan, config assetModel) assetModel {
	template := plan
	if template.ConnectionSecrets.IsNull() || template.ConnectionSecrets.IsUnknown() {
		template.ConnectionSecrets = config.ConnectionSecrets
	}
	return template
}

func assetToModel(asset *client.Asset, template assetModel) (*assetModel, diag.Diagnostics) {
	var diags diag.Diagnostics

	if asset.Name == "" || asset.Type == "" {
		diags.AddError(
			"Unexpected API Response",
			fmt.Sprintf("asset %s is missing required fields in the API response", asset.AssetID()),
		)
		return nil, diags
	}

	mergedConfig, mergedSecrets, mergeDiags := mergeAssetConnectionState(
		asset,
		template.ConnectionConfig,
		template.ConnectionSecrets,
		types.StringNull(),
	)
	diags.Append(mergeDiags...)

	description := template.Description
	if description.IsUnknown() {
		description = types.StringNull()
	}

	authMethod := template.AuthMethod
	if authMethod.IsUnknown() {
		authMethod = types.StringNull()
	}

	return &assetModel{
		ID:                types.StringValue(asset.AssetID()),
		Name:              template.Name,
		Description:       description,
		Type:              template.Type,
		AuthMethod:        authMethod,
		Tags:              template.Tags,
		ConnectionConfig:  mergedConfig,
		ConnectionSecrets: mergedSecrets,
		IsDraft:           types.BoolValue(asset.IsDraft),
	}, diags
}
