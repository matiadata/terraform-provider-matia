package provider

import (
	"fmt"
	"maps"

	"github.com/hashicorp/terraform-plugin-framework/diag"
)

type initialSelectionTarget struct {
	schema string
	table  string
	column string
}

type initialSchemaSelection struct {
	config   string
	disables []initialSelectionTarget
}

func usesInitialSelection(policy string) bool {
	return policy == "enableColumnChanges" || policy == "ignoreAll"
}

// buildInitialSchemaSelection expands only the outgoing PATCH. Terraform state must keep the
// user's declared config, otherwise Create returns a value that differs from the plan. Subsequent
// updates must not continually disable tables discovered under the integration's schema policy.
func buildInitialSchemaSelection(config, catalog, policy string) (initialSchemaSelection, diag.Diagnostics) {
	selection := initialSchemaSelection{config: config}
	var diags diag.Diagnostics
	if !usesInitialSelection(policy) {
		return selection, diags
	}
	declared, diags := parseSchemaConfigPayload(config)
	if diags.HasError() {
		return selection, diags
	}
	catalogSchemas, catalogDiags := parseSchemaCatalog(catalog)
	diags.Append(catalogDiags...)
	if diags.HasError() {
		return selection, diags
	}

	// Readiness of the integration alone does not guarantee discovery has produced a catalog,
	// e.g. for older hybrid agents. Do not report a successful initial selection over no tables.
	tableCount := 0
	for _, schema := range catalogSchemas {
		tableCount += len(schemaTables(schema))
	}
	if tableCount == 0 {
		diags.AddError(
			"Schema Catalog Not Ready",
			"Initial table selection requires a discovered catalog. No tables were returned. Retry after schema discovery completes; no selection PATCH was sent.",
		)
		return selection, diags
	}

	var skipped []string
	for schemaName, catalogSchema := range catalogSchemas {
		declaredTables := schemaTables(declared.schemas[schemaName])
		changes := map[string]any{}
		for tableName, value := range schemaTables(catalogSchema) {
			table, ok := value.(map[string]any)
			if !ok {
				continue
			}
			declaredTable, listed := declaredTables[tableName]
			if !isPatchableTable(table) {
				if enabled, known := tableEnabled(table); !listed && (!known || enabled) {
					skipped = append(skipped, fmt.Sprintf("%q.%q", schemaName, tableName))
				}
				continue
			}
			patch, _ := declaredTable.(map[string]any)
			patch = maps.Clone(patch)
			if patch == nil {
				patch = map[string]any{}
			}
			if enabled, known := tableEnabled(table); !listed && (!known || enabled) {
				patch["enabled"] = false
				selection.disables = append(
					selection.disables,
					initialSelectionTarget{schema: schemaName, table: tableName},
				)
			}
			if policy == "ignoreAll" {
				selection.disableUnlistedColumns(schemaName, tableName, patch, table)
			}
			if len(patch) > 0 {
				changes[tableName] = patch
			}
		}
		if len(changes) > 0 {
			declared.schemas[schemaName] = schemaWithTables(declared.schemas[schemaName], changes)
		}
	}
	diags.Append(consolidatedConfigWarning("Initial Table Selection Incomplete", skipped, func(names string) string {
		return "The API does not allow disabling these omitted tables: " + names + ". They may remain enabled; inspect effective_schema before unpausing the integration."
	})...)
	declared.raw["schemas"] = declared.schemas
	encoded, err := marshalSchemaConfigSorted(declared.raw)
	if err != nil {
		diags.AddError("Invalid config JSON", err.Error())
		return selection, diags
	}
	selection.config = string(encoded)
	return selection, diags
}

func (s *initialSchemaSelection) disableUnlistedColumns(schemaName, tableName string, patch, table map[string]any) {
	columns, _ := table["columns"].(map[string]any)
	declaredColumns, _ := patch["columns"].(map[string]any)
	changes := maps.Clone(declaredColumns)
	if changes == nil {
		changes = map[string]any{}
	}
	for name, value := range columns {
		column, ok := value.(map[string]any)
		if !ok || !isPatchableColumn(column) {
			continue
		}
		if _, listed := declaredColumns[name]; listed {
			continue
		}
		if enabled, known := tableEnabled(column); known && !enabled {
			continue
		}
		// The API interprets omitted hashed as false. Preserve it to avoid unhashing data and
		// scheduling a resync as a side effect of an automatically generated column disable.
		hashed, _ := column["hashed"].(bool)
		changes[name] = map[string]any{"enabled": false, "hashed": hashed}
		s.disables = append(s.disables, initialSelectionTarget{schema: schemaName, table: tableName, column: name})
	}
	if len(changes) > 0 {
		patch["columns"] = changes
	}
}

// Automatic entries are not retained in config, so ordinary drift detection cannot surface an
// ignored disable. Check the PATCH response, especially for sources without column blocking.
func (s *initialSchemaSelection) verify(catalog string) diag.Diagnostics {
	schemas, diags := parseSchemaCatalog(catalog)
	if diags.HasError() {
		return diags
	}
	var unapplied []string
	for _, target := range s.disables {
		table, _ := schemaTables(schemas[target.schema])[target.table].(map[string]any)
		object := table
		name := fmt.Sprintf("%q.%q", target.schema, target.table)
		if target.column != "" {
			columns, _ := table["columns"].(map[string]any)
			object, _ = columns[target.column].(map[string]any)
			name += fmt.Sprintf(".%q", target.column)
		}
		if enabled, known := tableEnabled(object); !known || enabled {
			unapplied = append(unapplied, name)
		}
	}
	diags.Append(
		consolidatedConfigWarning("Initial Selection Was Not Fully Applied", unapplied, func(joined string) string {
			return "The API did not confirm these automatic disables: " + joined + ". Column disabling requires source support for field-level data blocking. Inspect effective_schema before syncing. Initial selection runs only when this Terraform resource is created; later applies do not retry automatic disables."
		})...)
	return diags
}
