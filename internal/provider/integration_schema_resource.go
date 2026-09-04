package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
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
	IntegrationID   types.String         `tfsdk:"integration_id"`
	Config          jsontypes.Normalized `tfsdk:"config"`
	EffectiveSchema jsontypes.Normalized `tfsdk:"effective_schema"`
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
				// The two-space indent on every line after the first is load-bearing: tfplugindocs
				// renders each attribute as one list item, so unindented continuation lines end the
				// list and the following attribute is swallowed into these bullets.
				MarkdownDescription: "Desired schema selection: the JSON body of `PATCH /v1/integrations/:id/schemas`, " +
					"with a top-level `schemas` key holding only the tables and fields you want to manage.\n\n" +
					"  This is **not** the enriched catalog `GET` returns, so do not paste a `GET` response " +
					"here. Terraform manages only the keys you declare and ignores discovered tables, " +
					"destination names, columns and primary keys you omit; read `effective_schema` to see " +
					"the full catalog.\n\n" +
					"  - **Removing a table** whose `enabled` you had declared disables it: deleting it " +
					"from `config` sends `enabled: false` on the next apply. Ownership is per field, so " +
					"a table you declared without `enabled` (a `syncMode`-only entry, say) is left " +
					"running - Terraform never turned it on, so removing it does not turn it off - and a " +
					"table you never declared is untouched entirely. That is what keeps this from " +
					"undoing the integration's own `on_schema_update` policy. If a removed table cannot " +
					"be disabled, the apply warns instead of reporting a clean run.\n" +
					"  - **Settable keys** are a table's `enabled`, `syncMode` and `cursorField`, and a " +
					"column's `enabled` and `hashed`. Anything else is sent but ignored by the API, so it " +
					"stays in state without ever taking effect.\n" +
					"  - **Drift detection:** on refresh these keys are compared against the API, so a change " +
					"made outside Terraform shows up as a diff. Equivalent `syncMode` spellings (`cdc` and " +
					"`change_stream`) compare equal.\n" +
					"  - **Deliberately not reported as drift:** entries the API cannot write back - ones the " +
					"catalog no longer lists, flattened subtables, and primary-key columns. Inspect " +
					"`effective_schema` for their real state. A whole schema the catalog no longer " +
					"lists is kept too, but then the API rejects every later apply on this resource " +
					"with `" + client.SchemaNotFoundMessage + "` until you remove it, so refresh " +
					"warns when it finds one.\n" +
					"  - **Caveat:** a column's `enabled` only takes effect on sources that support " +
					"field-level data blocking. Elsewhere the API keeps the column enabled and the " +
					"difference keeps appearing in the plan.",
				Required:   true,
				CustomType: jsontypes.NormalizedType{},
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"effective_schema": schema.StringAttribute{
				Description: "The full enriched schema catalog as returned by GET /v1/integrations/:id/schemas, including tables you did not declare in `config`, their columns, primary keys, and destination names. Read-only, and wider than what you can change: the schemas API only accepts a table's `enabled`, `syncMode` and `cursorField` and a column's `enabled` and `hashed`. Destination names, primary keys and discovered structure are reported here but cannot be set through `config`.",
				Computed:    true,
				CustomType:  jsontypes.NormalizedType{},
				// No UseStateForUnknown: this mirrors the catalog, which changes exactly when
				// config does, so pinning the plan to the stale value would make every apply
				// fail the post-apply consistency check. The framework already marks it unknown
				// only when the plan differs from prior state, which is the behaviour we want.
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

	// No prior state on Create, so nothing is owned yet and nothing can have been dropped: the
	// first apply stays sparse and never disables a table the user has not declared.
	state, diags := r.applySchema(ctx, plan, plan.Config.ValueString())
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

	catalog, err := r.client.Integrations.GetSchemaConfig(ctx, state.IntegrationID.ValueString())
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

	refreshed, diags := refreshIntegrationSchemaModel(catalog, state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, refreshed)...)
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

	planCanonical, diags := canonicalizeSchemaConfigForComparison(plan.Config.ValueString())
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	stateCanonical, stateDiags := canonicalizeSchemaConfigForComparison(state.Config.ValueString())
	resp.Diagnostics.Append(stateDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	if planCanonical == stateCanonical {
		// No-op (only equivalent syncMode aliases differ), so skip the PATCH - but persist the
		// plan, not the old state: config's planned value is the user's new text, and writing back
		// the old text trips Terraform's "provider produced inconsistent result after apply" check.
		noop := plan
		noop.EffectiveSchema = state.EffectiveSchema
		resp.Diagnostics.Append(resp.State.Set(ctx, noop)...)
		return
	}

	// Safe to run after the no-op check: a disable is only emitted for a table whose prior entry
	// declared enabled, and canonicalizeSchemaConfig keeps that field, so a dropped table needing
	// one always leaves the two canonical forms unequal.
	configToSend, expandDiags := addDisablesForDroppedTables(
		state.Config.ValueString(),
		plan.Config.ValueString(),
		state.EffectiveSchema.ValueString(),
	)
	resp.Diagnostics.Append(expandDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	newState, applyDiags := r.applySchema(ctx, plan, configToSend)
	resp.Diagnostics.Append(applyDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *integrationSchemaResource) Delete(_ context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
}

// applySchema PATCHes configToSend and stores the result. configToSend is separate from
// plan.Config because Update also has to disable the tables the plan dropped, which are by
// definition absent from the text the user wrote - and state must still echo that text verbatim.
func (r *integrationSchemaResource) applySchema(
	ctx context.Context,
	plan integrationSchemaModel,
	configToSend string,
) (*integrationSchemaModel, diag.Diagnostics) {
	var diags diag.Diagnostics

	configJSON, prepareDiags := prepareSchemaConfigForAPI(configToSend)
	diags.Append(prepareDiags...)
	if diags.HasError() {
		return nil, diags
	}

	integrationID := plan.IntegrationID.ValueString()
	if err := r.client.Integrations.WaitForIntegrationReady(ctx, integrationID); err != nil {
		diags.AddError("Failed waiting for integration to finish creation", err.Error())
		return nil, diags
	}

	// The schemas PATCH replies with the same enriched catalog GET returns, so effective_schema
	// needs no extra round trip.
	catalog, err := r.client.Integrations.UpdateSchemaConfig(ctx, integrationID, configJSON)
	if err != nil {
		diags.AddError("Failed to apply integration schema", err.Error())
		return nil, diags
	}

	return integrationSchemaToModel(catalog, plan), diags
}

// integrationSchemaToModel builds the post-apply state: config echoes the plan verbatim, because
// it is Required and Terraform rejects an applied value that differs from the planned one. Any
// coercion the API applied surfaces on the next refresh, through refreshIntegrationSchemaModel.
//
// Writing a server-canonicalised config here is not an option even in principle: Create and
// Update run semantic equality against the PLAN rather than prior state, so an equal value is
// reverted to the plan and an unequal one is rejected by Terraform. Reconciliation belongs in
// Read.
func integrationSchemaToModel(catalog string, template integrationSchemaModel) *integrationSchemaModel {
	return &integrationSchemaModel{
		IntegrationID:   template.IntegrationID,
		Config:          template.Config,
		EffectiveSchema: jsontypes.NewNormalizedValue(catalog),
	}
}

// refreshIntegrationSchemaModel rebuilds state from the API: config keeps the user's declared key
// set but takes the server's current values, so an edit made outside Terraform becomes a diff.
func refreshIntegrationSchemaModel(
	catalog string,
	state integrationSchemaModel,
) (*integrationSchemaModel, diag.Diagnostics) {
	projected, diags := projectSchemaConfig(state.Config.ValueString(), catalog)
	if diags.HasError() {
		return nil, diags
	}

	return &integrationSchemaModel{
		IntegrationID:   state.IntegrationID,
		Config:          jsontypes.NewNormalizedValue(projected),
		EffectiveSchema: jsontypes.NewNormalizedValue(catalog),
	}, diags
}
