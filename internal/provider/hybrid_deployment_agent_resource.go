package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/matiadata/terraform-provider-matia/internal/provider/client"
)

type hybridDeploymentAgentResource struct {
	client *client.MatiaClient
}

type hybridDeploymentAgentModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	CreatedAt   types.String `tfsdk:"created_at"`
	Token       types.String `tfsdk:"token"`
}

var _ resource.Resource = &hybridDeploymentAgentResource{}
var _ resource.ResourceWithConfigure = &hybridDeploymentAgentResource{}

func NewHybridDeploymentAgentResource() resource.Resource {
	return &hybridDeploymentAgentResource{}
}

func (r *hybridDeploymentAgentResource) Configure(
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

func (r *hybridDeploymentAgentResource) Metadata(
	_ context.Context,
	req resource.MetadataRequest,
	resp *resource.MetadataResponse,
) {
	resp.TypeName = req.ProviderTypeName + "_hybrid_deployment_agent"
}

func (r *hybridDeploymentAgentResource) Schema(
	_ context.Context,
	_ resource.SchemaRequest,
	resp *resource.SchemaResponse,
) {
	resp.Schema = schema.Schema{
		Description: "A hybrid deployment agent for running Matia connectors in your environment.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Agent ID assigned by Matia.",
				Computed:    true,
			},
			"name": schema.StringAttribute{
				Description: "The unique name for the hybrid deployment agent.",
				Required:    true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"description": schema.StringAttribute{
				Description: "Optional description for the hybrid deployment agent.",
				Optional:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"created_at": schema.StringAttribute{
				Description: "Timestamp when the agent was created.",
				Computed:    true,
			},
			"token": schema.StringAttribute{
				Description: "One-time agent token returned on create. Use this to start the hybrid agent process.",
				Computed:    true,
				Sensitive:   true,
			},
		},
	}
}

func (r *hybridDeploymentAgentResource) Create(
	ctx context.Context,
	req resource.CreateRequest,
	resp *resource.CreateResponse,
) {
	var plan hybridDeploymentAgentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	createReq := client.CreateHybridDeploymentAgentRequest{
		Name: plan.Name.ValueString(),
	}
	if !plan.Description.IsNull() && !plan.Description.IsUnknown() {
		createReq.Description = plan.Description.ValueString()
	}

	agent, err := r.client.HybridDeploymentAgents.Create(ctx, createReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create hybrid deployment agent", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, hybridDeploymentAgentToModel(agent, plan))...)
}

func (r *hybridDeploymentAgentResource) Read(
	ctx context.Context,
	req resource.ReadRequest,
	resp *resource.ReadResponse,
) {
	var state hybridDeploymentAgentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	agent, err := r.client.HybridDeploymentAgents.Get(ctx, state.ID.ValueString())
	if err != nil {
		if errors.Is(err, client.ErrHybridDeploymentAgentNotFound) {
			resp.Diagnostics.AddWarning(
				"Hybrid Deployment Agent Removed From State",
				fmt.Sprintf(
					"Matia returned 404 for hybrid deployment agent %q. The resource was removed from Terraform state.",
					state.ID.ValueString(),
				),
			)
			resp.State.RemoveResource(ctx)
			return
		}

		resp.Diagnostics.AddError("Failed to read hybrid deployment agent", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, hybridDeploymentAgentToModel(agent, state))...)
}

// No updatable fields: name and description use RequiresReplace, so any change
// recreates the agent and Update is never called.
func (r *hybridDeploymentAgentResource) Update(
	_ context.Context,
	_ resource.UpdateRequest,
	_ *resource.UpdateResponse,
) {
}

func (r *hybridDeploymentAgentResource) Delete(
	ctx context.Context,
	req resource.DeleteRequest,
	resp *resource.DeleteResponse,
) {
	var state hybridDeploymentAgentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.HybridDeploymentAgents.Delete(ctx, state.ID.ValueString()); err != nil {
		if errors.Is(err, client.ErrHybridDeploymentAgentNotFound) {
			return
		}
		resp.Diagnostics.AddError("Failed to delete hybrid deployment agent", err.Error())
	}
}

func hybridDeploymentAgentToModel(
	agent *client.HybridDeploymentAgent,
	template hybridDeploymentAgentModel,
) *hybridDeploymentAgentModel {
	createdAt := template.CreatedAt
	if agent.CreatedAt != "" {
		createdAt = types.StringValue(agent.CreatedAt)
	}

	token := template.Token
	if agent.Token != "" {
		token = types.StringValue(agent.Token)
	} else if token.IsUnknown() {
		token = types.StringNull()
	}

	return &hybridDeploymentAgentModel{
		ID:          types.StringValue(agent.AgentID()),
		Name:        template.Name,
		Description: template.Description,
		CreatedAt:   createdAt,
		Token:       token,
	}
}
