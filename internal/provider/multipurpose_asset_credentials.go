package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

// snowflakeCredentialsModel is one credentials block of a multi-purpose
// Snowflake asset, whether shared (`credentials`) or per purpose (`etl`, ...).
type snowflakeCredentialsModel struct {
	Account              types.String `tfsdk:"account"`
	Username             types.String `tfsdk:"username"`
	Warehouse            types.String `tfsdk:"warehouse"`
	Database             types.String `tfsdk:"database"`
	Password             types.String `tfsdk:"password"`
	PrivateKey           types.String `tfsdk:"private_key"`
	PrivateKeyPassphrase types.String `tfsdk:"private_key_passphrase"`
}

// The order matters for diagnostics only; the API accepts the purposes in any order.
var credentialsPurposes = []struct {
	attribute        string
	apiKey           string
	required         bool
	requiresDatabase bool
}{
	{attribute: "etl", apiKey: "etl", required: true, requiresDatabase: true},
	{attribute: "reverse_etl", apiKey: "reverseEtl", required: true, requiresDatabase: true},
	{attribute: "catalog", apiKey: "catalog", required: true},
	{attribute: "etl_source", apiKey: "etlSource", requiresDatabase: true},
}

const credentialsReplacementNote = " Changing or removing this block after creation forces resource replacement."

func snowflakeCredentialsSchema(description string) schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		Description: description + credentialsReplacementNote,
		Optional:    true,
		PlanModifiers: []planmodifier.Object{
			objectplanmodifier.RequiresReplaceIf(
				replaceUnlessAdoptingImportedCredentials,
				"Changing or removing credentials forces replacement.",
				"Changing or removing credentials forces replacement.",
			),
		},
		Attributes: map[string]schema.Attribute{
			"account": schema.StringAttribute{
				Description: "Snowflake account identifier (e.g. myorg-myaccount).",
				Required:    true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"username": schema.StringAttribute{
				Description: "Snowflake user for this purpose.",
				Required:    true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"warehouse": schema.StringAttribute{
				Description: "Warehouse the user runs on.",
				Required:    true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"database": schema.StringAttribute{
				Description: "Default database. Required everywhere except on catalog.",
				Optional:    true,
			},
			"password": schema.StringAttribute{
				Description: "Password for password authentication. Set this or private_key.",
				Optional:    true,
				Sensitive:   true,
			},
			"private_key": schema.StringAttribute{
				Description: "PKCS#8 PEM private key for key-pair authentication, newlines included. Set this or password.",
				Optional:    true,
				Sensitive:   true,
			},
			"private_key_passphrase": schema.StringAttribute{
				Description: "Passphrase of an encrypted private_key.",
				Optional:    true,
				Sensitive:   true,
			},
		},
	}
}

// The Matia API cannot rewrite the credentials of a multipurpose asset: a nested
// PATCH is rejected and a flat one is silently dropped. Every credentials change
// is therefore a replacement, except on an imported asset, whose state holds no
// credentials at all until the first apply records the configured blocks.
func replaceUnlessAdoptingImportedCredentials(
	ctx context.Context,
	req planmodifier.ObjectRequest,
	resp *objectplanmodifier.RequiresReplaceIfFuncResponse,
) {
	if !req.StateValue.IsNull() {
		resp.RequiresReplace = true
		return
	}
	var state multiPurposeAssetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.RequiresReplace = !state.hasNoCredentials()
}

func snowflakeCredentialsToAPI(ctx context.Context, block types.Object) (map[string]any, diag.Diagnostics) {
	var model snowflakeCredentialsModel
	diags := block.As(ctx, &model, basetypes.ObjectAsOptions{})
	if diags.HasError() {
		return nil, diags
	}

	fields := map[string]types.String{
		"account":                model.Account,
		"username":               model.Username,
		"warehouse":              model.Warehouse,
		"database":               model.Database,
		"password":               model.Password,
		"private_key":            model.PrivateKey,
		"private_key_passphrase": model.PrivateKeyPassphrase,
	}

	connection := map[string]any{}
	for key, value := range fields {
		if value.IsNull() || value.IsUnknown() || value.ValueString() == "" {
			continue
		}
		connection[key] = value.ValueString()
	}
	return connection, diags
}

func blockIsSet(block types.Object) bool {
	return !block.IsNull() && !block.IsUnknown()
}

// A known block must carry a password or a private key, and for the purposes
// that load or read data, a database. Unknown values count as present: they
// resolve at apply.
func snowflakeCredentialsIssue(
	ctx context.Context,
	block types.Object,
	requiresDatabase bool,
) (string, diag.Diagnostics) {
	var model snowflakeCredentialsModel
	diags := block.As(ctx, &model, basetypes.ObjectAsOptions{})
	if diags.HasError() {
		return "", diags
	}
	if !stringPresent(model.Password) && !stringPresent(model.PrivateKey) {
		return "set password or private_key", diags
	}
	if requiresDatabase && !stringPresent(model.Database) {
		return "set database", diags
	}
	return "", diags
}

func stringPresent(value types.String) bool {
	return value.IsUnknown() || (!value.IsNull() && value.ValueString() != "")
}
