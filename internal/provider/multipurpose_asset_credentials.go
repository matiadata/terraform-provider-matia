package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
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
	DatabaseSchemas      types.List   `tfsdk:"database_schemas"`
	Password             types.String `tfsdk:"password"`
	PrivateKey           types.String `tfsdk:"private_key"`
	PrivateKeyPassphrase types.String `tfsdk:"private_key_passphrase"`
	PublicKey            types.String `tfsdk:"public_key"`
}

type databaseSchemaModel struct {
	Database types.String `tfsdk:"database"`
	Schema   types.String `tfsdk:"schema"`
}

// databaseRule says whether a block must name a database, and whether listing
// database_schemas can stand in for it. Only reverse ETL reads the list where a
// database would otherwise be read: the ETL destination resolver and the ETL
// source both take the single `database` and never look at the list, so for them
// a list alone would pass validation and then fail downstream.
type databaseRule struct {
	required       bool
	rowsMayReplace bool
}

// The order matters for diagnostics only; the API accepts the purposes in any order.
var credentialsPurposes = []struct {
	attribute string
	apiKey    string
	required  bool
	database  databaseRule
}{
	{attribute: "etl", apiKey: "etl", required: true, database: databaseRule{required: true}},
	{
		attribute: "reverse_etl",
		apiKey:    "reverseEtl",
		required:  true,
		database:  databaseRule{required: true, rowsMayReplace: true},
	},
	{attribute: "catalog", apiKey: "catalog", required: true},
	{attribute: "etl_source", apiKey: "etlSource", database: databaseRule{required: true}},
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
				Description: "Default database. Required everywhere except on catalog, and on reverse_etl " +
					"when database_schemas is set instead.",
				Optional: true,
			},
			"database_schemas": schema.ListNestedAttribute{
				Description: "Databases this purpose works in, each paired with a schema. Matia stores " +
					"reverse-ETL and catalog databases this way, and its Manage tab reads them from here. " +
					"On reverse_etl it replaces database. Elsewhere it is recorded alongside database, " +
					"which etl and etl_source still need: the destination resolver and the ETL source read " +
					"that single field and never this list.",
				Optional: true,
				Validators: []validator.List{
					listvalidator.SizeAtLeast(1),
				},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"database": schema.StringAttribute{
							Description: "Name of an existing database.",
							Required:    true,
							Validators: []validator.String{
								stringvalidator.LengthAtLeast(1),
							},
						},
						"schema": schema.StringAttribute{
							Description: "Schema within the database. Matia requires one on every row.",
							Required:    true,
							Validators: []validator.String{
								stringvalidator.LengthAtLeast(1),
							},
						},
					},
				},
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
			"public_key": schema.StringAttribute{
				Description: "Base64 body of the public key, without the BEGIN and END lines, as Snowflake's " +
					"RSA_PUBLIC_KEY expects. Matia never authenticates with it: the dashboard shows it in the " +
					"edit wizard and puts it in the Snowflake setup script. Adding it later replaces the asset.",
				Optional: true,
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
		"public_key":             model.PublicKey,
	}

	connection := map[string]any{}
	for key, value := range fields {
		if value.IsNull() || value.IsUnknown() || value.ValueString() == "" {
			continue
		}
		connection[key] = value.ValueString()
	}

	rows, rowDiags := databaseSchemaRows(ctx, model.DatabaseSchemas)
	diags.Append(rowDiags...)
	if diags.HasError() {
		return nil, diags
	}
	if len(rows) > 0 {
		schemas := make([]map[string]string, 0, len(rows))
		for _, row := range rows {
			schemas = append(schemas, map[string]string{
				"database": row.Database.ValueString(),
				"schema":   row.Schema.ValueString(),
			})
		}
		connection["databaseSchemas"] = schemas
	}

	if method := credentialsAuthMethod(model); method != "" {
		connection["authMethod"] = method
	}
	return connection, diags
}

func credentialsAuthMethod(model snowflakeCredentialsModel) string {
	switch {
	case stringPresent(model.Password):
		return "password"
	case !stringPresent(model.PrivateKey):
		return ""
	case stringPresent(model.PrivateKeyPassphrase):
		return "keypair_encrypted"
	default:
		return "keypair"
	}
}

func databaseSchemaRows(ctx context.Context, list types.List) ([]databaseSchemaModel, diag.Diagnostics) {
	var diags diag.Diagnostics
	if list.IsNull() || list.IsUnknown() {
		return nil, diags
	}
	var rows []databaseSchemaModel
	diags.Append(list.ElementsAs(ctx, &rows, false)...)
	return rows, diags
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
	database databaseRule,
) (string, diag.Diagnostics) {
	var model snowflakeCredentialsModel
	diags := block.As(ctx, &model, basetypes.ObjectAsOptions{})
	if diags.HasError() {
		return "", diags
	}
	if !stringPresent(model.Password) && !stringPresent(model.PrivateKey) {
		return "set password or private_key", diags
	}
	if !database.required || stringPresent(model.Database) {
		return "", diags
	}
	if database.rowsMayReplace && listPresent(model.DatabaseSchemas) {
		return "", diags
	}
	if database.rowsMayReplace {
		return "set database or database_schemas", diags
	}
	return "set database", diags
}

// A list that resolves at apply counts as present, like an unknown string.
func listPresent(value types.List) bool {
	return value.IsUnknown() || !value.IsNull()
}

func stringPresent(value types.String) bool {
	return value.IsUnknown() || (!value.IsNull() && value.ValueString() != "")
}
