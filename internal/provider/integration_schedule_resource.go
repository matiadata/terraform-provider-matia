package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/matiadata/terraform-provider-matia/internal/provider/client"
)

type integrationScheduleResource struct {
	client *client.MatiaClient
}

type integrationScheduleModel struct {
	IntegrationID        types.String `tfsdk:"integration_id"`
	ReplicationFrequency types.String `tfsdk:"replication_frequency"`
	CronExpression       types.String `tfsdk:"cron_expression"`
	BaseTime             types.String `tfsdk:"base_time"`
}

var (
	_ resource.Resource                = &integrationScheduleResource{}
	_ resource.ResourceWithImportState = &integrationScheduleResource{}
)

func NewIntegrationScheduleResource() resource.Resource {
	return &integrationScheduleResource{}
}

func (r *integrationScheduleResource) Metadata(
	_ context.Context,
	req resource.MetadataRequest,
	resp *resource.MetadataResponse,
) {
	resp.TypeName = req.ProviderTypeName + "_integration_schedule"
}

func (r *integrationScheduleResource) Schema(
	_ context.Context,
	_ resource.SchemaRequest,
	resp *resource.SchemaResponse,
) {
	resp.Schema = schema.Schema{
		Description: "Sync schedule for a Matia integration (PATCH /v1/integrations/:id).",
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
			"replication_frequency": schema.StringAttribute{
				Description: "Sync schedule: manual, hourly, daily, cron, or a Matia API value (e.g. 60, 1440, cron).",
				Required:    true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"cron_expression": schema.StringAttribute{
				Description: "Cron expression when replication_frequency is cron. " +
					"Matia keeps its stored value when the configuration omits this, so removing " +
					"the attribute after setting it leaves the schedule unchanged rather than clearing it. " +
					"Once Terraform holds an expression in state, after an import or an earlier apply, " +
					"switching replication_frequency to cron reuses it rather than requiring it again.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"base_time": schema.StringAttribute{
				Description: "Base time for scheduled syncs. " +
					"Matia keeps its stored value when the configuration omits this, so removing " +
					"the attribute after setting it leaves the schedule unchanged rather than clearing it.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *integrationScheduleResource) Configure(
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

func (r *integrationScheduleResource) Create(
	ctx context.Context,
	req resource.CreateRequest,
	resp *resource.CreateResponse,
) {
	var plan integrationScheduleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	state, diags := r.applySchedule(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *integrationScheduleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state integrationScheduleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	integrationID := state.IntegrationID.ValueString()
	integration, err := r.client.Integrations.Get(ctx, integrationID)
	if errors.Is(err, client.ErrIntegrationNotFound) {
		resp.Diagnostics.AddWarning(
			"Integration Schedule Removed From State",
			fmt.Sprintf(
				"Matia returned 404 for integration %q. The resource was removed from Terraform state.",
				integrationID,
			),
		)
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read integration schedule", err.Error())
		return
	}

	newState := scheduleToModel(integration, integrationID, state)
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *integrationScheduleResource) Update(
	ctx context.Context,
	req resource.UpdateRequest,
	resp *resource.UpdateResponse,
) {
	var plan integrationScheduleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	newState, diags := r.applySchedule(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *integrationScheduleResource) Delete(_ context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
}

// The schedule lives on the integration rather than as its own API object, so
// the import id is the integration id; Read then fills the rest from the API.
func (r *integrationScheduleResource) ImportState(
	ctx context.Context,
	req resource.ImportStateRequest,
	resp *resource.ImportStateResponse,
) {
	resource.ImportStatePassthroughID(ctx, path.Root("integration_id"), req, resp)
}

func (r *integrationScheduleResource) applySchedule(
	ctx context.Context,
	plan integrationScheduleModel,
) (*integrationScheduleModel, diag.Diagnostics) {
	var diags diag.Diagnostics

	integrationID := plan.IntegrationID.ValueString()
	if err := r.client.Integrations.WaitForIntegrationReady(ctx, integrationID); err != nil {
		diags.AddError("Failed waiting for integration to finish creation", err.Error())
		return nil, diags
	}

	modifyReq, buildDiags := buildScheduleModifyRequest(plan)
	diags.Append(buildDiags...)
	if diags.HasError() {
		return nil, diags
	}

	if err := r.client.Integrations.ModifyWithRetry(ctx, integrationID, modifyReq); err != nil {
		diags.AddError("Failed to apply integration schedule", err.Error())
		return nil, diags
	}

	if err := r.client.Integrations.WaitForScheduleApplied(
		ctx,
		integrationID,
		schedulePlanFromModel(plan),
	); err != nil {
		diags.AddError("Failed to verify integration schedule", err.Error())
		return nil, diags
	}

	state, stateDiags := scheduleFromIntegration(ctx, integrationID, plan, r.client)
	diags.Append(stateDiags...)
	if diags.HasError() {
		return nil, diags
	}

	return state, diags
}

func buildScheduleModifyRequest(plan integrationScheduleModel) (client.ModifyIntegrationRequest, diag.Diagnostics) {
	var diags diag.Diagnostics

	frequency, err := client.ResolveReplicationFrequency(plan.ReplicationFrequency.ValueString())
	if err != nil {
		diags.AddError("Invalid replication_frequency", err.Error())
		return client.ModifyIntegrationRequest{}, diags
	}

	if frequency == "cron" && !scheduleValueSet(plan.CronExpression) {
		diags.AddError(
			"Missing cron_expression",
			"cron_expression is required when replication_frequency is cron.",
		)
	}

	req := client.ModifyIntegrationRequest{
		ReplicationFrequency: frequency,
	}

	if scheduleValueSet(plan.CronExpression) {
		req.CronExpression = plan.CronExpression.ValueString()
	}
	if scheduleValueSet(plan.BaseTime) {
		req.BaseTime = plan.BaseTime.ValueString()
	}

	return req, diags
}

func scheduleFromIntegration(
	ctx context.Context,
	integrationID string,
	template integrationScheduleModel,
	apiClient *client.MatiaClient,
) (*integrationScheduleModel, diag.Diagnostics) {
	var diags diag.Diagnostics

	integration, err := apiClient.Integrations.Get(ctx, integrationID)
	if errors.Is(err, client.ErrIntegrationNotFound) {
		diags.AddError("Integration not found", fmt.Sprintf("Matia returned 404 for integration %q.", integrationID))
		return nil, diags
	}
	if err != nil {
		diags.AddError("Failed to read integration schedule", err.Error())
		return nil, diags
	}

	return scheduleToModel(integration, integrationID, template), diags
}

func scheduleToModel(
	integration *client.Integration,
	integrationID string,
	template integrationScheduleModel,
) *integrationScheduleModel {
	frequency := template.ReplicationFrequency
	if integration.ReplicationFrequency != "" {
		userFrequency := ""
		if !template.ReplicationFrequency.IsNull() {
			userFrequency = template.ReplicationFrequency.ValueString()
		}
		frequency = types.StringValue(
			ReplicationFrequencyForState(userFrequency, integration.ReplicationFrequency),
		)
	}

	return &integrationScheduleModel{
		IntegrationID:        types.StringValue(integrationID),
		ReplicationFrequency: frequency,
		CronExpression:       scheduleValueForState(template.CronExpression, integration.CronExpression),
		BaseTime:             scheduleValueForState(template.BaseTime, integration.BaseTime),
	}
}

// These attributes are Computed, so the plan leaves them unknown whenever the
// configuration omits them on create. Unknown is not a valid state value, so an
// attribute the API reports nothing for has to land in state as null.
//
// An empty apiValue falls back to the planned/prior value rather than clearing
// state, so a field cleared out of band is not detected as drift. Terraform
// never clears one itself - the provider only ever omits a field, and an
// omitted field leaves Matia's stored value untouched - but other clients can:
// the Matia UI sends an explicit null, which the API honours.
func scheduleValueForState(planned types.String, apiValue string) types.String {
	if apiValue != "" {
		return types.StringValue(apiValue)
	}
	if planned.IsUnknown() {
		return types.StringNull()
	}
	return planned
}

// Unknown means the config is silent and no prior state filled the gap in, so
// it carries no user value the API should be told about.
func scheduleValueSet(planned types.String) bool {
	return !planned.IsNull() && !planned.IsUnknown() && planned.ValueString() != ""
}

func schedulePlanFromModel(plan integrationScheduleModel) client.SchedulePlan {
	schedulePlan := client.SchedulePlan{
		ReplicationFrequency: plan.ReplicationFrequency.ValueString(),
	}
	if scheduleValueSet(plan.CronExpression) {
		schedulePlan.CronExpression = plan.CronExpression.ValueString()
	}
	if scheduleValueSet(plan.BaseTime) {
		schedulePlan.BaseTime = plan.BaseTime.ValueString()
	}
	return schedulePlan
}
