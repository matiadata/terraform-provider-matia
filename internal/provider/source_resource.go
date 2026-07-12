package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/matiadata/terraform-provider-matia/internal/provider/client"
)

type assetKind string

const (
	assetKindSource      assetKind = "source"
	assetKindDestination assetKind = "destination"
)

type assetResource struct {
	client *client.MatiaClient
	kind   assetKind
}

type assetModel struct {
	ID                types.String `tfsdk:"id"`
	Name              types.String `tfsdk:"name"`
	Type              types.String `tfsdk:"type"`
	Description       types.String `tfsdk:"description"`
	ConnectionConfig  types.String `tfsdk:"connection_config"`
	ConnectionSecrets types.String `tfsdk:"connection_secrets"`
	AuthMethod        types.String `tfsdk:"auth_method"`
	IsDraft           types.Bool   `tfsdk:"is_draft"`
	Tags              types.List   `tfsdk:"tags"`
}

type assetSchemaText struct {
	resourceDesc      string
	name              string
	assetType         string
	description       string
	connectionConfig  string
	connectionSecrets string
}

var _ resource.Resource = &assetResource{}

func NewSourceResource() resource.Resource {
	return &assetResource{kind: assetKindSource}
}

func (r *assetResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *assetResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + string(r.kind)
}

func (r *assetResource) schemaText() assetSchemaText {
	switch r.kind {
	case assetKindSource:
		return assetSchemaText{
			resourceDesc:      "A Matia source asset - a connector Matia reads data from.",
			name:              "The display name of the source asset.",
			assetType:         "The connector type (e.g. postgres, salesforce). Changing this forces resource replacement.",
			description:       "A human-readable description of the source asset.",
			connectionConfig:  "JSON object with connector-specific configuration for the source.",
			connectionSecrets: "JSON object with sensitive connector configuration (e.g. API keys, tokens).",
		}
	case assetKindDestination:
		return assetSchemaText{
			resourceDesc:      "A Matia destination asset - a connector Matia writes data to.",
			name:              "The display name of the destination asset.",
			assetType:         "The connector type (e.g. snowflake, bigquery). Changing this forces resource replacement.",
			description:       "A human-readable description of the destination asset.",
			connectionConfig:  "JSON object with connector-specific configuration for the destination.",
			connectionSecrets: "JSON object with sensitive connector configuration (e.g. passwords, tokens).",
		}
	default:
		return assetSchemaText{}
	}
}

func (r *assetResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	text := r.schemaText()
	resp.Schema = schema.Schema{
		Description: text.resourceDesc,
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The asset ID assigned by Matia.",
				Computed:    true,
			},
			"name": schema.StringAttribute{
				Description: text.name,
				Required:    true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"type": schema.StringAttribute{
				Description: text.assetType,
				Required:    true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"description": schema.StringAttribute{
				Description: text.description,
				Optional:    true,
			},
			"connection_config": schema.StringAttribute{
				Description: text.connectionConfig,
				Required:    true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"connection_secrets": schema.StringAttribute{
				Description: text.connectionSecrets,
				Optional:    true,
				Sensitive:   true,
			},
			"auth_method": schema.StringAttribute{
				Description: "The authentication method for the connector (e.g. direct). Defaults to direct.",
				Optional:    true,
			},
			"is_draft": schema.BoolAttribute{
				Description: "Whether the asset is created as a draft. Changing this forces resource replacement.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.RequiresReplace(),
				},
			},
			"tags": schema.ListAttribute{
				Description: "Tag IDs to associate with the asset. Changing this forces resource replacement.",
				Optional:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.List{
					listplanmodifier.RequiresReplace(),
				},
			},
		},
	}
}

func (r *assetResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan assetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var config assetModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var connectionSecrets types.String
	if !config.ConnectionSecrets.IsNull() && !config.ConnectionSecrets.IsUnknown() {
		connectionSecrets = config.ConnectionSecrets
	}
	var tagIDs []string
	if !plan.Tags.IsNull() && !plan.Tags.IsUnknown() {
		resp.Diagnostics.Append(plan.Tags.ElementsAs(ctx, &tagIDs, false)...)
	}
	if resp.Diagnostics.HasError() {
		return
	}

	mergedConnection, mergeDiags := mergeConnectionFields(plan.ConnectionConfig, connectionSecrets)
	resp.Diagnostics.Append(mergeDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	description := ""
	if !plan.Description.IsNull() && plan.Description.ValueString() != "" {
		description = plan.Description.ValueString()
	}
	authMethod := ""
	if !plan.AuthMethod.IsNull() && plan.AuthMethod.ValueString() != "" {
		authMethod = plan.AuthMethod.ValueString()
	}

	createReq := buildCreateAssetRequest(
		plan.Name.ValueString(),
		plan.Type.ValueString(),
		description,
		authMethod,
		mergedConnection,
		string(r.kind),
		tagIDs,
	)

	asset, err := r.client.Assets.Create(ctx, createReq)
	if err != nil {
		resp.Diagnostics.AddError(fmt.Sprintf("Failed to create %s", r.kind), err.Error())
		return
	}

	state, diags := assetToModel(asset, effectiveAssetTemplate(plan, config))
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *assetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state assetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	asset, err := r.client.Assets.Get(ctx, state.ID.ValueString())
	if errors.Is(err, client.ErrAssetNotFound) {
		label := strings.ToUpper(string(r.kind)[:1]) + string(r.kind)[1:]
		resp.Diagnostics.AddWarning(
			fmt.Sprintf("%s Removed From State", label),
			fmt.Sprintf(
				"Matia returned 404 for %s %q. The resource was removed from Terraform state. "+
					"If the asset still exists, verify GET /v1/assets/:id is available on your api_url.",
				r.kind,
				state.ID.ValueString(),
			),
		)
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError(fmt.Sprintf("Failed to read %s", r.kind), err.Error())
		return
	}

	newState, diags := assetToModel(asset, state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *assetResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state assetModel
	var config assetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	updateReq, changed, diags := buildAssetUpdateRequest(
		plan.Name, state.Name,
		plan.Description, state.Description,
		plan.AuthMethod, state.AuthMethod,
		plan.ConnectionConfig, state.ConnectionConfig,
		plan.ConnectionSecrets, config.ConnectionSecrets,
	)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	if changed {
		if err := r.client.Assets.Update(ctx, state.ID.ValueString(), updateReq); err != nil {
			resp.Diagnostics.AddError(fmt.Sprintf("Failed to update %s", r.kind), err.Error())
			return
		}
	}

	asset, err := r.client.Assets.Get(ctx, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(fmt.Sprintf("Failed to read %s after update", r.kind), err.Error())
		return
	}

	newState, stateDiags := assetToModel(asset, effectiveAssetTemplate(plan, config))
	resp.Diagnostics.Append(stateDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *assetResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state assetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.Assets.Delete(ctx, state.ID.ValueString())
	if err != nil && !errors.Is(err, client.ErrAssetNotFound) {
		resp.Diagnostics.AddError(fmt.Sprintf("Failed to delete %s", r.kind), err.Error())
	}
}
