package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/require"
)

func TestProviderSchema_APIToken(t *testing.T) {
	p := New("test")()
	resp := &provider.SchemaResponse{}
	p.Schema(context.Background(), provider.SchemaRequest{}, resp)

	attr, ok := resp.Schema.Attributes["api_token"]
	require.True(t, ok, "api_token attribute missing from provider schema")
	require.False(
		t,
		attr.IsRequired(),
		"api_token should be Optional so it can come from the environment, not Required",
	)
	require.True(t, attr.IsOptional(), "api_token should be Optional")
	require.True(t, attr.IsSensitive(), "api_token should remain Sensitive")
}

func TestResolveProviderConfig_TokenResolution(t *testing.T) {
	cases := []struct {
		name     string
		envToken string
		attr     types.String
		want     string
	}{
		{"from attribute", "", types.StringValue("attr-token"), "attr-token"},
		{"from environment", "env-token", types.StringNull(), "env-token"},
		{"attribute wins over environment", "env-token", types.StringValue("attr-token"), "attr-token"},
		{"empty attribute falls back to environment", "env-token", types.StringValue(""), "env-token"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MATIA_API_TOKEN", tc.envToken)
			t.Setenv("MATIA_API_URL", "")

			config := providerConfigModel{APIToken: tc.attr, APIURL: types.StringNull()}
			token, _, diags := resolveProviderConfig(config)

			require.False(t, diags.HasError(), "unexpected diagnostics: %v", diags)
			require.Equal(t, tc.want, token)
		})
	}
}

func TestResolveProviderConfig_URLResolution(t *testing.T) {
	cases := []struct {
		name   string
		envURL string
		attr   types.String
		want   string
	}{
		{"defaults when unset", "", types.StringNull(), defaultAPIURL},
		{"from environment", "http://localhost:3001/v1", types.StringNull(), "http://localhost:3001/v1"},
		{"attribute wins over environment", "http://env/v1", types.StringValue("http://attr/v1"), "http://attr/v1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MATIA_API_TOKEN", "some-token")
			t.Setenv("MATIA_API_URL", tc.envURL)

			config := providerConfigModel{APIToken: types.StringValue("some-token"), APIURL: tc.attr}
			_, apiURL, diags := resolveProviderConfig(config)

			require.False(t, diags.HasError(), "unexpected diagnostics: %v", diags)
			require.Equal(t, tc.want, apiURL)
		})
	}
}

func TestResolveProviderConfig_MissingToken(t *testing.T) {
	t.Setenv("MATIA_API_TOKEN", "")
	t.Setenv("MATIA_API_URL", "")

	config := providerConfigModel{APIToken: types.StringNull(), APIURL: types.StringNull()}
	_, _, diags := resolveProviderConfig(config)

	require.True(t, diags.HasError(), "expected an error when neither api_token nor MATIA_API_TOKEN is set")
	require.True(t, diagnosticsContain(diags, "MATIA_API_TOKEN"),
		"error should mention the MATIA_API_TOKEN environment variable; got %v", diags)
}

func TestResolveProviderConfig_UnknownToken(t *testing.T) {
	t.Setenv("MATIA_API_TOKEN", "env-token")
	t.Setenv("MATIA_API_URL", "")

	config := providerConfigModel{APIToken: types.StringUnknown(), APIURL: types.StringNull()}
	_, _, diags := resolveProviderConfig(config)

	require.True(
		t,
		diags.HasError(),
		"expected an error for an unknown api_token value even when the environment is set",
	)
}

func TestResolveProviderConfig_UnknownURL(t *testing.T) {
	t.Setenv("MATIA_API_TOKEN", "env-token")
	t.Setenv("MATIA_API_URL", "env-url")

	config := providerConfigModel{APIToken: types.StringValue("some-token"), APIURL: types.StringUnknown()}
	_, _, diags := resolveProviderConfig(config)

	require.True(t, diags.HasError(), "expected an error for an unknown api_url value")
}

func diagnosticsContain(diags diag.Diagnostics, substr string) bool {
	for _, d := range diags {
		if strings.Contains(d.Summary(), substr) || strings.Contains(d.Detail(), substr) {
			return true
		}
	}
	return false
}
