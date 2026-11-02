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
		preservePlanValue,
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
		preservePlanValue,
	)
	require.Equal(t, "enableAll", model.OnSchemaUpdate.ValueString())
}

func TestBuildAssetUpdateRequest_ClearsDescription(t *testing.T) {
	req, changed, diags := buildAssetUpdateRequest(
		types.StringValue("same"),
		types.StringValue("same"),
		types.StringNull(),
		types.StringValue("old"),
		types.StringNull(),
		types.StringNull(),
		types.StringValue(`{"hostname":"localhost"}`),
		types.StringValue(`{"hostname":"localhost"}`),
		types.StringNull(),
		types.StringNull(),
	)
	require.False(t, diags.HasError(), "unexpected diags: %v", diags)
	require.True(t, changed)
	require.NotNil(t, req.Description)
	require.Empty(t, *req.Description)
}

func TestToStoredConnection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		assetType string
		input     map[string]any
		want      map[string]any
	}{
		{
			name:      "postgres create names",
			assetType: "postgres",
			input: map[string]any{
				"hostname": "db", "port": float64(5432), "database": "app", "ssl": true,
				"replicationSlot": "matia_slot", "publication": "matia_pub",
				"sshHost": "bastion", "sshPort": float64(22), "sshUser": "matia",
			},
			want: map[string]any{
				"hostname": "db", "port": "5432", "database": "app", "ssl": true,
				"slot": "matia_slot", "publication": "matia_pub",
				"ssh_host": "bastion", "ssh_port": "22", "ssh_user": "matia",
			},
		},
		{
			name:      "postgres stored names stay",
			assetType: "postgres",
			input:     map[string]any{"hostname": "db", "port": "5432", "slot": "matia_slot", "ssl": false},
			want:      map[string]any{"hostname": "db", "port": "5432", "slot": "matia_slot", "ssl": false},
		},
		{
			name:      "postgres ssl default",
			assetType: "postgres",
			input:     map[string]any{"hostname": "db", "port": float64(5432)},
			want:      map[string]any{"hostname": "db", "port": "5432", "ssl": false},
		},
		{
			name:      "mysql create names and defaults",
			assetType: "mysql",
			input: map[string]any{
				"hostname": "db", "port": float64(3306), "vstreamPort": float64(443),
				"sshHost": "bastion", "sshPort": float64(22), "sshUser": "matia",
			},
			want: map[string]any{
				"hostname": "db", "port": "3306", "vstreamPort": "443",
				"ssh_host": "bastion", "ssh_port": "22", "ssh_user": "matia",
				"database": "", "flavor": "mysql",
			},
		},
		{
			name:      "other types untouched",
			assetType: "mssql",
			input:     map[string]any{"hostname": "db", "port": float64(1433)},
			want:      map[string]any{"hostname": "db", "port": float64(1433)},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, toStoredConnection(tc.assetType, tc.input))
		})
	}

	require.Nil(t, toStoredConnection("postgres", nil), "a metadata-only update sends no connection")
}
