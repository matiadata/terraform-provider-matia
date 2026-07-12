package provider

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
)

// syncModeAPIValues maps user-friendly aliases to Matia catalog ESyncMode values.
var syncModeAPIValues = map[string]string{
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

// syncModeStateAliases defines the preferred alias order when reversing API values.
var syncModeStateAliases = []string{
	"incremental",
	"incremental_append_only",
	"full_refresh",
	"change_stream",
	"change_tracking",
	"change_stream_append_only",
	"change_stream_initial_snapshot",
	"change_stream_no_snapshot",
}

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

	for _, apiValue := range syncModeAPIValues {
		if strings.EqualFold(mode, apiValue) {
			return apiValue
		}
	}

	return mode
}

func reverseSyncModeForState(apiMode string) string {
	if apiMode == "" {
		return apiMode
	}

	key := normalizeSyncModeKey(apiMode)
	for _, alias := range syncModeStateAliases {
		if key == normalizeSyncModeKey(syncModeAPIValues[alias]) {
			return alias
		}
	}

	return apiMode
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

func canonicalizeSchemaConfigForState(config string) (string, diag.Diagnostics) {
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
				outTable["syncMode"] = reverseSyncModeForState(syncMode)
			}
			if cursorField, cursorFieldOK := table["cursorField"].(string); cursorFieldOK && cursorField != "" {
				outTable["cursorField"] = cursorField
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

	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
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
