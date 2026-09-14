package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/matiadata/terraform-provider-matia/internal/provider/client"
)

type integrationResource struct {
	client *client.MatiaClient
}

type integrationModel struct {
	ID                      types.String `tfsdk:"id"`
	Name                    types.String `tfsdk:"name"`
	SourceID                types.String `tfsdk:"source_id"`
	DestinationID           types.String `tfsdk:"destination_id"`
	DestinationSchema       types.String `tfsdk:"destination_schema"`
	AgentID                 types.String `tfsdk:"agent_id"`
	OnSchemaUpdate          types.String `tfsdk:"on_schema_update"`
	Paused                  types.Bool   `tfsdk:"paused"`
	SourceSettingsJSON      types.String `tfsdk:"source_settings"`
	DestinationSettingsJSON types.String `tfsdk:"destination_settings"`
	DestinationDatabase     types.String `tfsdk:"destination_database"`
	DestinationWarehouse    types.String `tfsdk:"destination_warehouse"`
	Tags                    types.List   `tfsdk:"tags"`
}

var (
	_ resource.Resource                = &integrationResource{}
	_ resource.ResourceWithImportState = &integrationResource{}
)

// An omitted agent_id is resolved by the backend: it inherits a bound source's
// agent, and a source's agent change cascades to its integrations. The
// effective value is therefore only knowable after apply, so plan it unknown
// whenever this integration is already changing.
//
// Reading the source here and planning its current agent as a known value
// aborts mid-apply with "Provider produced inconsistent final plan" when the
// same apply also moves the source to a different agent: the plan records the
// old agent, and the re-plan that follows the source's update reads the new
// one. An unchanged integration is left alone so plans stay empty - the
// attribute is Computed, so the prior state carries forward, and a cascade
// leaves state stale only until the next refresh.
//
// Alignment is not validated here either. Rejecting agent_id = "" against the
// source's current binding fails a configuration that unbinds the source in
// the same apply, because planning still sees the old binding. The backend
// enforces it at apply, after the source_id reference has ordered the source's
// update first.
func (r *integrationResource) ModifyPlan(
	ctx context.Context,
	req resource.ModifyPlanRequest,
	resp *resource.ModifyPlanResponse,
) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var configuredAgent types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("agent_id"), &configuredAgent)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// A written agent_id stands as configured, the "" sentinel included.
	if !configuredAgent.IsNull() {
		return
	}
	// With agent_id omitted the framework plans the prior state, so an equal
	// plan and state means nothing about this integration is changing.
	if req.Plan.Raw.Equal(req.State.Raw) {
		return
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("agent_id"), types.StringUnknown())...)
}

func NewIntegrationResource() resource.Resource {
	return &integrationResource{}
}

func (r *integrationResource) Configure(
	_ context.Context,
	req resource.ConfigureRequest,
	resp *resource.ConfigureResponse,
) {
	if req.ProviderData == nil {
		return
	}

	apiClient, ok := req.ProviderData.(*client.MatiaClient)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Provider Configure Type",
			"Expected *client.MatiaClient. Please report this issue to the provider developers.",
		)
		return
	}

	r.client = apiClient
}

func (r *integrationResource) Metadata(
	_ context.Context,
	req resource.MetadataRequest,
	resp *resource.MetadataResponse,
) {
	resp.TypeName = req.ProviderTypeName + "_integration"
}

func (r *integrationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "An integration binding a Matia source to a destination.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Integration ID assigned by Matia.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "Display name for the integration. When omitted, Matia assigns a default name.",
				Optional:    true,
				Computed:    true,
			},
			"source_id": schema.StringAttribute{
				Description: "Matia source asset ID. Changing this forces resource replacement.",
				Required:    true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"destination_id": schema.StringAttribute{
				Description: "Matia destination asset ID. Changing this forces resource replacement.",
				Required:    true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"destination_schema": schema.StringAttribute{
				Description: "Destination schema path for synced data.",
				Required:    true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"agent_id": schema.StringAttribute{
				Description: "Hybrid deployment agent ID for running the integration in your environment. " +
					"A bound source owns this: assigning or changing the source's agent cascades to its integrations, " +
					"and omitting the attribute keeps whatever the source cascaded - it reads as known after apply " +
					"whenever the integration is changing, because the cascade resolves it. Set it only for an " +
					"integration whose source has no agent - a source Matia Cloud can reach that loads into a " +
					"destination only the agent can reach. Set it to \"\" to detach that agent and move the " +
					"integration back to Matia Cloud; the API rejects that while the source is bound, because the " +
					"source takes precedence, so unbind the source in the same configuration to detach both at " +
					"once. Naming an agent other than a bound source's is rejected with SOURCE_AGENT_MISMATCH.",
				Optional: true,
				// Computed so an omitted attribute keeps the server's value rather
				// than planning null and detaching. No length validator: "" is the
				// explicit detach sentinel, so it has to reach the provider.
				Computed: true,
			},
			"on_schema_update": schema.StringAttribute{
				Description: "Schema change policy: enableAll, enableColumnChanges, enableNamespaceChanges, ignoreAll, or pauseConnection.",
				Optional:    true,
				Computed:    true,
				Validators: []validator.String{
					stringvalidator.OneOf(
						"enableAll",
						"enableColumnChanges",
						"enableNamespaceChanges",
						"ignoreAll",
						"pauseConnection",
					),
				},
			},
			"paused": schema.BoolAttribute{
				Description: "When true, the integration is paused (disabled). " +
					"Importing a paused integration keeps that value, so write it into the configuration: " +
					"omitting it falls back to the default and the next apply resumes the integration.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
			},
			"source_settings": schema.StringAttribute{
				Description: "Optional JSON object for advanced source settings passed to the Matia API. " +
					"The provider does not read these settings back, so they are null on an imported " +
					"integration, and if the configuration sets them after import, the resulting update " +
					"is rejected by the API unless it only sets customReports; use ignore_changes after import.",
				Optional: true,
			},
			"destination_settings": schema.StringAttribute{
				Description: "Optional JSON object for advanced destination settings passed to the Matia API. " +
					"Changing this forces resource replacement. The provider does not read these settings " +
					"back, so they are null on an imported integration and a configuration that sets them " +
					"plans a replacement after import.",
				Optional: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"destination_database": schema.StringAttribute{
				Description: "Snowflake database this integration loads into, chosen from the destination " +
					"asset's default_database and additional_databases. Omit it on creation to use the " +
					"default; once recorded, omitting it keeps the recorded selection. Changing it, or setting " +
					"it on an integration that has none recorded, forces resource replacement: the API " +
					"cannot move a synced integration.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplaceIfConfigured(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"destination_warehouse": schema.StringAttribute{
				Description: "Snowflake warehouse this integration runs on, chosen from the destination " +
					"asset's default_warehouse and additional_warehouses. Omit it on creation to use the " +
					"default; once recorded, omitting it keeps the recorded selection. Changing it, or setting " +
					"it on an integration that has none recorded, forces resource replacement.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplaceIfConfigured(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"tags": schema.ListAttribute{
				Description: "Tag IDs to associate with the integration. Changing this forces resource " +
					"replacement. The provider does not read tags back, so they are null on an imported " +
					"integration and a configuration that sets them plans a replacement after import.",
				Optional:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.List{
					listplanmodifier.RequiresReplace(),
				},
			},
		},
	}
}

func (r *integrationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan integrationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	createReq, diags := buildCreateIntegrationRequest(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	integration, err := r.client.Integrations.Create(ctx, createReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create integration", err.Error())
		return
	}

	if waitErr := r.client.Integrations.WaitForIntegrationReady(ctx, integration.ID); waitErr != nil {
		resp.Diagnostics.AddError("Failed waiting for integration to finish creation", waitErr.Error())
		return
	}

	integration, err = r.client.Integrations.Get(ctx, integration.ID)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read integration after create", err.Error())
		return
	}

	state := integrationToModel(integration, plan, preservePlanValue)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *integrationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state integrationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	integration, err := r.client.Integrations.Get(ctx, state.ID.ValueString())
	if errors.Is(err, client.ErrIntegrationNotFound) {
		resp.Diagnostics.AddWarning(
			"Integration Removed From State",
			fmt.Sprintf(
				"Matia returned 404 for integration %q. The resource was removed from Terraform state.",
				state.ID.ValueString(),
			),
		)
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read integration", err.Error())
		return
	}

	newState := integrationToModel(integration, state, refreshFromAPI)
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *integrationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state integrationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	modifyReq, modifyChanged, modifyDiags := buildModifyIntegrationRequest(plan, state)
	resp.Diagnostics.Append(modifyDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	if modifyChanged {
		if err := r.client.Integrations.Modify(ctx, state.ID.ValueString(), modifyReq); err != nil {
			resp.Diagnostics.AddError("Failed to update integration", err.Error())
			return
		}
	}

	integration, err := r.client.Integrations.Get(ctx, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to read integration after update", err.Error())
		return
	}

	newState := integrationToModel(integration, plan, preservePlanValue)
	newState.ID = state.ID
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *integrationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state integrationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.Integrations.Delete(ctx, state.ID.ValueString()); err != nil &&
		!errors.Is(err, client.ErrIntegrationNotFound) {
		resp.Diagnostics.AddError("Failed to delete integration", err.Error())
	}
}

func (r *integrationResource) ImportState(
	ctx context.Context,
	req resource.ImportStateRequest,
	resp *resource.ImportStateResponse,
) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func buildCreateIntegrationRequest(
	ctx context.Context,
	plan integrationModel,
) (client.CreateIntegrationRequest, diag.Diagnostics) {
	var diags diag.Diagnostics

	sourceSettings, settingsDiags := parseOptionalJSONObject(plan.SourceSettingsJSON, "source_settings")
	diags.Append(settingsDiags...)
	destinationSettings, destDiags := parseOptionalJSONObject(plan.DestinationSettingsJSON, "destination_settings")
	diags.Append(destDiags...)
	if diags.HasError() {
		return client.CreateIntegrationRequest{}, diags
	}

	var tagIDs []string
	if !plan.Tags.IsNull() && !plan.Tags.IsUnknown() {
		diags.Append(plan.Tags.ElementsAs(ctx, &tagIDs, false)...)
	}

	if !plan.DestinationDatabase.IsNull() && !plan.DestinationDatabase.IsUnknown() {
		destinationSettings[client.SelectedDatabaseKey] = plan.DestinationDatabase.ValueString()
	}
	if !plan.DestinationWarehouse.IsNull() && !plan.DestinationWarehouse.IsUnknown() {
		destinationSettings[client.SelectedWarehouseKey] = plan.DestinationWarehouse.ValueString()
	}

	enabled := !plan.Paused.ValueBool()

	req := client.CreateIntegrationRequest{
		SourceID:             plan.SourceID.ValueString(),
		DestinationID:        plan.DestinationID.ValueString(),
		ReplicationFrequency: "manual",
		DestinationSchema:    plan.DestinationSchema.ValueString(),
		SourceSettings:       sourceSettings,
		DestinationSettings:  destinationSettings,
		Enabled:              &enabled,
	}

	if !plan.Name.IsNull() && !plan.Name.IsUnknown() && plan.Name.ValueString() != "" {
		req.Name = plan.Name.ValueString()
	}

	if !plan.OnSchemaUpdate.IsNull() && !plan.OnSchemaUpdate.IsUnknown() && plan.OnSchemaUpdate.ValueString() != "" {
		req.OnSchemaUpdate = plan.OnSchemaUpdate.ValueString()
	}
	if len(tagIDs) > 0 {
		req.Tags = tagIDs
	}

	if !plan.AgentID.IsNull() && !plan.AgentID.IsUnknown() {
		// The "" sentinel is sent as an explicit null so a bound source rejects
		// it with SOURCE_AGENT_MISMATCH. Dropping it instead would let the
		// backend cascade the source's agent onto an integration whose
		// configuration asks for none.
		if plan.AgentID.ValueString() == "" {
			req.AgentID = (*string)(nil)
		} else {
			req.AgentID = plan.AgentID.ValueString()
		}
	}

	return req, diags
}

// Import runs Read with no prior state, so a user-set field falls back to the
// API value when the template has none; a populated template still wins, per
// the toModel template rule (P4) in internal-docs/CONTRIBUTING.md.
func templateOrAPIString(template types.String, apiValue string) types.String {
	if (template.IsUnknown() || template.IsNull()) && apiValue != "" {
		return types.StringValue(apiValue)
	}
	if template.IsUnknown() {
		return types.StringNull()
	}
	return template
}

// readPolicy decides which side wins for an attribute the user configures and
// the API echoes back. Read refreshes it from Matia, so an edit made outside
// Terraform is planned away instead of being absorbed into state; Create and
// Update keep the planned value, because the framework rejects an apply whose
// result differs from its plan.
type readPolicy bool

const (
	refreshFromAPI    readPolicy = true
	preservePlanValue readPolicy = false
)

func configuredString(template types.String, apiValue string, policy readPolicy) types.String {
	if policy == refreshFromAPI {
		return apiStringOrNull(apiValue)
	}
	return templateOrAPIString(template, apiValue)
}

func integrationToModel(
	integration *client.Integration,
	template integrationModel,
	policy readPolicy,
) *integrationModel {
	agentID := types.StringNull()
	if integration.AgentID != nil {
		agentID = types.StringValue(*integration.AgentID)
	} else if !template.AgentID.IsNull() && !template.AgentID.IsUnknown() &&
		template.AgentID.ValueString() == "" {
		// The API reports a detached agent as absent. Keeping the "" the
		// practitioner wrote stops it reading back as a null that differs from
		// the configuration on every plan.
		agentID = types.StringValue("")
	}
	// An absent settings block carries no selection to refresh from, so the
	// configured value stands rather than being cleared.
	selectionPolicy := policy
	if integration.DestinationSettings == nil {
		selectionPolicy = preservePlanValue
	}
	var selection client.IntegrationDestinationSettings
	if integration.DestinationSettings != nil {
		selection = *integration.DestinationSettings
	}

	return &integrationModel{
		ID:                      types.StringValue(integration.ID),
		Name:                    templateOrAPIString(template.Name, integration.Name),
		SourceID:                templateOrAPIString(template.SourceID, integration.Source.ID),
		DestinationID:           templateOrAPIString(template.DestinationID, integration.Destination.ID),
		DestinationSchema:       templateOrAPIString(template.DestinationSchema, integration.DestinationSchema),
		AgentID:                 agentID,
		OnSchemaUpdate:          templateOrAPIString(template.OnSchemaUpdate, integration.OnSchemaUpdate),
		Paused:                  types.BoolValue(integration.Paused),
		SourceSettingsJSON:      template.SourceSettingsJSON,
		DestinationSettingsJSON: template.DestinationSettingsJSON,
		DestinationDatabase: configuredString(
			template.DestinationDatabase, selection.SelectedDatabase, selectionPolicy,
		),
		DestinationWarehouse: configuredString(
			template.DestinationWarehouse, selection.SelectedWarehouse, selectionPolicy,
		),
		Tags: template.Tags,
	}
}

func buildModifyIntegrationRequest(
	plan, state integrationModel,
) (client.ModifyIntegrationRequest, bool, diag.Diagnostics) {
	var diags diag.Diagnostics
	var req client.ModifyIntegrationRequest
	changed := false

	if !plan.Paused.Equal(state.Paused) {
		paused := plan.Paused.ValueBool()
		req.Paused = &paused
		changed = true
	}

	if !plan.Name.IsNull() && !plan.Name.IsUnknown() && !plan.Name.Equal(state.Name) {
		req.Name = plan.Name.ValueString()
		changed = true
	}

	if !plan.SourceSettingsJSON.Equal(state.SourceSettingsJSON) &&
		!plan.SourceSettingsJSON.IsUnknown() {
		settings, settingsDiags := parseOptionalJSONObject(plan.SourceSettingsJSON, "source_settings")
		diags.Append(settingsDiags...)
		if diags.HasError() {
			return req, false, diags
		}
		req.SourceSettings = settings
		changed = true
	}

	if !plan.DestinationSchema.Equal(state.DestinationSchema) {
		req.DestinationSchema = plan.DestinationSchema.ValueString()
		changed = true
	}

	if !plan.OnSchemaUpdate.Equal(state.OnSchemaUpdate) &&
		!plan.OnSchemaUpdate.IsNull() &&
		!plan.OnSchemaUpdate.IsUnknown() {
		req.OnSchemaUpdate = plan.OnSchemaUpdate.ValueString()
		changed = true
	}

	if plan.AgentID.Equal(state.AgentID) || plan.AgentID.IsUnknown() {
		return req, changed, diags
	}

	changed = true
	// The API detaches on an explicit null, so the "" sentinel is sent as one.
	if plan.AgentID.IsNull() || plan.AgentID.ValueString() == "" {
		req.AgentID = (*string)(nil)
	} else {
		req.AgentID = plan.AgentID.ValueString()
	}

	return req, changed, diags
}

func parseOptionalJSONObject(value types.String, field string) (map[string]any, diag.Diagnostics) {
	if value.IsNull() || value.IsUnknown() || value.ValueString() == "" {
		return map[string]any{}, nil
	}
	return parseJSONObjectAttribute(value, field)
}

func parseJSONObjectAttribute(value types.String, field string) (map[string]any, diag.Diagnostics) {
	var diags diag.Diagnostics
	var out map[string]any
	if err := json.Unmarshal([]byte(value.ValueString()), &out); err != nil {
		diags.AddError(
			fmt.Sprintf("Invalid %s JSON", field),
			err.Error(),
		)
		return nil, diags
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, diags
}
