package provider

import (
	"strings"

	"github.com/matiadata/terraform-provider-matia/internal/provider/client"
)

// ReplicationFrequencyForState keeps the user's alias (e.g. hourly) when it matches the API value (e.g. 60).
func ReplicationFrequencyForState(userValue, apiValue string) string {
	if apiValue == "" {
		return userValue
	}
	if userValue != "" {
		resolved, err := client.ResolveReplicationFrequency(userValue)
		if err == nil && resolved == apiValue {
			return userValue
		}
	}
	return reverseReplicationFrequency(apiValue)
}

func reverseReplicationFrequency(apiValue string) string {
	apiValue = strings.TrimSpace(apiValue)
	for alias, mapped := range client.ReplicationFrequencyAliases {
		if mapped == apiValue {
			return alias
		}
	}
	return apiValue
}
