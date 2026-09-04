package provider

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/stretchr/testify/require"
)

// catalogGETSyncModeForms records, as literals captured from the API, the exact string GET returns
// for every ESyncMode PATCH accepts. Both mirrors of the backend's formatCatalogSyncMode - the
// provider's own lookup and this package's fake server - are checked against these literals, so a
// change to that transform fails the tests instead of being reproduced by them.
var catalogGETSyncModeForms = map[string]string{
	"Full Refresh":                     "FULL_REFRESH",
	"Incremental":                      "INCREMENTAL",
	"Incremental (Append-Only)":        "INCREMENTAL_(APPEND-ONLY)",
	"Change Stream":                    "CHANGE_STREAM",
	"Change Tracking":                  "CHANGE_TRACKING",
	"Change Stream (Append-Only)":      "CHANGE_STREAM (APPEND-ONLY)",
	"Change Stream (Initial Snapshot)": "CHANGE_STREAM (INITIAL SNAPSHOT)",
	"Change Stream (No Snapshot)":      "CHANGE_STREAM (NO SNAPSHOT)",
}

// catalogGETForm mirrors the backend's formatCatalogSyncMode
// (integration-schema-config.service.ts): syncMode.toUpperCase().replace(' ', '_').
// JavaScript's String.replace with a string pattern substitutes only the FIRST match, so
// multi-word modes keep their remaining spaces.
func catalogGETForm(mode string) string {
	return strings.Replace(strings.ToUpper(mode), " ", "_", 1)
}

func TestResolveSyncModeForAPI_ResolvesEveryCatalogGETForm(t *testing.T) {
	t.Parallel()

	for apiValue, getForm := range catalogGETSyncModeForms {
		t.Run(apiValue, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, apiValue, resolveSyncModeForAPI(getForm))
		})
	}
}

// The fake acceptance server derives its catalog syncMode from catalogGETForm, so pin that mirror
// against the recorded forms too - otherwise a wrong mirror would be consistent with itself and the
// acceptance suite would pass on a shape the API never returns.
func TestCatalogGETForm_MatchesRecordedAPIForms(t *testing.T) {
	t.Parallel()

	for apiValue, getForm := range catalogGETSyncModeForms {
		require.Equal(t, getForm, catalogGETForm(apiValue))
	}
}

func TestResolveSyncModeForAPI_RejectsNearMissSpellings(t *testing.T) {
	t.Parallel()

	// A value that merely resembles a mode must pass through untouched so the API rejects it,
	// rather than being folded onto a real mode and applied as something the user never asked for.
	for _, mode := range []string{"cdc!!!", "--cdc--", "full!!refresh", "fullrefresh", "incremental(append)only", "unknown_mode"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, mode, resolveSyncModeForAPI(mode))
		})
	}
}

func TestProjectSchemaConfig_PreservesLargeIntegers(t *testing.T) {
	t.Parallel()

	// Decoding into any turns numbers into float64 and would rewrite this as 12345678901234567000.
	declared := `{"schemas":{"public":{"tables":{"customers":{"enabled":true}}}},"budget":12345678901234567890}`
	catalog := `{"schemas":{"public":{"tables":{"customers":{"enabled":true}}}}}`

	projected, diags := projectSchemaConfig(declared, catalog)

	require.False(t, diags.HasError(), "%v", diags)
	require.Contains(t, projected, "12345678901234567890")
}

func TestProjectSchemaConfig_SurfacesServerDrift(t *testing.T) {
	t.Parallel()

	declared := `{"schemas":{"public":{"tables":{"customers":{"enabled":true,"syncMode":"cdc"}}}}}`
	catalog := `{"schemas":{"public":{"tables":{"customers":{"enabled":false,"syncMode":"FULL_REFRESH"}}}}}`

	projected, diags := projectSchemaConfig(declared, catalog)

	require.False(t, diags.HasError(), "%v", diags)
	require.JSONEq(
		t,
		`{"schemas":{"public":{"tables":{"customers":{"enabled":false,"syncMode":"Full Refresh"}}}}}`,
		projected,
	)
}

func TestProjectSchemaConfig_KeepsUserAliasWhenServerAgrees(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		alias   string
		catalog string
	}{
		{"cdc", "CHANGE_STREAM"},
		{"change_stream", "CHANGE_STREAM"},
		{"Change Stream", "CHANGE_STREAM"},
		{"change_stream_initial_snapshot", "CHANGE_STREAM (INITIAL SNAPSHOT)"},
		{"incremental_append_only", "INCREMENTAL_(APPEND-ONLY)"},
	} {
		t.Run(tc.alias, func(t *testing.T) {
			t.Parallel()

			declared := `{"schemas":{"public":{"tables":{"customers":{"syncMode":"` + tc.alias + `"}}}}}`
			catalog := `{"schemas":{"public":{"tables":{"customers":{"syncMode":"` + tc.catalog + `"}}}}}`

			projected, diags := projectSchemaConfig(declared, catalog)

			require.False(t, diags.HasError(), "%v", diags)
			require.JSONEq(t, declared, projected)
		})
	}
}

func TestProjectSchemaConfig_IgnoresUndeclaredCatalogEntries(t *testing.T) {
	t.Parallel()

	declared := `{"schemas":{"public":{"tables":{"customers":{"enabled":true}}}}}`
	catalog := `{"schemas":{"public":{"nameInDestination":"public","tables":{` +
		`"customers":{"enabled":true,"nameInDestination":"customers","syncMode":"CHANGE_STREAM",` +
		`"enabledPatchSettings":{"allowed":true},"columns":{"id":{"enabled":true,"hashed":false,"isPrimaryKey":true}}},` +
		`"audit_log":{"enabled":true,"syncMode":"FULL_REFRESH"}}},` +
		`"private":{"tables":{"secrets":{"enabled":true}}}}}`

	projected, diags := projectSchemaConfig(declared, catalog)

	require.False(t, diags.HasError(), "%v", diags)
	require.JSONEq(t, declared, projected)
}

// A declared table or column the catalog does not list must survive projection: PATCH answers 200
// and silently skips both, so dropping it here would put a diff in every plan that no apply can
// ever clear.
func TestProjectSchemaConfig_KeepsDeclaredTableMissingFromCatalog(t *testing.T) {
	t.Parallel()

	declared := `{"schemas":{"public":{"tables":{"customers":{"enabled":true},"gone":{"enabled":true}}}}}`
	catalog := `{"schemas":{"public":{"tables":{"customers":{"enabled":true}}}}}`

	projected, diags := projectSchemaConfig(declared, catalog)

	require.False(t, diags.HasError(), "%v", diags)
	require.JSONEq(t, declared, projected)
}

// A missing schema is the one level PATCH does not skip: ensureSchemaExists rejects the whole
// request with SCHEMA_NOT_FOUND. Keeping it is still the right Read behaviour - dropping it would
// produce a diff whose apply errors outright rather than one that merely never clears.
func TestProjectSchemaConfig_KeepsDeclaredSchemaMissingFromCatalog(t *testing.T) {
	t.Parallel()

	declared := `{"schemas":{"public":{"tables":{"customers":{"enabled":true}}},"gone":{"tables":{"x":{"enabled":true}}}}}`
	catalog := `{"schemas":{"public":{"tables":{"customers":{"enabled":true}}}}}`

	projected, diags := projectSchemaConfig(declared, catalog)

	require.False(t, diags.HasError(), "%v", diags)
	require.JSONEq(t, declared, projected)
}

// Keeping the schema is right, but silent: refreshes stay clean forever while the next PATCH -
// triggered by an edit anywhere else in the config - fails as a whole with SCHEMA_NOT_FOUND. Read
// is the only place that sees it coming, so it warns.
func TestProjectSchemaConfig_WarnsWhenDeclaredSchemaMissingFromCatalog(t *testing.T) {
	t.Parallel()

	declared := `{"schemas":{"public":{"tables":{"customers":{"enabled":true}}},"gone":{"tables":{"x":{"enabled":true}}}}}`
	catalog := `{"schemas":{"public":{"tables":{"customers":{"enabled":true}}}}}`

	_, diags := projectSchemaConfig(declared, catalog)

	require.False(t, diags.HasError(), "%v", diags)
	warnings := diags.Warnings()
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0].Detail(), `"gone"`)
	require.NotContains(t, warnings[0].Detail(), `"public"`)
	// An independent literal, not client.SchemaNotFoundMessage: the warning exists so the user
	// recognises the apply error when it arrives, so rewording that error must break this test
	// rather than silently reword the warning with it.
	require.Contains(t, warnings[0].Detail(), "schema not found in integration catalog")

	// Attribute-scoped so Terraform prints the config line the schema is declared on.
	withPath, isAttributeScoped := warnings[0].(diag.DiagnosticWithPath)
	require.True(t, isAttributeScoped, "expected an attribute diagnostic, got %T", warnings[0])
	require.Equal(t, path.Root("config"), withPath.Path())
}

// ensureSchemaExists early-returns for the backend's DEFAULT_SCHEMA_NAME, so a config declaring
// "schema" never hits SCHEMA_NOT_FOUND however the catalog names its streams. Warning about it
// would send the user to delete config that works.
func TestProjectSchemaConfig_DoesNotWarnForDefaultSchemaName(t *testing.T) {
	t.Parallel()

	declared := `{"schemas":{"schema":{"tables":{"customers":{"enabled":true}}}}}`
	catalog := `{"schemas":{"public":{"tables":{"orders":{"enabled":true}}}}}`

	projected, diags := projectSchemaConfig(declared, catalog)

	require.False(t, diags.HasError(), "%v", diags)
	require.Empty(t, diags.Warnings())
	require.JSONEq(t, declared, projected)
}

// "{}" is the client's fallback for a response carrying no data - an absent comparison, so there
// is nothing to warn about.
func TestProjectSchemaConfig_DoesNotWarnWhenCatalogHasNoSchemas(t *testing.T) {
	t.Parallel()

	_, diags := projectSchemaConfig(`{"schemas":{"public":{"tables":{"customers":{"enabled":true}}}}}`, `{}`)

	require.False(t, diags.HasError(), "%v", diags)
	require.Empty(t, diags.Warnings())
}

// An EMPTY schemas object is a real answer, not a missing one: ensureSchemaExists tests the same
// stream catalog GET renders, so a PATCH against it rejects every declared schema. Suppressing
// here - the tempting symmetry with the "{}" case above - would hide a guaranteed apply failure.
func TestProjectSchemaConfig_WarnsWhenCatalogListsNoSchemas(t *testing.T) {
	t.Parallel()

	_, diags := projectSchemaConfig(
		`{"schemas":{"public":{"tables":{"customers":{"enabled":true}}}}}`,
		`{"schemas":{}}`,
	)

	require.False(t, diags.HasError(), "%v", diags)
	require.Len(t, diags.Warnings(), 1)
	require.Contains(t, diags.Warnings()[0].Detail(), `"public"`)
}

func TestProjectSchemaConfig_DoesNotWarnWhenEveryDeclaredSchemaIsListed(t *testing.T) {
	t.Parallel()

	declared := `{"schemas":{"public":{"tables":{"customers":{"enabled":true}}}}}`
	catalog := `{"schemas":{"public":{"tables":{"customers":{"enabled":false}}},"private":{"tables":{}}}}`

	_, diags := projectSchemaConfig(declared, catalog)

	require.False(t, diags.HasError(), "%v", diags)
	require.Empty(t, diags.Warnings())
}

// Every missing schema must be named in ONE diagnostic: Terraform prints only the first of several
// warnings sharing a summary, so a per-schema diagnostic would hide the rest behind "(and one more
// similar warning elsewhere)".
func TestProjectSchemaConfig_NamesEveryMissingSchemaInOneStableWarning(t *testing.T) {
	t.Parallel()

	declared := `{"schemas":{"zeta":{"tables":{}},"gamma":{"tables":{}},"beta":{"tables":{}},` +
		`"alpha":{"tables":{}},"public":{"tables":{}}}}`
	catalog := `{"schemas":{"public":{"tables":{}}}}`

	// Go randomises map iteration per range, so one pass could produce sorted order by chance;
	// repeating drives that to nothing. Unstable order would reword the warning between two
	// otherwise identical plans.
	for range 20 {
		_, diags := projectSchemaConfig(declared, catalog)

		require.False(t, diags.HasError(), "%v", diags)
		require.Len(t, diags.Warnings(), 1)
		require.Contains(t, diags.Warnings()[0].Detail(), `"alpha", "beta", "gamma", "zeta"`)
		require.NotContains(t, diags.Warnings()[0].Detail(), `"public"`)
	}
}

func TestProjectSchemaConfig_KeepsDeclaredColumnMissingFromCatalog(t *testing.T) {
	t.Parallel()

	declared := `{"schemas":{"public":{"tables":{"customers":{"columns":{"email":{"hashed":true},"dropped":{"hashed":true}}}}}}}`
	catalog := `{"schemas":{"public":{"tables":{"customers":{"columns":{"email":{"hashed":true}}}}}}}`

	projected, diags := projectSchemaConfig(declared, catalog)

	require.False(t, diags.HasError(), "%v", diags)
	require.JSONEq(t, declared, projected)
}

// Flattened subtables are listed in the catalog but PATCH resolves tables only through the parent
// stream, so their server values must not be projected as drift no apply can repair.
func TestProjectSchemaConfig_LeavesUnpatchableTableDeclared(t *testing.T) {
	t.Parallel()

	declared := `{"schemas":{"public":{"tables":{"order_items":{"enabled":false}}}}}`
	catalog := `{"schemas":{"public":{"tables":{"order_items":{"enabled":true,` +
		`"enabledPatchSettings":{"allowed":false}}}}}}`

	projected, diags := projectSchemaConfig(declared, catalog)

	require.False(t, diags.HasError(), "%v", diags)
	require.JSONEq(t, declared, projected)
}

// The catalog-update path skips sync and hashed writes for primary-key columns outright, so their
// server values must not be projected: the diff would reappear after every apply.
func TestProjectSchemaConfig_LeavesPrimaryKeyColumnDeclared(t *testing.T) {
	t.Parallel()

	declared := `{"schemas":{"public":{"tables":{"customers":{"columns":{"id":{"hashed":true,"enabled":false}}}}}}}`
	catalog := `{"schemas":{"public":{"tables":{"customers":{"columns":{` +
		`"id":{"enabled":true,"hashed":false,"isPrimaryKey":true}}}}}}}`

	projected, diags := projectSchemaConfig(declared, catalog)

	require.False(t, diags.HasError(), "%v", diags)
	require.JSONEq(t, declared, projected)
}

func TestProjectSchemaConfig_KeepsSchemaDeclaredWithoutTables(t *testing.T) {
	t.Parallel()

	// The PATCH DTO accepts a schema-level enabled with no tables object; projection must not
	// invent an empty one, which would never equal the user's config.
	declared := `{"schemas":{"public":{"enabled":false}}}`
	catalog := `{"schemas":{"public":{"nameInDestination":"public","tables":{"customers":{"enabled":true}}}}}`

	projected, diags := projectSchemaConfig(declared, catalog)

	require.False(t, diags.HasError(), "%v", diags)
	require.JSONEq(t, declared, projected)
}

func TestProjectSchemaConfig_KeepsConfigWhenCatalogHasNoSchemas(t *testing.T) {
	t.Parallel()

	declared := `{"schemas":{"public":{"tables":{"customers":{"enabled":true}}}}}`

	projected, diags := projectSchemaConfig(declared, `{}`)

	require.False(t, diags.HasError(), "%v", diags)
	require.JSONEq(t, declared, projected)
}

func TestProjectSchemaConfig_ProjectsDeclaredColumns(t *testing.T) {
	t.Parallel()

	declared := `{"schemas":{"public":{"tables":{"customers":{"columns":{"email":{"hashed":false}}}}}}}`
	catalog := `{"schemas":{"public":{"tables":{"customers":{"columns":{` +
		`"email":{"enabled":true,"hashed":true,"isPrimaryKey":false},` +
		`"id":{"enabled":true,"hashed":false,"isPrimaryKey":true}}}}}}}`

	projected, diags := projectSchemaConfig(declared, catalog)

	require.False(t, diags.HasError(), "%v", diags)
	require.JSONEq(
		t,
		`{"schemas":{"public":{"tables":{"customers":{"columns":{"email":{"hashed":true}}}}}}}`,
		projected,
	)
}

func TestProjectSchemaConfig_KeepsDeclaredCursorFieldMissingFromCatalog(t *testing.T) {
	t.Parallel()

	// GET omits cursorField unless the column is selectable, so its absence is not proof the
	// server dropped it; keeping the declared value avoids a perpetual phantom diff.
	declared := `{"schemas":{"public":{"tables":{"customers":{"cursorField":"updated_at"}}}}}`
	catalog := `{"schemas":{"public":{"tables":{"customers":{"enabled":true}}}}}`

	projected, diags := projectSchemaConfig(declared, catalog)

	require.False(t, diags.HasError(), "%v", diags)
	require.JSONEq(t, declared, projected)
}

func TestProjectSchemaConfig_SurfacesCursorFieldDrift(t *testing.T) {
	t.Parallel()

	declared := `{"schemas":{"public":{"tables":{"customers":{"cursorField":"updated_at"}}}}}`
	catalog := `{"schemas":{"public":{"tables":{"customers":{"cursorField":"created_at"}}}}}`

	projected, diags := projectSchemaConfig(declared, catalog)

	require.False(t, diags.HasError(), "%v", diags)
	require.JSONEq(t, `{"schemas":{"public":{"tables":{"customers":{"cursorField":"created_at"}}}}}`, projected)
}

func TestProjectSchemaConfig_KeepsUnmanagedDeclaredKeys(t *testing.T) {
	t.Parallel()

	// The schemas PATCH accepts a schema-level enabled but GET never returns one, so there is
	// nothing to compare it against - keep the user's value rather than invent drift.
	declared := `{"schemas":{"public":{"enabled":true,"tables":{"customers":{"enabled":true}}}}}`
	catalog := `{"schemas":{"public":{"nameInDestination":"public","tables":{"customers":{"enabled":true}}}}}`

	projected, diags := projectSchemaConfig(declared, catalog)

	require.False(t, diags.HasError(), "%v", diags)
	require.JSONEq(t, declared, projected)
}

func TestProjectSchemaConfig_RejectsInvalidCatalog(t *testing.T) {
	t.Parallel()

	_, diags := projectSchemaConfig(`{"schemas":{}}`, `not json`)

	require.True(t, diags.HasError())
}

// A json.Decoder stops at the first complete value, so without an explicit check it would accept a
// catalog body json.Unmarshal rejected.
func TestProjectSchemaConfig_RejectsCatalogWithTrailingContent(t *testing.T) {
	t.Parallel()

	_, diags := projectSchemaConfig(`{"schemas":{}}`, `{"schemas":{}} {"schemas":{}}`)

	require.True(t, diags.HasError())
}

// Read projects on projectTable's table-level field set and Update's no-op comparison reduces on
// canonicalizeSchemaConfig's. The two are written out by hand, so pin them against one list here: a
// field wired into only one of them is either drift nothing can detect or a real edit that never
// reaches the API. Columns already share patchManagedColumnFields, so this covers the scalars.
func TestProjectAndCanonicalize_ShareTableFieldSet(t *testing.T) {
	t.Parallel()

	patchManagedTableFields := []string{"enabled", "syncMode", "cursorField"}

	// nameInDestination and enabledPatchSettings are real catalog fields PATCH does not accept -
	// the plausible mistakes - so either function acting on one shows up as an extra key below.
	declared := `{"schemas":{"public":{"tables":{"customers":{` +
		`"enabled":true,"syncMode":"cdc","cursorField":"updated_at",` +
		`"nameInDestination":"customers","enabledPatchSettings":{"allowed":true}}}}}}`
	// Disagrees on every field, so whatever projection rewrites is exactly what it manages.
	catalog := `{"schemas":{"public":{"tables":{"customers":{` +
		`"enabled":false,"syncMode":"FULL_REFRESH","cursorField":"created_at",` +
		`"nameInDestination":"customers_v2","enabledPatchSettings":{"allowed":true}}}}}}`

	projected, diags := projectSchemaConfig(declared, catalog)
	require.False(t, diags.HasError(), "%v", diags)

	canonical, canonicalDiags := canonicalizeSchemaConfigForComparison(declared)
	require.False(t, canonicalDiags.HasError(), "%v", canonicalDiags)

	require.ElementsMatch(t, patchManagedTableFields, rewrittenTableFields(t, declared, projected),
		"projectTable rewrote a different field set than PATCH manages")
	require.ElementsMatch(t, patchManagedTableFields, tableFieldNames(t, canonical),
		"canonicalizeSchemaConfig reduced to a different field set than PATCH manages")
}

func rewrittenTableFields(t *testing.T, before, after string) []string {
	t.Helper()

	beforeTable := customersTable(t, before)
	afterTable := customersTable(t, after)

	rewritten := []string{}
	for field, beforeValue := range beforeTable {
		if !reflect.DeepEqual(beforeValue, afterTable[field]) {
			rewritten = append(rewritten, field)
		}
	}
	return rewritten
}

func tableFieldNames(t *testing.T, config string) []string {
	t.Helper()

	fields := []string{}
	for field := range customersTable(t, config) {
		fields = append(fields, field)
	}
	return fields
}

func customersTable(t *testing.T, config string) map[string]any {
	t.Helper()

	var payload struct {
		Schemas map[string]struct {
			Tables map[string]map[string]any `json:"tables"`
		} `json:"schemas"`
	}
	require.NoError(t, json.Unmarshal([]byte(config), &payload))

	return payload.Schemas["public"].Tables["customers"]
}

func TestIsPatchableTable(t *testing.T) {
	t.Parallel()

	require.True(t, isPatchableTable(map[string]any{"enabledPatchSettings": map[string]any{"allowed": true}}))
	require.False(t, isPatchableTable(map[string]any{"enabledPatchSettings": map[string]any{"allowed": false}}))
	require.True(t, isPatchableTable(map[string]any{}), "absent settings must not block projection")
}

func TestNormalizeSchemaConfigJSON(t *testing.T) {
	_, diags := prepareSchemaConfigForAPI(`{"schemas":{}}`)
	require.False(t, diags.HasError(), "expected valid config, got %v", diags)

	_, diags = prepareSchemaConfigForAPI(`{}`)
	require.True(t, diags.HasError(), "expected error for missing schemas key")
}

func TestCanonicalizeSchemaConfigForComparisonTreatsEquivalentAliasesEqual(t *testing.T) {
	cdc, diags := canonicalizeSchemaConfigForComparison(
		`{"schemas":{"public":{"tables":{"users":{"enabled":true,"syncMode":"cdc"}}}}}`,
	)
	require.False(t, diags.HasError(), "unexpected error: %v", diags)

	changeStream, csDiags := canonicalizeSchemaConfigForComparison(
		`{"schemas":{"public":{"tables":{"users":{"enabled":true,"syncMode":"change_stream"}}}}}`,
	)
	require.False(t, csDiags.HasError(), "unexpected error: %v", csDiags)

	// cdc and change_stream are the same Matia sync mode, so an Update between them is a no-op.
	require.Equal(t, changeStream, cdc)
	require.Contains(t, cdc, "Change Stream")
}

func TestCanonicalizeSchemaConfigForComparisonKeepsColumnChanges(t *testing.T) {
	base, diags := canonicalizeSchemaConfigForComparison(
		`{"schemas":{"public":{"tables":{"users":{"syncMode":"cdc","columns":{"ssn":{"hashed":false}}}}}}}`,
	)
	require.False(t, diags.HasError(), "unexpected error: %v", diags)

	hashed, hDiags := canonicalizeSchemaConfigForComparison(
		`{"schemas":{"public":{"tables":{"users":{"syncMode":"cdc","columns":{"ssn":{"hashed":true}}}}}}}`,
	)
	require.False(t, hDiags.HasError(), "unexpected error: %v", hDiags)

	// A column-only change (hashed false -> true) must not be mistaken for a no-op.
	require.NotEqual(t, base, hashed)
	require.Contains(t, hashed, `"hashed":true`)
}

func TestCanonicalizeSchemaConfigForComparisonDropsFieldsTheAPIIgnores(t *testing.T) {
	// Server-side fields a user may paste in from a GET response are not part of the PATCH
	// contract, so they must not make an otherwise-equal plan and state compare unequal.
	got, diags := canonicalizeSchemaConfigForComparison(`{
		"schemas": {
			"public": {
				"nameInDestination": "public",
				"tables": {
					"users": {
						"nameInDestination": "users",
						"enabled": true,
						"enabledPatchSettings": {"allowed": true},
						"syncMode": "CHANGE_STREAM",
						"columns": {
							"id": {
								"enabled": true,
								"hashed": false,
								"isPrimaryKey": true,
								"nameInDestination": "id"
							}
						}
					}
				}
			}
		}
	}`)
	require.False(t, diags.HasError(), "unexpected error: %v", diags)

	want := `{"schemas":{"public":{"tables":{"users":{"columns":{"id":{"enabled":true,"hashed":false}},` +
		`"enabled":true,"syncMode":"Change Stream"}}}}}`
	require.Equal(t, want, got)
}

func TestPrepareSchemaConfigForAPIResolvesSyncMode(t *testing.T) {
	got, diags := prepareSchemaConfigForAPI(`{"schemas":{"public":{"tables":{"users":{"syncMode":"incremental"}}}}}`)
	require.False(t, diags.HasError(), "unexpected error: %v", diags)

	want := `{"schemas":{"public":{"tables":{"users":{"syncMode":"Incremental"}}}}}`
	require.Equal(t, want, string(got))
}

func TestAddDisablesForDroppedTables_DisablesTableRemovedFromConfig(t *testing.T) {
	t.Parallel()

	prior := `{"schemas":{"public":{"tables":{"orders":{"enabled":true},"users":{"enabled":true}}}}}`
	planned := `{"schemas":{"public":{"tables":{"users":{"enabled":true}}}}}`
	catalog := `{"schemas":{"public":{"tables":{"orders":{"enabled":true},"users":{"enabled":true}}}}}`

	expanded, diags := addDisablesForDroppedTables(prior, planned, catalog)

	require.False(t, diags.HasError(), "%v", diags)
	require.JSONEq(
		t,
		`{"schemas":{"public":{"tables":{"orders":{"enabled":false},"users":{"enabled":true}}}}}`,
		expanded,
	)
	require.Empty(t, diags.Warnings())
}

// A table Terraform never declared is not Terraform's to disable. This is the ownership boundary
// the whole design rests on: without it, every apply would undo whatever onSchemaUpdate enabled.
func TestAddDisablesForDroppedTables_LeavesNeverDeclaredTableAlone(t *testing.T) {
	t.Parallel()

	prior := `{"schemas":{"public":{"tables":{"users":{"enabled":true}}}}}`
	planned := `{"schemas":{"public":{"tables":{"users":{"enabled":true}}}}}`
	catalog := `{"schemas":{"public":{"tables":{"audit_log":{"enabled":true},"users":{"enabled":true}}}}}`

	expanded, diags := addDisablesForDroppedTables(prior, planned, catalog)

	require.False(t, diags.HasError(), "%v", diags)
	require.JSONEq(t, planned, expanded)
	require.Empty(t, diags.Warnings())
}

// PATCH answers 200 and silently skips a table the catalog no longer lists, so naming it would put
// a permanent no-op in every request - and that table is already gone from the source, so there is
// nothing still syncing to warn about either.
func TestAddDisablesForDroppedTables_SkipsTableMissingFromCatalog(t *testing.T) {
	t.Parallel()

	prior := `{"schemas":{"public":{"tables":{"orders":{"enabled":true},"users":{"enabled":true}}}}}`
	planned := `{"schemas":{"public":{"tables":{"users":{"enabled":true}}}}}`
	catalog := `{"schemas":{"public":{"tables":{"users":{"enabled":true}}}}}`

	expanded, diags := addDisablesForDroppedTables(prior, planned, catalog)

	require.False(t, diags.HasError(), "%v", diags)
	require.JSONEq(t, planned, expanded)
	require.Empty(t, diags.Warnings())
}

// Flattened subtables carry enabledPatchSettings.allowed:false and cannot be written at all, so the
// drop is reported rather than sent.
func TestAddDisablesForDroppedTables_WarnsWithoutDisablingUnpatchableTable(t *testing.T) {
	t.Parallel()

	prior := `{"schemas":{"public":{"tables":{"orders_items":{"enabled":true},"users":{"enabled":true}}}}}`
	planned := `{"schemas":{"public":{"tables":{"users":{"enabled":true}}}}}`
	catalog := `{"schemas":{"public":{"tables":{"orders_items":{"enabled":true,"enabledPatchSettings":{"allowed":false}},"users":{"enabled":true}}}}}`

	expanded, diags := addDisablesForDroppedTables(prior, planned, catalog)

	require.False(t, diags.HasError(), "%v", diags)
	require.JSONEq(t, planned, expanded)
	warnings := diags.Warnings()
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0].Detail(), `"public"."orders_items"`)
}

// Naming a schema the catalog dropped fails the whole PATCH with SCHEMA_NOT_FOUND, which would
// break the exact cleanup warnSchemasMissingFromCatalog asks the user to perform.
func TestAddDisablesForDroppedTables_SkipsSchemaMissingFromCatalog(t *testing.T) {
	t.Parallel()

	prior := `{"schemas":{"gone":{"tables":{"orders":{"enabled":true}}},"public":{"tables":{"users":{"enabled":true}}}}}`
	planned := `{"schemas":{"public":{"tables":{"users":{"enabled":true}}}}}`
	catalog := `{"schemas":{"public":{"tables":{"users":{"enabled":true}}}}}`

	expanded, diags := addDisablesForDroppedTables(prior, planned, catalog)

	require.False(t, diags.HasError(), "%v", diags)
	require.JSONEq(t, planned, expanded)
	require.Empty(t, diags.Warnings())
}

// Dropping a whole schema that the catalog still lists disables the tables it used to declare.
func TestAddDisablesForDroppedTables_DisablesTablesOfSchemaDroppedEntirely(t *testing.T) {
	t.Parallel()

	prior := `{"schemas":{"analytics":{"tables":{"events":{"enabled":true}}},"public":{"tables":{"users":{"enabled":true}}}}}`
	planned := `{"schemas":{"public":{"tables":{"users":{"enabled":true}}}}}`
	catalog := `{"schemas":{"analytics":{"tables":{"events":{"enabled":true}}},"public":{"tables":{"users":{"enabled":true}}}}}`

	expanded, diags := addDisablesForDroppedTables(prior, planned, catalog)

	require.False(t, diags.HasError(), "%v", diags)
	require.JSONEq(
		t,
		`{"schemas":{"analytics":{"tables":{"events":{"enabled":false}}},"public":{"tables":{"users":{"enabled":true}}}}}`,
		expanded,
	)
	require.Empty(t, diags.Warnings())
}

// effective_schema is null in state written before it existed, so an apply that skips refresh can
// reach Update without a catalog. Disabling nothing is the safe read of "no evidence"; failing the
// apply over a state field the user never set is not. Dropping the table also erases it from the
// ownership record, so this apply spent the only chance it will get - hence the warning.
func TestAddDisablesForDroppedTables_WarnsWithoutDisablingWhenCatalogIsUnavailable(t *testing.T) {
	t.Parallel()

	prior := `{"schemas":{"public":{"tables":{"orders":{"enabled":true},"users":{"enabled":true}}}}}`
	planned := `{"schemas":{"public":{"tables":{"users":{"enabled":true}}}}}`

	expanded, diags := addDisablesForDroppedTables(prior, planned, "")

	require.False(t, diags.HasError(), "%v", diags)
	require.JSONEq(t, planned, expanded)
	warnings := diags.Warnings()
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0].Detail(), `"public"."orders"`)

	withPath, isAttributeScoped := warnings[0].(diag.DiagnosticWithPath)
	require.True(t, isAttributeScoped, "expected an attribute diagnostic, got %T", warnings[0])
	require.Equal(t, path.Root("config"), withPath.Path())
}

// A prior entry that never set `enabled` means Terraform never turned that table on, so removing
// it must not turn it off. syncMode-only declarations are this repo's own canonical shape
// (testAccIntegrationSchemaConfig renders exactly that).
func TestAddDisablesForDroppedTables_LeavesDroppedTableWhoseEnabledWasNeverDeclared(t *testing.T) {
	t.Parallel()

	prior := `{"schemas":{"public":{"tables":{"orders":{"syncMode":"Incremental"},"users":{"enabled":true}}}}}`
	planned := `{"schemas":{"public":{"tables":{"users":{"enabled":true}}}}}`
	catalog := `{"schemas":{"public":{"tables":{"orders":{"enabled":true},"users":{"enabled":true}}}}}`

	expanded, diags := addDisablesForDroppedTables(prior, planned, catalog)

	require.False(t, diags.HasError(), "%v", diags)
	require.JSONEq(t, planned, expanded)
	require.Empty(t, diags.Warnings())
}

// A table the prior config already declared disabled has nothing left to turn off, so dropping it
// is not a missed disable and must not warn that it "keeps syncing".
func TestAddDisablesForDroppedTables_SkipsDroppedTableAlreadyDisabledInConfig(t *testing.T) {
	t.Parallel()

	prior := `{"schemas":{"public":{"tables":{"orders":{"enabled":false},"users":{"enabled":true}}}}}`
	planned := `{"schemas":{"public":{"tables":{"users":{"enabled":true}}}}}`

	expanded, diags := addDisablesForDroppedTables(prior, planned, "")

	require.False(t, diags.HasError(), "%v", diags)
	require.JSONEq(t, planned, expanded)
	require.Empty(t, diags.Warnings())
}

// Same when the catalog is the one reporting it off: an unpatchable table that is already disabled
// upstream is not a table that keeps syncing.
func TestAddDisablesForDroppedTables_SkipsDroppedTableAlreadyDisabledUpstream(t *testing.T) {
	t.Parallel()

	prior := `{"schemas":{"public":{"tables":{"orders_items":{"enabled":true},"users":{"enabled":true}}}}}`
	planned := `{"schemas":{"public":{"tables":{"users":{"enabled":true}}}}}`
	catalog := `{"schemas":{"public":{"tables":{"orders_items":{"enabled":false,"enabledPatchSettings":{"allowed":false}},"users":{"enabled":true}}}}}`

	expanded, diags := addDisablesForDroppedTables(prior, planned, catalog)

	require.False(t, diags.HasError(), "%v", diags)
	require.JSONEq(t, planned, expanded)
	require.Empty(t, diags.Warnings())
}

// The client answers "{}" whenever a GET or PATCH carries no data, so effective_schema can hold it.
// That is an absent catalog, not evidence every table vanished - the same reading
// warnSchemasMissingFromCatalog takes - and it must warn like a missing one.
func TestAddDisablesForDroppedTables_WarnsWhenCatalogCarriesNoSchemas(t *testing.T) {
	t.Parallel()

	prior := `{"schemas":{"public":{"tables":{"orders":{"enabled":true},"users":{"enabled":true}}}}}`
	planned := `{"schemas":{"public":{"tables":{"users":{"enabled":true}}}}}`

	expanded, diags := addDisablesForDroppedTables(prior, planned, "{}")

	require.False(t, diags.HasError(), "%v", diags)
	require.JSONEq(t, planned, expanded)
	warnings := diags.Warnings()
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0].Detail(), `"public"."orders"`)
}

// One diagnostic naming every table, since Terraform collapses warnings that share a summary.
func TestAddDisablesForDroppedTables_NamesEveryUndisabledTableInOneStableWarning(t *testing.T) {
	t.Parallel()

	prior := `{"schemas":{"analytics":{"tables":{"events":{"enabled":true}}},"public":{"tables":{"orders":{"enabled":true},"users":{"enabled":true}}}}}`
	planned := `{"schemas":{"public":{"tables":{"users":{"enabled":true}}}}}`

	// Go randomises map iteration per range, so one pass could produce sorted order by chance;
	// repeating drives that to nothing.
	for range 20 {
		_, diags := addDisablesForDroppedTables(prior, planned, "")

		require.False(t, diags.HasError(), "%v", diags)
		warnings := diags.Warnings()
		require.Len(t, warnings, 1)
		require.Contains(t, warnings[0].Detail(), `"analytics"."events", "public"."orders"`)
	}
}

func TestAddDisablesForDroppedTables_KeepsUnmanagedTopLevelKeys(t *testing.T) {
	t.Parallel()

	prior := `{"budget":7,"schemas":{"public":{"tables":{"orders":{"enabled":true},"users":{"enabled":true}}}}}`
	planned := `{"budget":7,"schemas":{"public":{"tables":{"users":{"enabled":true}}}}}`
	catalog := `{"schemas":{"public":{"tables":{"orders":{"enabled":true},"users":{"enabled":true}}}}}`

	expanded, diags := addDisablesForDroppedTables(prior, planned, catalog)

	require.False(t, diags.HasError(), "%v", diags)
	require.JSONEq(
		t,
		`{"budget":7,"schemas":{"public":{"tables":{"orders":{"enabled":false},"users":{"enabled":true}}}}}`,
		expanded,
	)
	require.Empty(t, diags.Warnings())
}
