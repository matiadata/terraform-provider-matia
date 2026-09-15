package provider

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const initialSelectionCatalog = `{"schemas":{
  "public":{"tables":{
    "users":{"enabled":true,"enabledPatchSettings":{"allowed":true},"columns":{
      "id":{"enabled":true,"isPrimaryKey":true},
      "email":{"enabled":true,"hashed":false},
      "secret":{"enabled":true,"hashed":true},
      "off":{"enabled":false}
    }},
    "orders":{"enabled":true,"columns":{"id":{"enabled":true,"isPrimaryKey":true}}},
    "already_off":{"enabled":false},
    "users_child":{"enabled":true,"enabledPatchSettings":{"allowed":false}}
  }},
  "other":{"tables":{"events":{"enabled":true}}}
}}`

func TestInitialSchemaSelection(t *testing.T) {
	t.Parallel()
	const config = `{"schemas":{"public":{"tables":{"users":{"enabled":true,"syncMode":"cdc","columns":{"email":{"enabled":true}}}}}}}`
	for _, tc := range []struct {
		policy string
		want   string
	}{
		{"enableAll", config},
		{"pauseConnection", config},
		{"enableNamespaceChanges", config},
		{"", config},
		{"future-policy", config},
		{"enableColumnChanges", `{"schemas":{
          "public":{"tables":{
            "users":{"enabled":true,"syncMode":"cdc","columns":{"email":{"enabled":true}}},
            "orders":{"enabled":false}
          }},
          "other":{"tables":{"events":{"enabled":false}}}
        }}`},
		{"ignoreAll", `{"schemas":{
          "public":{"tables":{
            "users":{"enabled":true,"syncMode":"cdc","columns":{
              "email":{"enabled":true},"secret":{"enabled":false,"hashed":true}
            }},
            "orders":{"enabled":false}
          }},
          "other":{"tables":{"events":{"enabled":false}}}
        }}`},
	} {
		t.Run(tc.policy, func(t *testing.T) {
			t.Parallel()
			selection, diags := buildInitialSchemaSelection(config, initialSelectionCatalog, tc.policy)
			require.False(t, diags.HasError(), "%v", diags)
			require.JSONEq(t, tc.want, selection.config)
		})
	}
}

func TestInitialSchemaSelection_EmptySelectionAndExplicitFields(t *testing.T) {
	t.Parallel()
	selection, diags := buildInitialSchemaSelection(`{"schemas":{}}`, initialSelectionCatalog, "enableColumnChanges")
	require.False(t, diags.HasError(), "%v", diags)
	require.JSONEq(t, `{"schemas":{
      "public":{"tables":{"users":{"enabled":false},"orders":{"enabled":false}}},
      "other":{"tables":{"events":{"enabled":false}}}
    }}`, selection.config)

	// A declaration of other fields does not implicitly enable a table or change a column's
	// enabled setting. User-supplied disables, hashing, and unknown names survive expansion.
	selection, diags = buildInitialSchemaSelection(`{"schemas":{"public":{"tables":{
      "users":{"syncMode":"cdc","columns":{"secret":{"hashed":true},"email":{"enabled":false}}},
      "orders":{"enabled":false},"unknown":{"enabled":true}
    }}}}`, initialSelectionCatalog, "ignoreAll")
	require.False(t, diags.HasError(), "%v", diags)
	require.JSONEq(t, `{"schemas":{
      "public":{"tables":{
        "users":{"syncMode":"cdc","columns":{"secret":{"hashed":true},"email":{"enabled":false}}},
        "orders":{"enabled":false},"unknown":{"enabled":true}
      }},
      "other":{"tables":{"events":{"enabled":false}}}
    }}`, selection.config)
}

func TestInitialSchemaSelection_RequiresDiscoveredCatalog(t *testing.T) {
	t.Parallel()
	for _, catalog := range []string{`{}`, `{"schemas":{}}`, `{"schemas":{"public":{"tables":{}}}}`, `invalid`} {
		_, diags := buildInitialSchemaSelection(`{"schemas":{}}`, catalog, "ignoreAll")
		require.True(t, diags.HasError(), "catalog %s should not allow initial selection", catalog)
	}
	_, diags := buildInitialSchemaSelection(`invalid`, initialSelectionCatalog, "ignoreAll")
	require.True(t, diags.HasError())
}

func TestInitialSchemaSelection_VerifiesAutomaticDisables(t *testing.T) {
	t.Parallel()
	selection, diags := buildInitialSchemaSelection(
		`{"schemas":{"public":{"tables":{"users":{"enabled":true}}}}}`,
		initialSelectionCatalog,
		"ignoreAll",
	)
	require.False(t, diags.HasError(), "%v", diags)

	// A 200 response is not sufficient: a source without data blocking may ignore columns.
	diags = selection.verify(initialSelectionCatalog)
	require.False(t, diags.HasError())
	require.Len(t, diags, 1)
	require.Contains(t, diags[0].Detail(), `"public"."users"."secret"`)
	require.Contains(t, diags[0].Detail(), `"public"."orders"`)
	require.NotContains(t, diags[0].Detail(), `"public"."users"."id"`)

	diags = selection.verify(`{"schemas":{
      "public":{"tables":{
        "users":{"enabled":true,"columns":{"email":{"enabled":false},"secret":{"enabled":false}}},
        "orders":{"enabled":false}
      }},"other":{"tables":{"events":{"enabled":false}}}
    }}`)
	require.Empty(t, diags)
	diags = selection.verify(`{}`)
	require.Len(t, diags, 1, "missing response entries cannot confirm a disable")
}
