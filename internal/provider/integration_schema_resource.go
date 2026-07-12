package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/matiadata/terraform-provider-matia/internal/provider/client"
)

type integrationSchemaResource struct {
	client *client.MatiaClient
}

type integrationSchemaModel struct {
	IntegrationID types.String `tfsdk:"integration_id"`
	Config        types.String `tfsdk:"config"`
}

var _ resource.Resource = &integrationSchemaResource{}

func NewIntegrationSchemaResource() resource.Resource {
	return &integrationSchemaResource{}
}

func (r *integrationSchemaResource) Configure(
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

func (r *integrationSchemaResource) Metadata(
	_ context.Context,
	req resource.MetadataRequest,
	resp *resource.MetadataResponse,
) {
	resp.TypeName = req.ProviderTypeName + "_integration_schema"
}

func (r *integrationSchemaResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Stream and table selection for a Matia integration (PATCH /v1/integrations/:id/schemas).",
		Attributes: map[string]schema.Attribute{
			"integration_id": schema.StringAttribute{
				Description: "Matia integration ID.",
				Required:    true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"config": schema.StringAttribute{
				Description: "Desired schema selection as a JSON object with a top-level `schemas` key: the PATCH request body for /v1/integrations/:id/schemas, containing only the tables and fields you want to manage. This is not the enriched catalog that GET returns - Terraform stores this value verbatim and does not manage discovered tables, destination names, columns, or primary keys it omits, so do not paste a GET response here.",
				Required:    true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
		},
	}
}

func (r *integrationSchemaResource) Create(
	ctx context.Context,
	req resource.CreateRequest,
	resp *resource.CreateResponse,
) {
	var plan integrationSchemaModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	state, diags := r.applySchema(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *integrationSchemaResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state integrationSchemaModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	_, err := r.client.Integrations.GetSchemaConfig(ctx, state.IntegrationID.ValueString())
	if errors.Is(err, client.ErrIntegrationNotFound) {
		resp.Diagnostics.AddWarning(
			"Integration Schema Removed From State",
			fmt.Sprintf(
				"Matia returned 404 for integration %q. The resource was removed from Terraform state.",
				state.IntegrationID.ValueString(),
			),
		)
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read integration schema", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, integrationSchemaToModel(state))...)
}

func (r *integrationSchemaResource) Update(
	ctx context.Context,
	req resource.UpdateRequest,
	resp *resource.UpdateResponse,
) {
	var plan, state integrationSchemaModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	planCanonical, diags := canonicalizeSchemaConfigForState(plan.Config.ValueString())
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	stateCanonical, stateDiags := canonicalizeSchemaConfigForState(state.Config.ValueString())
	resp.Diagnostics.Append(stateDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	if planCanonical == stateCanonical {
		resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
		return
	}

	newState, applyDiags := r.applySchema(ctx, plan)
	resp.Diagnostics.Append(applyDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *integrationSchemaResource) Delete(_ context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
}

func (r *integrationSchemaResource) applySchema(
	ctx context.Context,
	plan integrationSchemaModel,
) (*integrationSchemaModel, diag.Diagnostics) {
	var diags diag.Diagnostics

	configJSON, prepareDiags := prepareSchemaConfigForAPI(plan.Config.ValueString())
	diags.Append(prepareDiags...)
	if diags.HasError() {
		return nil, diags
	}

	integrationID := plan.IntegrationID.ValueString()
	if err := r.client.Integrations.WaitForIntegrationReady(ctx, integrationID); err != nil {
		diags.AddError("Failed waiting for integration to finish creation", err.Error())
		return nil, diags
	}

	if _, err := r.client.Integrations.UpdateSchemaConfig(ctx, integrationID, configJSON); err != nil {
		diags.AddError("Failed to apply integration schema", err.Error())
		return nil, diags
	}

	return integrationSchemaToModel(plan), diags
}

func integrationSchemaToModel(template integrationSchemaModel) *integrationSchemaModel {
	// State is the user's config, not the API's enriched response (extra columns, enabled defaults,
	// discovered tables): config is Required, so post-apply state must equal the plan.
	return &integrationSchemaModel{
		IntegrationID: template.IntegrationID,
		Config:        template.Config,
	}
}
