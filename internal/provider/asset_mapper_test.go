package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/require"
)

func TestMergeConnectionFields(t *testing.T) {
	merged, diags := mergeConnectionFields(
		types.StringValue(`{"hostname":"localhost","port":"5432","database":"postgres","ssl":false}`),
		types.StringValue(`{"username":"postgres","password":"secret"}`),
	)
	require.False(t, diags.HasError(), "diags = %v", diags)
	require.Equal(t, "postgres", merged["username"])
	require.Equal(t, "secret", merged["password"])
	ssl, ok := merged["ssl"].(bool)
	require.True(t, ok)
	require.False(t, ssl)
}
