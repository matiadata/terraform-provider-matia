package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"

	"github.com/matiadata/terraform-provider-matia/internal/provider/client"
)

// schemaCatalogDefaultName mirrors the backend's DEFAULT_SCHEMA_NAME: streams without a namespace
// are catalogued under it, and ensureSchemaExists skips its existence check entirely, so PATCH
// never answers SCHEMA_NOT_FOUND for a schema by this name.
const schemaCatalogDefaultName = "schema"

// syncModeAliases maps user-friendly aliases to Matia catalog ESyncMode values.
var syncModeAliases = map[string]string{
	"incremental":                    "Incremental",
	"incremental_append_only":        "Incremental (Append-Only)",
	"full_refresh":                   "Full Refresh",
	"change_stream":                  "Change Stream",
	"cdc":                            "Change Stream",
	"change_tracking":                "Change Tracking",
	"change_stream_append_only":      "Change Stream (Append-Only)",
	"change_stream_initial_snapshot": "Change Stream (Initial Snapshot)",
	"change_stream_no_snapshot":      "Change Stream (No Snapshot)",
}

var syncModeAPIValues = buildSyncModeLookup()

// buildSyncModeLookup registers each alias and each canonical API value. One entry per mode also
// covers the form GET returns: the backend emits syncMode.toUpperCase().replace(' ', '_') and
// JavaScript's string replace substitutes only the first match, so GET keeps the remaining spaces
// ("CHANGE_STREAM (INITIAL SNAPSHOT)") - and normalizeSyncModeKey folds every space alike, reducing
// both spellings to one key. Enumerating exact spellings beats a permissive normalizer, which would
// fold a typo like "cdc!!!" onto a real mode and send it as valid instead of letting the API
// reject it.
func buildSyncModeLookup() map[string]string {
	lookup := map[string]string{}
	for alias, apiValue := range syncModeAliases {
		lookup[alias] = apiValue
		lookup[normalizeSyncModeKey(apiValue)] = apiValue
	}
	return lookup
}

// patchManagedColumnFields are the only column fields UpdateIntegrationSchemaConfigColumnDto
// accepts, so they are both what the Update no-op comparison keeps and what Read projects.
var patchManagedColumnFields = []string{"enabled", "hashed"}

func normalizeSyncModeKey(mode string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(mode), " ", "_"))
}

func resolveSyncModeForAPI(mode string) string {
	if mode == "" {
		return mode
	}

	key := normalizeSyncModeKey(mode)
	if mapped, ok := syncModeAPIValues[key]; ok {
		return mapped
	}

	return mode
}

func prepareSchemaConfigForAPI(config string) (json.RawMessage, diag.Diagnostics) {
	payload, diags := parseSchemaConfigPayload(config)
	if diags.HasError() {
		return nil, diags
	}

	for _, schemaVal := range payload.schemas {
		schema, schemaOK := schemaVal.(map[string]any)
		if !schemaOK {
			continue
		}
		tables, tablesOK := schema["tables"].(map[string]any)
		if !tablesOK {
			continue
		}
		for _, tableVal := range tables {
			table, tableOK := tableVal.(map[string]any)
			if !tableOK {
				continue
			}
			if syncMode, syncModeOK := table["syncMode"].(string); syncModeOK && syncMode != "" {
				table["syncMode"] = resolveSyncModeForAPI(syncMode)
			}
		}
	}

	encoded, err := marshalSchemaConfigSorted(payload.raw)
	if err != nil {
		diags.AddError("Invalid config JSON", err.Error())
		return nil, diags
	}

	return json.RawMessage(encoded), diags
}

// canonicalizeSchemaConfigForComparison canonicalizes a config for Update no-op detection: it
// resolves syncMode to its canonical Matia API value (so aliases like cdc and change_stream
// compare equal) and keeps columns, so a column-only edit is not mistaken for a no-op.
func canonicalizeSchemaConfigForComparison(config string) (string, diag.Diagnostics) {
	return canonicalizeSchemaConfig(config, resolveSyncModeForAPI, true)
}

// projectSchemaConfig rewrites the user's declared config with the catalog's current values for
// the fields PATCH /integrations/:id/schemas manages, which is what turns a server-side edit into
// a plan diff. It stays sparse - catalog entries the user never declared are left out - and keeps
// the user's spelling wherever the server agrees, so equivalent syncMode aliases do not churn.
//
// WARNING: the field set below must match canonicalizeSchemaConfig's. Update compares plan
// against state on that reduction while Read projects on this one, so a field handled by only one
// of them is either drift nothing can detect or a real edit that never reaches the API.
//
// A declared entry the catalog does not list is kept, never dropped, though PATCH treats the three
// levels differently: it answers 200 and silently skips an unknown table or a deleted column, but
// rejects an unknown schema outright with SCHEMA_NOT_FOUND (ensureSchemaExists). Dropping any of
// them here would put a diff in every plan that no apply can settle - at table and column level
// because the write is a no-op, at schema level because the apply errors instead.
func projectSchemaConfig(declared, catalog string) (string, diag.Diagnostics) {
	declaredPayload, diags := parseSchemaConfigPayload(declared)
	if diags.HasError() {
		return "", diags
	}

	catalogSchemas, catalogDiags := parseSchemaCatalog(catalog)
	diags.Append(catalogDiags...)
	if diags.HasError() {
		return "", diags
	}

	projected := maps.Clone(declaredPayload.raw)
	projected["schemas"] = projectSchemas(declaredPayload.schemas, catalogSchemas)
	diags.Append(warnSchemasMissingFromCatalog(declaredPayload.schemas, catalogSchemas)...)

	encoded, err := marshalSchemaConfigSorted(projected)
	if err != nil {
		diags.AddError("Invalid config JSON", err.Error())
		return "", diags
	}

	return string(encoded), diags
}

// warnSchemasMissingFromCatalog reports declared schemas the catalog no longer lists. Keeping them
// is correct, but it is also the one drop the rest of a plan cannot show: projection reports drift
// only on keys the catalog lists, so refreshes stay clean until an edit anywhere in the config
// sends the whole thing in one PATCH and ensureSchemaExists rejects it before applying any of it.
func warnSchemasMissingFromCatalog(declared, catalog map[string]any) diag.Diagnostics {
	var diags diag.Diagnostics

	// No schemas object at all is the client's "{}" fallback for a response carrying no data: an
	// absent comparison, not evidence every declared schema vanished. An empty schemas object is
	// deliberately NOT treated the same - PATCH really does reject every declared schema against
	// an empty catalog, so those warnings are correct.
	if catalog == nil {
		return diags
	}

	missing := make([]string, 0, len(declared))
	for schemaName := range declared {
		if schemaName == schemaCatalogDefaultName {
			continue
		}
		if _, listed := catalog[schemaName]; !listed {
			missing = append(missing, strconv.Quote(schemaName))
		}
	}
	diags.Append(consolidatedConfigWarning("Declared Schema Missing From Catalog", missing, func(joined string) string {
		return fmt.Sprintf(
			"Config declares schemas the integration catalog no longer lists: %s. Terraform "+
				"keeps them in state and reports no drift for them, so plans stay clean - but an "+
				"apply sends the whole config in one PATCH, and the API rejects the whole request "+
				// Quote the sentence the failed apply actually prints, so the user connects it
				// back to this warning.
				"with %q, including the edits you made to schemas that do still exist. Remove "+
				"them from config, or restore them in the source.",
			joined,
			client.SchemaNotFoundMessage,
		)
	})...)

	return diags
}

// addDisablesForDroppedTables makes deleting a table from config mean something: a table the last
// apply declared and this one does not is sent as enabled:false, instead of being omitted from the
// sparse PATCH and left syncing.
//
// Terraform stays authoritative only over tables it has actually declared, with prior state as the
// ownership record - the model Kubernetes server-side apply uses for managed fields. A table it
// never declared is left alone, which is what stops this from undoing the integration's own
// onSchemaUpdate policy on every apply.
func addDisablesForDroppedTables(prior, planned, catalog string) (string, diag.Diagnostics) {
	var diags diag.Diagnostics

	plannedPayload, plannedDiags := parseSchemaConfigPayload(planned)
	diags.Append(plannedDiags...)
	if diags.HasError() {
		return "", diags
	}

	priorPayload, priorDiags := parseSchemaConfigPayload(prior)
	diags.Append(priorDiags...)
	if diags.HasError() {
		return "", diags
	}

	// effective_schema is null in state written before that attribute existed, so an apply run with
	// -refresh=false reaches Update with no catalog. Nothing can be disabled safely without one,
	// and the same apply drops the table from config, spending the only chance to disable it - so
	// the tables that misses are reported rather than skipped in silence.
	var catalogSchemas map[string]any
	if strings.TrimSpace(catalog) != "" {
		var catalogDiags diag.Diagnostics
		catalogSchemas, catalogDiags = parseSchemaCatalog(catalog)
		diags.Append(catalogDiags...)
		if diags.HasError() {
			return "", diags
		}
	}
	// A nil schemas map is the client's "{}" fallback for a response carrying no data, which reads
	// the same as no catalog at all - the reading warnSchemasMissingFromCatalog already takes.
	catalogAvailable := catalogSchemas != nil

	expanded := maps.Clone(plannedPayload.raw)
	schemas := maps.Clone(plannedPayload.schemas)
	var undisabled []string

	for schemaName, priorSchema := range priorPayload.schemas {
		disabled, skipped := droppedTableDisables(
			schemaTables(priorSchema),
			schemaTables(schemas[schemaName]),
			schemaTables(catalogSchemas[schemaName]),
			catalogAvailable,
		)
		for _, tableName := range skipped {
			undisabled = append(undisabled, strconv.Quote(schemaName)+"."+strconv.Quote(tableName))
		}
		if len(disabled) == 0 {
			continue
		}
		schemas[schemaName] = schemaWithTables(schemas[schemaName], disabled)
	}
	expanded["schemas"] = schemas

	// Removal also erases these from the ownership record, so no later apply retries: without this
	// the run reports success over a table that is still syncing and billing. The message stops
	// short of "re-add it with enabled = false" on purpose - that works for the no-catalog cause
	// but not for a flattened subtable, where PATCH answers 200 without changing anything and
	// projectSchema echoes the declared value back, leaving a permanently clean plan over a table
	// that never turned off.
	diags.Append(consolidatedConfigWarning(
		"Removed Table Was Not Disabled",
		undisabled,
		func(joined string) string {
			return fmt.Sprintf(
				"These tables were removed from config but Terraform could not disable them, so "+
					"they may still be syncing: %s. Terraform no longer tracks them, so "+
					"re-applying will not retry - check them in the Matia UI.",
				joined,
			)
		},
	)...)

	encoded, err := marshalSchemaConfigSorted(expanded)
	if err != nil {
		diags.AddError("Invalid config JSON", err.Error())
		return "", diags
	}

	return string(encoded), diags
}

// consolidatedConfigWarning returns ONE warning naming every item rather than one per item:
// Terraform consolidates warnings that share a summary, printing the first and collapsing the rest
// into "(and one more similar warning elsewhere)", which would hide the very names the user needs.
// Sorting keeps two identical plans identical, since map iteration is randomised.
func consolidatedConfigWarning(
	summary string,
	names []string,
	detail func(joined string) string,
) diag.Diagnostics {
	var diags diag.Diagnostics
	if len(names) == 0 {
		return diags
	}
	sort.Strings(names)
	diags.AddAttributeWarning(path.Root("config"), summary, detail(strings.Join(names, ", ")))

	return diags
}

func schemaTables(schemaValue any) map[string]any {
	schema, ok := schemaValue.(map[string]any)
	if !ok {
		return nil
	}
	tables, _ := schema["tables"].(map[string]any)
	return tables
}

// droppedTableDisables returns the enabled:false entries to add for tables the prior config owned
// and the planned config no longer declares, plus the names of the ones it could not disable.
//
// Ownership is per field rather than per table, matching projectTable and the server-side-apply
// model this follows: a prior entry that never set `enabled` - the syncMode-only shape this repo's
// own examples use - means Terraform never turned that table on, so removing it must not turn it
// off.
//
// Requiring the catalog to list the table is also what keeps a dropped schema safe: PATCH rejects
// an unknown schema outright with SCHEMA_NOT_FOUND, so resurrecting one here purely to disable its
// tables would fail the whole apply - and would fail exactly the cleanup
// warnSchemasMissingFromCatalog tells the user to perform.
func droppedTableDisables(
	prior, planned, catalog map[string]any,
	catalogAvailable bool,
) (map[string]any, []string) {
	disabled := map[string]any{}
	var undisabled []string

	for tableName, priorTable := range prior {
		if _, stillDeclared := planned[tableName]; stillDeclared {
			continue
		}
		// Terraform never turned this table on, or already turned it off: nothing to undo either way.
		if priorEnabled, owned := tableEnabled(priorTable); !owned || !priorEnabled {
			continue
		}
		if !catalogAvailable {
			undisabled = append(undisabled, tableName)
			continue
		}

		catalogTable, listed := catalog[tableName].(map[string]any)
		if !listed {
			// Already gone from the source, so nothing is still syncing to disable or report.
			continue
		}
		if upstreamEnabled, known := tableEnabled(catalogTable); known && !upstreamEnabled {
			continue
		}
		if !isPatchableTable(catalogTable) {
			undisabled = append(undisabled, tableName)
			continue
		}
		disabled[tableName] = map[string]any{"enabled": false}
	}

	return disabled, undisabled
}

// tableEnabled reports a table entry's `enabled` value and whether it set one at all. It reads
// both user config and catalog entries, so it names the field rather than its provenance; the
// presence half is the distinction per-field ownership rests on.
func tableEnabled(tableValue any) (bool, bool) {
	table, ok := tableValue.(map[string]any)
	if !ok {
		return false, false
	}
	enabled, declared := table["enabled"].(bool)
	return enabled, declared
}

func schemaWithTables(schemaValue any, extra map[string]any) map[string]any {
	schema, ok := schemaValue.(map[string]any)
	if ok {
		schema = maps.Clone(schema)
	} else {
		schema = map[string]any{}
	}

	tables := map[string]any{}
	maps.Copy(tables, schemaTables(schema))
	maps.Copy(tables, extra)
	schema["tables"] = tables

	return schema
}

func parseSchemaCatalog(catalog string) (map[string]any, diag.Diagnostics) {
	var diags diag.Diagnostics

	payload, err := decodeJSONObject(catalog)
	if err != nil {
		diags.AddError("Unexpected API Response", "integration schema response is not valid JSON: "+err.Error())
		return nil, diags
	}

	// A catalog with no schemas object (the client's "{}" fallback, or an integration whose
	// streams are not discovered yet) means there is nothing to compare against, not that every
	// declared table vanished.
	schemas, _ := payload["schemas"].(map[string]any)

	return schemas, diags
}

func projectSchemas(declared, catalog map[string]any) map[string]any {
	out := map[string]any{}
	for schemaName, declaredVal := range declared {
		declaredSchema, declaredOK := declaredVal.(map[string]any)
		catalogSchema, catalogOK := catalog[schemaName].(map[string]any)
		if !declaredOK || !catalogOK {
			out[schemaName] = declaredVal
			continue
		}
		out[schemaName] = projectSchema(declaredSchema, catalogSchema)
	}
	return out
}

func projectSchema(declared, catalog map[string]any) map[string]any {
	out := maps.Clone(declared)

	declaredTables, declaredOK := declared["tables"].(map[string]any)
	if !declaredOK {
		return out
	}
	catalogTables, _ := catalog["tables"].(map[string]any)

	outTables := map[string]any{}
	for tableName, declaredVal := range declaredTables {
		declaredTable, tableOK := declaredVal.(map[string]any)
		catalogTable, catalogOK := catalogTables[tableName].(map[string]any)
		if !tableOK || !catalogOK || !isPatchableTable(catalogTable) {
			outTables[tableName] = declaredVal
			continue
		}
		outTables[tableName] = projectTable(declaredTable, catalogTable)
	}
	out["tables"] = outTables

	return out
}

// isPatchableColumn reports whether the schemas PATCH can write this column. Primary-key columns
// are skipped outright when the catalog applies column updates, so their reported values are not
// drift the user can act on.
func isPatchableColumn(catalogColumn map[string]any) bool {
	isPrimaryKey, ok := catalogColumn["isPrimaryKey"].(bool)
	return !ok || !isPrimaryKey
}

// isPatchableTable reports whether the schemas PATCH can actually write this table. Flattened
// subtables are listed in the catalog but resolved only through the parent stream, so PATCH
// returns 200 and changes nothing; projecting their server values would report drift that no
// apply can ever clear.
func isPatchableTable(catalogTable map[string]any) bool {
	settings, ok := catalogTable["enabledPatchSettings"].(map[string]any)
	if !ok {
		return true
	}
	allowed, ok := settings["allowed"].(bool)
	return !ok || allowed
}

func projectTable(declared, catalog map[string]any) map[string]any {
	out := maps.Clone(declared)

	if _, isDeclared := declared["enabled"]; isDeclared {
		if enabled, ok := catalog["enabled"].(bool); ok {
			out["enabled"] = enabled
		}
	}
	if declaredMode, isDeclared := declared["syncMode"].(string); isDeclared {
		if catalogMode, ok := catalog["syncMode"].(string); ok && catalogMode != "" {
			out["syncMode"] = reconcileSyncMode(declaredMode, catalogMode)
		}
	}
	// GET omits cursorField unless the column is selectable, so its absence is not evidence the
	// server dropped it - only an actual value can contradict what the user declared.
	if _, isDeclared := declared["cursorField"]; isDeclared {
		if cursorField, ok := catalog["cursorField"].(string); ok && cursorField != "" {
			out["cursorField"] = cursorField
		}
	}
	if declaredColumns, isDeclared := declared["columns"].(map[string]any); isDeclared {
		catalogColumns, _ := catalog["columns"].(map[string]any)
		out["columns"] = projectColumns(declaredColumns, catalogColumns)
	}

	return out
}

func projectColumns(declared, catalog map[string]any) map[string]any {
	out := map[string]any{}
	for columnName, declaredVal := range declared {
		declaredColumn, declaredOK := declaredVal.(map[string]any)
		catalogColumn, catalogOK := catalog[columnName].(map[string]any)
		if !declaredOK || !catalogOK || !isPatchableColumn(catalogColumn) {
			out[columnName] = declaredVal
			continue
		}

		outColumn := maps.Clone(declaredColumn)
		for _, field := range patchManagedColumnFields {
			if _, isDeclared := declaredColumn[field]; !isDeclared {
				continue
			}
			if value, ok := catalogColumn[field].(bool); ok {
				outColumn[field] = value
			}
		}
		out[columnName] = outColumn
	}
	return out
}

// reconcileSyncMode keeps the user's spelling when it denotes the same mode the catalog reports,
// so an alias (cdc) and GET's upper-case form (CHANGE_STREAM) are not mistaken for drift.
func reconcileSyncMode(declared, catalog string) string {
	resolvedCatalog := resolveSyncModeForAPI(catalog)
	if resolveSyncModeForAPI(declared) == resolvedCatalog {
		return declared
	}
	return resolvedCatalog
}

func canonicalizeSchemaConfig(
	config string,
	syncModeTransform func(string) string,
	includeColumns bool,
) (string, diag.Diagnostics) {
	payload, diags := parseSchemaConfigPayload(config)
	if diags.HasError() {
		return "", diags
	}

	outSchemas := map[string]any{}
	canonical := map[string]any{
		"schemas": outSchemas,
	}

	for schemaName, schemaVal := range payload.schemas {
		schema, schemaOK := schemaVal.(map[string]any)
		if !schemaOK {
			continue
		}

		outTables := map[string]any{}
		tables, tablesOK := schema["tables"].(map[string]any)
		if !tablesOK {
			outSchemas[schemaName] = map[string]any{"tables": outTables}
			continue
		}

		for tableName, tableVal := range tables {
			table, tableOK := tableVal.(map[string]any)
			if !tableOK {
				continue
			}

			outTable := map[string]any{}
			if enabled, enabledOK := table["enabled"].(bool); enabledOK {
				outTable["enabled"] = enabled
			}
			if syncMode, syncModeOK := table["syncMode"].(string); syncModeOK && syncMode != "" {
				outTable["syncMode"] = syncModeTransform(syncMode)
			}
			if cursorField, cursorFieldOK := table["cursorField"].(string); cursorFieldOK && cursorField != "" {
				outTable["cursorField"] = cursorField
			}
			if includeColumns {
				if columns, columnsOK := table["columns"].(map[string]any); columnsOK && len(columns) > 0 {
					outTable["columns"] = canonicalizeSchemaColumns(columns)
				}
			}

			if len(outTable) > 0 {
				outTables[tableName] = outTable
			}
		}

		outSchemas[schemaName] = map[string]any{"tables": outTables}
	}

	encoded, err := marshalSchemaConfigSorted(canonical)
	if err != nil {
		diags.AddError("Invalid config JSON", err.Error())
		return "", diags
	}

	return string(encoded), diags
}

// canonicalizeSchemaColumns reduces columns to the only fields the schemas API acts on (enabled,
// hashed), so a column-level edit is caught by the Update no-op comparison instead of dropped.
func canonicalizeSchemaColumns(columns map[string]any) map[string]any {
	outColumns := map[string]any{}
	for columnName, columnVal := range columns {
		column, columnOK := columnVal.(map[string]any)
		if !columnOK {
			continue
		}
		outColumn := map[string]any{}
		for _, field := range patchManagedColumnFields {
			if value, ok := column[field].(bool); ok {
				outColumn[field] = value
			}
		}
		if len(outColumn) > 0 {
			outColumns[columnName] = outColumn
		}
	}
	return outColumns
}

type schemaConfigPayload struct {
	raw     map[string]any
	schemas map[string]any
}

func parseSchemaConfigPayload(config string) (*schemaConfigPayload, diag.Diagnostics) {
	var diags diag.Diagnostics

	trimmed := strings.TrimSpace(config)
	if trimmed == "" {
		diags.AddError("Invalid config JSON", "config must be valid JSON.")
		return nil, diags
	}

	if !json.Valid([]byte(trimmed)) {
		diags.AddError("Invalid config JSON", "config must be valid JSON.")
		return nil, diags
	}

	payload, err := decodeJSONObject(trimmed)
	if err != nil {
		diags.AddError("Invalid config JSON", err.Error())
		return nil, diags
	}

	schemas, ok := payload["schemas"].(map[string]any)
	if !ok {
		diags.AddError(
			"Invalid config JSON",
			"config must include a top-level schemas object (Matia PATCH /integrations/:id/schemas format).",
		)
		return nil, diags
	}

	return &schemaConfigPayload{
		raw:     payload,
		schemas: schemas,
	}, diags
}

// decodeJSONObject decodes with UseNumber so numeric literals survive the decode/encode round trip
// byte for byte; the default any decoding turns every number into a float64 and would silently
// rewrite a large integer (12345678901234567890 becomes 12345678901234567000). Trailing content is
// rejected explicitly, because a Decoder stops at the first value and would otherwise accept input
// json.Unmarshal refused.
func decodeJSONObject(raw string) (map[string]any, error) {
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(raw)))
	decoder.UseNumber()

	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	if decoder.More() {
		return nil, errors.New("unexpected content after top-level JSON value")
	}
	return payload, nil
}

func marshalSchemaConfigSorted(value map[string]any) ([]byte, error) {
	return json.Marshal(sortJSONKeys(value))
}

func sortJSONKeys(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)

		sorted := make(map[string]any, len(typed))
		for _, key := range keys {
			sorted[key] = sortJSONKeys(typed[key])
		}
		return sorted
	case []any:
		sorted := make([]any, len(typed))
		for i, item := range typed {
			sorted[i] = sortJSONKeys(item)
		}
		return sorted
	default:
		return value
	}
}
