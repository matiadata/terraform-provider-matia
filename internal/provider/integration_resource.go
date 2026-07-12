package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
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
	Tags                    types.List   `tfsdk:"tags"`
}

var _ resource.Resource = &integrationResource{}

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
			},
			"name": schema.StringAttribute{
				Description: "Display name for the integration. When omitted, Matia assigns a default name.",
				Optional:    true,
				Computed:    true,
			},
			"source_id": schema.StringAttribute{
				Description: "Matia source asset ID.",
				Required:    true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"destination_id": schema.StringAttribute{
				Description: "Matia destination asset ID.",
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
				Description: "Hybrid deployment agent ID for running the integration in your environment.",
				Optional:    true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
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
				Description: "When true, the integration is paused (disabled).",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
			"source_settings": schema.StringAttribute{
				Description: "Optional JSON object for advanced source settings passed to the Matia API.",
				Optional:    true,
			},
			"destination_settings": schema.StringAttribute{
				Description: "Optional JSON object for advanced destination settings passed to the Matia API.",
				Optional:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"tags": schema.ListAttribute{
				Description: "Tag IDs to associate with the integration.",
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

	state := integrationToModel(integration, plan)
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

	newState := integrationToModel(integration, state)
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

	newState := integrationToModel(integration, plan)
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
		req.AgentID = plan.AgentID.ValueString()
	}

	return req, diags
}

func integrationToModel(integration *client.Integration, template integrationModel) *integrationModel {
	name := template.Name
	if (name.IsUnknown() || name.IsNull()) && integration.Name != "" {
		name = types.StringValue(integration.Name)
	} else if name.IsUnknown() {
		name = types.StringNull()
	}

	onSchemaUpdate := template.OnSchemaUpdate
	if onSchemaUpdate.IsUnknown() || onSchemaUpdate.IsNull() {
		if integration.OnSchemaUpdate != "" {
			onSchemaUpdate = types.StringValue(integration.OnSchemaUpdate)
		} else if onSchemaUpdate.IsUnknown() {
			onSchemaUpdate = types.StringNull()
		}
	}

	agentID := types.StringNull()
	if integration.AgentID != nil {
		agentID = types.StringValue(*integration.AgentID)
	}

	return &integrationModel{
		ID:                      types.StringValue(integration.ID),
		Name:                    name,
		SourceID:                template.SourceID,
		DestinationID:           template.DestinationID,
		DestinationSchema:       template.DestinationSchema,
		AgentID:                 agentID,
		OnSchemaUpdate:          onSchemaUpdate,
		Paused:                  types.BoolValue(integration.Paused),
		SourceSettingsJSON:      template.SourceSettingsJSON,
		DestinationSettingsJSON: template.DestinationSettingsJSON,
		Tags:                    template.Tags,
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
	if plan.AgentID.IsNull() {
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
