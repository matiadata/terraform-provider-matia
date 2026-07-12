package provider

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReplicationFrequencyForState(t *testing.T) {
	tests := []struct {
		user string
		api  string
		want string
	}{
		{"hourly", "60", "hourly"},
		{"60", "60", "60"},
		{"", "60", "hourly"},
		{"daily", "1440", "daily"},
		{"", "manual", "manual"},
		{"hourly", "", "hourly"},
	}

	for _, tc := range tests {
		got := ReplicationFrequencyForState(tc.user, tc.api)
		require.Equal(t, tc.want, got, "ReplicationFrequencyForState(%q, %q)", tc.user, tc.api)
	}
}
