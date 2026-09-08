package provider

import (
	"context"
	"maps"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/matiadata/terraform-provider-matia/internal/provider/client"
)

func (m multiPurposeAssetModel) purposeBlocks() map[string]types.Object {
	return map[string]types.Object{
		"etl":         m.Etl,
		"reverse_etl": m.ReverseEtl,
		"catalog":     m.Catalog,
		"etl_source":  m.EtlSource,
	}
}

// credentialBlocks is every block that carries Snowflake credentials, the shared
// one included.
func (m multiPurposeAssetModel) credentialBlocks() map[string]types.Object {
	blocks := m.purposeBlocks()
	blocks["credentials"] = m.Credentials
	return blocks
}

func (m multiPurposeAssetModel) hasNoCredentials() bool {
	for _, block := range m.credentialBlocks() {
		if !block.IsNull() {
			return false
		}
	}
	return true
}

func buildCreateMultiPurposeAssetRequest(
	ctx context.Context,
	plan multiPurposeAssetModel,
) (client.CreateAssetRequest, diag.Diagnostics) {
	var diags diag.Diagnostics

	owners := []string{}
	if !plan.Owners.IsNull() && !plan.Owners.IsUnknown() {
		diags.Append(plan.Owners.ElementsAs(ctx, &owners, false)...)
	}
	var tagIDs []string
	if !plan.Tags.IsNull() && !plan.Tags.IsUnknown() {
		diags.Append(plan.Tags.ElementsAs(ctx, &tagIDs, false)...)
	}
	if diags.HasError() {
		return client.CreateAssetRequest{}, diags
	}

	purposeConnections := map[string]any{}
	for _, purpose := range credentialsPurposes {
		block := plan.purposeBlocks()[purpose.attribute]
		if !blockIsSet(block) {
			continue
		}
		connection, credDiags := snowflakeCredentialsToAPI(ctx, block)
		diags.Append(credDiags...)
		purposeConnections[purpose.apiKey] = connection
	}
	if diags.HasError() {
		return client.CreateAssetRequest{}, diags
	}

	connection := purposeConnections
	var overrides map[string]any
	if blockIsSet(plan.Credentials) {
		shared, credDiags := snowflakeCredentialsToAPI(ctx, plan.Credentials)
		diags.Append(credDiags...)
		connection = shared
		if len(purposeConnections) > 0 {
			overrides = purposeConnections
		}
	}

	// connectionType stays empty: the API creates every Snowflake asset as multi_purpose.
	req := buildCreateAssetRequest(
		plan.Name.ValueString(),
		plan.Type.ValueString(),
		plan.Description.ValueString(),
		plan.AuthMethod.ValueString(),
		connection,
		"",
		tagIDs,
	)
	req.Owners = owners
	req.ConnectionOverrides = overrides
	req.Configuration = configurationRequest(ctx, plan.AdditionalDatabases, plan.AdditionalWarehouses, &diags)
	return req, diags
}

// configurationRequest maps the two resource lists to the API's configuration
// block. A null list is omitted so the API leaves it alone; an empty list is
// sent as [] and clears it.
func configurationRequest(
	ctx context.Context,
	databases, warehouses types.List,
	diags *diag.Diagnostics,
) *client.AssetConfigurationRequest {
	etl := client.AssetEtlConfigurationRequest{
		AdditionalDatabases:  listToStringSlicePointer(ctx, databases, diags),
		AdditionalWarehouses: listToStringSlicePointer(ctx, warehouses, diags),
	}
	if etl.AdditionalDatabases == nil && etl.AdditionalWarehouses == nil {
		return nil
	}
	return &client.AssetConfigurationRequest{Etl: &etl}
}

func listToStringSlicePointer(ctx context.Context, list types.List, diags *diag.Diagnostics) *[]string {
	if list.IsNull() || list.IsUnknown() {
		return nil
	}
	values := []string{}
	diags.Append(list.ElementsAs(ctx, &values, false)...)
	return &values
}

func buildMultiPurposeAssetUpdateRequest(
	ctx context.Context,
	plan, state multiPurposeAssetModel,
) (client.UpdateAssetRequest, bool, diag.Diagnostics) {
	var diags diag.Diagnostics
	var req client.UpdateAssetRequest

	changed := applyAssetMetadataUpdate(
		&req,
		plan.Name, state.Name,
		plan.Description, state.Description,
		plan.AuthMethod, state.AuthMethod,
	)

	if !plan.Owners.Equal(state.Owners) && !plan.Owners.IsNull() && !plan.Owners.IsUnknown() {
		req.Owners = listToStringSlicePointer(ctx, plan.Owners, &diags)
		changed = true
	}

	var databases, warehouses types.List
	if !plan.AdditionalDatabases.Equal(state.AdditionalDatabases) {
		databases = plan.AdditionalDatabases
	}
	if !plan.AdditionalWarehouses.Equal(state.AdditionalWarehouses) {
		warehouses = plan.AdditionalWarehouses
	}
	if configuration := configurationRequest(ctx, databases, warehouses, &diags); configuration != nil {
		req.Configuration = configuration
		changed = true
	}

	return req, changed, diags
}

func multiPurposeAssetToModel(
	ctx context.Context,
	asset *client.Asset,
	template multiPurposeAssetModel,
	policy readPolicy,
) (*multiPurposeAssetModel, diag.Diagnostics) {
	var diags diag.Diagnostics

	if asset.Name == "" || asset.Type == "" {
		diags.AddError(
			"Unexpected API Response",
			"asset "+asset.AssetID()+" is missing required fields in the API response",
		)
		return nil, diags
	}

	var etl client.AssetEtlResources
	if asset.Configuration != nil && asset.Configuration.Etl != nil {
		etl = *asset.Configuration.Etl
	}

	additionalDatabases, dbDiags := reconcileResourceList(ctx, template.AdditionalDatabases, etl.AdditionalDatabases)
	diags.Append(dbDiags...)
	additionalWarehouses, whDiags := reconcileResourceList(ctx, template.AdditionalWarehouses, etl.AdditionalWarehouses)
	diags.Append(whDiags...)

	return &multiPurposeAssetModel{
		ID:                   types.StringValue(asset.AssetID()),
		Name:                 configuredString(template.Name, asset.Name, policy),
		Type:                 templateOrAPIString(template.Type, asset.Type),
		Description:          configuredString(template.Description, asset.Description, policy),
		AuthMethod:           templateOrAPIString(template.AuthMethod, ""),
		Owners:               template.Owners,
		Tags:                 template.Tags,
		Credentials:          template.Credentials,
		Etl:                  template.Etl,
		ReverseEtl:           template.ReverseEtl,
		Catalog:              template.Catalog,
		EtlSource:            template.EtlSource,
		AdditionalDatabases:  additionalDatabases,
		AdditionalWarehouses: additionalWarehouses,
		ConnectionType:       apiStringOrNull(asset.ConnectionType),
		DefaultDatabase:      apiStringOrNull(etl.DefaultDatabase),
		DefaultWarehouse:     apiStringOrNull(etl.DefaultWarehouse),
	}, diags
}

func apiStringOrNull(value string) types.String {
	if value == "" {
		return types.StringNull()
	}
	return types.StringValue(value)
}

// reconcileResourceList keeps the configured list when the API reports the same
// resources, so the API's trimming, de-duplication and reordering never plan a
// change; otherwise the API list wins (import, or edits made outside Terraform).
func reconcileResourceList(
	ctx context.Context,
	template types.List,
	apiValues []string,
) (types.List, diag.Diagnostics) {
	var diags diag.Diagnostics

	if !template.IsNull() && !template.IsUnknown() {
		configured := []string{}
		diags.Append(template.ElementsAs(ctx, &configured, false)...)
		if diags.HasError() {
			return types.ListNull(types.StringType), diags
		}
		if sameSnowflakeNames(configured, apiValues) {
			return template, diags
		}
	}

	if len(apiValues) == 0 {
		return types.ListNull(types.StringType), diags
	}
	list, listDiags := types.ListValueFrom(ctx, types.StringType, apiValues)
	diags.Append(listDiags...)
	return list, diags
}

func sameSnowflakeNames(a, b []string) bool {
	return maps.Equal(normalizedNameSet(a), normalizedNameSet(b))
}

func normalizedNameSet(names []string) map[string]struct{} {
	set := map[string]struct{}{}
	for _, name := range names {
		set[strings.ToUpper(strings.TrimSpace(name))] = struct{}{}
	}
	return set
}
