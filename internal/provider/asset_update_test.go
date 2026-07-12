package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/require"

	"github.com/matiadata/terraform-provider-matia/internal/provider/client"
)

func TestBuildAssetUpdateRequest_ConnectionConfig(t *testing.T) {
	planConfig := types.StringValue(`{"hostname":"db.example.com"}`)
	stateConfig := types.StringValue(`{"hostname":"localhost"}`)

	req, changed, diags := buildAssetUpdateRequest(
		types.StringValue("same"),
		types.StringValue("same"),
		types.StringNull(),
		types.StringNull(),
		types.StringNull(),
		types.StringNull(),
		planConfig,
		stateConfig,
		types.StringNull(),
		types.StringNull(),
	)
	require.False(t, diags.HasError(), "unexpected diags: %v", diags)
	require.True(t, changed, "expected changed=true for connection_config update")
	require.Equal(t, "db.example.com", req.Connection["hostname"])
}

func TestBuildAssetUpdateRequest_SecretsFromPlan(t *testing.T) {
	config := types.StringValue(`{"hostname":"localhost"}`)
	planSecrets := types.StringValue(`{"password":"new-secret"}`)

	req, changed, diags := buildAssetUpdateRequest(
		types.StringValue("same"),
		types.StringValue("same"),
		types.StringNull(),
		types.StringNull(),
		types.StringNull(),
		types.StringNull(),
		config,
		config,
		planSecrets,
		types.StringNull(),
	)
	require.False(t, diags.HasError(), "unexpected diags: %v", diags)
	require.True(t, changed, "expected changed=true when plan includes write-only secrets")
	require.Equal(t, "new-secret", req.Connection["password"])
}

func TestBuildAssetUpdateRequest_NoChanges(t *testing.T) {
	name := types.StringValue("same")
	config := types.StringValue(`{"hostname":"localhost"}`)

	_, changed, diags := buildAssetUpdateRequest(
		name,
		name,
		types.StringNull(),
		types.StringNull(),
		types.StringNull(),
		types.StringNull(),
		config,
		config,
		types.StringNull(),
		types.StringNull(),
	)
	require.False(t, diags.HasError(), "unexpected diags: %v", diags)
	require.False(t, changed, "expected changed=false when plan matches state and no secrets")
}

func TestBuildModifyIntegrationRequest(t *testing.T) {
	req, changed, diags := buildModifyIntegrationRequest(
		integrationModel{
			Name:               types.StringValue("new"),
			Paused:             types.BoolValue(true),
			SourceSettingsJSON: types.StringValue(`{"customReports":[]}`),
			DestinationSchema:  types.StringValue("analytics"),
			OnSchemaUpdate:     types.StringValue("enableAll"),
			AgentID:            types.StringValue("agent-new"),
		},
		integrationModel{
			Name:               types.StringValue("old"),
			Paused:             types.BoolValue(false),
			SourceSettingsJSON: types.StringNull(),
			DestinationSchema:  types.StringValue("raw"),
			OnSchemaUpdate:     types.StringValue("ignoreAll"),
			AgentID:            types.StringValue("agent-old"),
		},
	)
	require.False(t, diags.HasError(), "unexpected diags: %v", diags)
	require.True(t, changed, "expected changed=true")
	require.Equal(t, "new", req.Name)
	require.NotNil(t, req.Paused)
	require.True(t, *req.Paused)
	require.NotNil(t, req.SourceSettings)
	require.Equal(t, "analytics", req.DestinationSchema)
	require.Equal(t, "enableAll", req.OnSchemaUpdate)
	require.Equal(t, "agent-new", req.AgentID)
}

func TestBuildModifyIntegrationRequest_ClearsSourceSettings(t *testing.T) {
	req, changed, diags := buildModifyIntegrationRequest(
		integrationModel{
			SourceSettingsJSON: types.StringNull(),
		},
		integrationModel{
			SourceSettingsJSON: types.StringValue(`{"customReports":[]}`),
		},
	)
	require.False(t, diags.HasError(), "unexpected diags: %v", diags)
	require.True(t, changed, "expected changed=true when source_settings is removed")
	require.NotNil(t, req.SourceSettings)
	require.Empty(t, req.SourceSettings)
}

func TestBuildModifyIntegrationRequest_ClearsAgentID(t *testing.T) {
	req, changed, diags := buildModifyIntegrationRequest(
		integrationModel{
			AgentID: types.StringNull(),
		},
		integrationModel{
			AgentID: types.StringValue("agent-123"),
		},
	)
	require.False(t, diags.HasError(), "unexpected diags: %v", diags)
	require.True(t, changed, "expected changed=true when agent_id is removed")
	require.IsType(t, (*string)(nil), req.AgentID)
}

func TestIntegrationToModel_UnknownComputedFieldsWithoutAPIValue(t *testing.T) {
	model := integrationToModel(
		&client.Integration{
			ID:                "integration-id",
			Name:              "test",
			Paused:            false,
			Source:            client.IntegrationEndpoint{ID: "src", Type: "postgres"},
			Destination:       client.IntegrationEndpoint{ID: "dst", Type: "snowflake"},
			DestinationSchema: "raw",
		},
		integrationModel{
			DestinationSchema: types.StringValue("raw"),
			OnSchemaUpdate:    types.StringUnknown(),
		},
	)
	require.True(t, model.OnSchemaUpdate.IsNull(), "on_schema_update = %v, want null", model.OnSchemaUpdate)
}

func TestIntegrationToModel_OnSchemaUpdateFromAPI(t *testing.T) {
	model := integrationToModel(
		&client.Integration{
			ID:             "integration-id",
			Name:           "test",
			Paused:         false,
			Source:         client.IntegrationEndpoint{ID: "src", Type: "postgres"},
			Destination:    client.IntegrationEndpoint{ID: "dst", Type: "snowflake"},
			OnSchemaUpdate: "enableAll",
		},
		integrationModel{
			OnSchemaUpdate: types.StringUnknown(),
		},
	)
	require.Equal(t, "enableAll", model.OnSchemaUpdate.ValueString())
}
