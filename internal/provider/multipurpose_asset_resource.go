package provider

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/matiadata/terraform-provider-matia/internal/provider/client"
)

const multiPurposeConnectionType = "multi_purpose"

// multiPurposeAssetResource is `matia_asset`: one backend object that an
// integration can use as its source or its destination, so it fits neither
// matia_source nor matia_destination. Today the API creates only Snowflake
// assets this way.
type multiPurposeAssetResource struct {
	client *client.MatiaClient
}

type multiPurposeAssetModel struct {
	ID                   types.String `tfsdk:"id"`
	Name                 types.String `tfsdk:"name"`
	Type                 types.String `tfsdk:"type"`
	Description          types.String `tfsdk:"description"`
	AuthMethod           types.String `tfsdk:"auth_method"`
	Owners               types.List   `tfsdk:"owners"`
	Tags                 types.List   `tfsdk:"tags"`
	Credentials          types.Object `tfsdk:"credentials"`
	Etl                  types.Object `tfsdk:"etl"`
	ReverseEtl           types.Object `tfsdk:"reverse_etl"`
	Catalog              types.Object `tfsdk:"catalog"`
	EtlSource            types.Object `tfsdk:"etl_source"`
	AdditionalDatabases  types.List   `tfsdk:"additional_databases"`
	AdditionalWarehouses types.List   `tfsdk:"additional_warehouses"`
	ConnectionType       types.String `tfsdk:"connection_type"`
	DefaultDatabase      types.String `tfsdk:"default_database"`
	DefaultWarehouse     types.String `tfsdk:"default_warehouse"`
}

var (
	_ resource.Resource                   = &multiPurposeAssetResource{}
	_ resource.ResourceWithImportState    = &multiPurposeAssetResource{}
	_ resource.ResourceWithValidateConfig = &multiPurposeAssetResource{}
)

func NewMultiPurposeAssetResource() resource.Resource {
	return &multiPurposeAssetResource{}
}

func (r *multiPurposeAssetResource) Configure(
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

func (r *multiPurposeAssetResource) Metadata(
	_ context.Context,
	req resource.MetadataRequest,
	resp *resource.MetadataResponse,
) {
	resp.TypeName = req.ProviderTypeName + "_asset"
}

func (r *multiPurposeAssetResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A multipurpose Matia asset: one connector that integrations can use as an ETL " +
			"destination, a reverse-ETL source, an observability target and optionally an ETL source. " +
			"Currently supported for Snowflake. Give it shared `credentials`, or one " +
			"credentials block per purpose; a purpose block set alongside `credentials` replaces the " +
			"shared credentials for that purpose only. Matia cannot change the credentials of an " +
			"existing multipurpose asset, so any credentials change forces resource replacement, and " +
			"every integration bound to the asset is replaced with it.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The asset ID assigned by Matia.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The display name of the asset.",
				Required:    true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"type": schema.StringAttribute{
				Description: "The connector type. Only snowflake is supported.",
				Required:    true,
				Validators: []validator.String{
					stringvalidator.OneOf("snowflake"),
				},
				// Unreachable while the validator accepts one value; it arms the
				// replacement for when the API creates other multipurpose types.
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"description": schema.StringAttribute{
				Description: "A human-readable description of the asset. Removing it clears the description in Matia.",
				Optional:    true,
			},
			"auth_method": schema.StringAttribute{
				Description: "The authentication method of the credentials: direct for passwords, keyPair for private keys.",
				Optional:    true,
				Validators: []validator.String{
					stringvalidator.OneOf("direct", "keyPair"),
				},
			},
			"owners": schema.ListAttribute{
				Description: "User IDs that own the asset, as published by GET /v1/users. The provider does not " +
					"read owners back, so they are null on an imported asset until the next apply. Removing " +
					"the attribute leaves the owners recorded in Matia unchanged; set [] to clear them.",
				Optional:    true,
				ElementType: types.StringType,
			},
			"tags": schema.ListAttribute{
				Description: "Tag IDs to associate with the asset. Changing this forces resource replacement. " +
					"The provider does not read tags back, so they are null on an imported asset and a " +
					"configuration that sets them plans a replacement after import.",
				Optional:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.List{
					listplanmodifier.RequiresReplace(),
				},
			},
			"credentials": snowflakeCredentialsSchema(
				"Credentials shared by every purpose. When set, etl, reverse_etl, catalog and etl_source " +
					"are optional and each one given replaces the shared credentials for that purpose.",
			),
			"etl": snowflakeCredentialsSchema(
				"Credentials Matia uses to load data into Snowflake. Required unless credentials is set.",
			),
			"reverse_etl": snowflakeCredentialsSchema(
				"Credentials Matia uses to read data out of Snowflake for reverse ETL. Required unless credentials is set.",
			),
			"catalog": snowflakeCredentialsSchema(
				"Credentials Matia uses for observability. Required unless credentials is set.",
			),
			"etl_source": snowflakeCredentialsSchema(
				"Credentials Matia uses to read Snowflake as an ETL source. Supplying them enables that purpose.",
			),
			"additional_databases": schema.ListAttribute{
				Description: "Existing Snowflake databases integrations may load into besides the ETL " +
					"credentials' default database. Omit it to leave the list unmanaged; set [] to clear it. " +
					"Matia trims and de-duplicates names case-insensitively without planning a change.",
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.List{
					listplanmodifier.UseStateForUnknown(),
				},
			},
			"additional_warehouses": schema.ListAttribute{
				Description: "Existing Snowflake warehouses integrations may run on besides the ETL " +
					"credentials' default warehouse. Omit it to leave the list unmanaged; set [] to clear it.",
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.List{
					listplanmodifier.UseStateForUnknown(),
				},
			},
			"connection_type": schema.StringAttribute{
				Description: "Connection type reported by Matia; multi_purpose for assets this resource creates.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"default_database": schema.StringAttribute{
				Description: "Database of the ETL credentials, as reported by Matia.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"default_warehouse": schema.StringAttribute{
				Description: "Warehouse of the ETL credentials, as reported by Matia.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

// Diagnostics about a credentials block are never anchored on the block: Terraform
// prints the anchored configuration lines, secrets included. Credentials changes
// are replacements; see replaceUnlessAdoptingImportedCredentials.
func (r *multiPurposeAssetResource) ValidateConfig(
	ctx context.Context,
	req resource.ValidateConfigRequest,
	resp *resource.ValidateConfigResponse,
) {
	var config multiPurposeAssetModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if config.Credentials.IsNull() {
		var missing []string
		for _, purpose := range credentialsPurposes {
			if purpose.required && config.purposeBlocks()[purpose.attribute].IsNull() {
				missing = append(missing, purpose.attribute)
			}
		}
		if len(missing) > 0 {
			resp.Diagnostics.AddError(
				"Missing Snowflake credentials",
				"Set credentials for every purpose to share, or set all of etl, reverse_etl "+
					"and catalog. Missing: "+strings.Join(missing, ", ")+".",
			)
		}
	}

	// The API validates shared credentials as one flat connection, which needs a
	// database. It also serves ETL, whose destination resolver reads that field
	// and not the list, so rows cannot stand in for it here.
	databaseRules := map[string]databaseRule{"credentials": {required: true}}
	for _, purpose := range credentialsPurposes {
		databaseRules[purpose.attribute] = purpose.database
	}
	for attribute, block := range config.credentialBlocks() {
		if !blockIsSet(block) {
			continue
		}
		issue, diags := snowflakeCredentialsIssue(ctx, block, databaseRules[attribute])
		resp.Diagnostics.Append(diags...)
		if !diags.HasError() && issue != "" {
			resp.Diagnostics.AddError("Incomplete Snowflake credentials", "On "+attribute+": "+issue+".")
		}
	}
}

func (r *multiPurposeAssetResource) Create(
	ctx context.Context,
	req resource.CreateRequest,
	resp *resource.CreateResponse,
) {
	var plan multiPurposeAssetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	createReq, diags := buildCreateMultiPurposeAssetRequest(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	asset, err := r.client.Assets.Create(ctx, createReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create asset", err.Error())
		return
	}

	setMultiPurposeAssetState(ctx, asset, plan, preservePlanValue, &resp.State, &resp.Diagnostics)
}

func (r *multiPurposeAssetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state multiPurposeAssetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	asset, err := r.client.Assets.Get(ctx, state.ID.ValueString())
	if errors.Is(err, client.ErrAssetNotFound) {
		resp.Diagnostics.AddWarning(
			"Asset Removed From State",
			fmt.Sprintf(
				"Matia returned 404 for asset %q. The resource was removed from Terraform state. "+
					"If the asset still exists, verify GET /v1/assets/:id is available on your api_url.",
				state.ID.ValueString(),
			),
		)
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read asset", err.Error())
		return
	}

	// Import is the only way a foreign asset reaches this resource.
	if asset.ConnectionType != "" && asset.ConnectionType != multiPurposeConnectionType {
		resp.Diagnostics.AddError(
			"Not a multipurpose asset",
			fmt.Sprintf(
				"Asset %q has connection type %q. matia_asset manages multi_purpose assets only; "+
					"use matia_source or matia_destination for this one.",
				asset.AssetID(), asset.ConnectionType,
			),
		)
		return
	}

	setMultiPurposeAssetState(ctx, asset, state, refreshFromAPI, &resp.State, &resp.Diagnostics)
}

func (r *multiPurposeAssetResource) Update(
	ctx context.Context,
	req resource.UpdateRequest,
	resp *resource.UpdateResponse,
) {
	var plan, state multiPurposeAssetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	warnAdoptedCredentials(plan, state, &resp.Diagnostics)

	updateReq, changed, diags := buildMultiPurposeAssetUpdateRequest(ctx, plan, state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	if changed {
		if err := r.client.Assets.Update(ctx, state.ID.ValueString(), updateReq); err != nil {
			resp.Diagnostics.AddError("Failed to update asset", err.Error())
			return
		}
	}

	asset, err := r.client.Assets.Get(ctx, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to read asset after update", err.Error())
		return
	}

	setMultiPurposeAssetState(ctx, asset, plan, preservePlanValue, &resp.State, &resp.Diagnostics)
}

// An imported asset has no credentials in state because the API never returns
// them, so the first apply after import records the configured blocks as-is.
func warnAdoptedCredentials(plan, state multiPurposeAssetModel, diags *diag.Diagnostics) {
	if !state.hasNoCredentials() {
		return
	}
	var adopted []string
	for attribute, block := range plan.credentialBlocks() {
		if blockIsSet(block) {
			adopted = append(adopted, attribute)
		}
	}
	if len(adopted) == 0 {
		return
	}
	sort.Strings(adopted)
	diags.AddWarning(
		"Credentials recorded without verification",
		"The Matia API does not return credentials, so the configured "+strings.Join(adopted, ", ")+
			" block(s) of this imported asset were written to state as-is. They were not compared "+
			"with the credentials stored in Matia.",
	)
}

func (r *multiPurposeAssetResource) Delete(
	ctx context.Context,
	req resource.DeleteRequest,
	resp *resource.DeleteResponse,
) {
	var state multiPurposeAssetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.Assets.Delete(ctx, state.ID.ValueString())
	if err != nil && !errors.Is(err, client.ErrAssetNotFound) {
		resp.Diagnostics.AddError("Failed to delete asset", err.Error())
	}
}

func (r *multiPurposeAssetResource) ImportState(
	ctx context.Context,
	req resource.ImportStateRequest,
	resp *resource.ImportStateResponse,
) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func setMultiPurposeAssetState(
	ctx context.Context,
	asset *client.Asset,
	template multiPurposeAssetModel,
	policy readPolicy,
	state *tfsdk.State,
	diags *diag.Diagnostics,
) {
	model, modelDiags := multiPurposeAssetToModel(ctx, asset, template, policy)
	diags.Append(modelDiags...)
	if diags.HasError() {
		return
	}
	diags.Append(state.Set(ctx, model)...)
}
