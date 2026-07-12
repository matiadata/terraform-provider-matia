package client

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScheduleMatchesPlan(t *testing.T) {
	plan := SchedulePlan{
		ReplicationFrequency: "hourly",
	}

	require.True(t, scheduleMatchesPlan(plan, "60", &Integration{ReplicationFrequency: "60"}))
	require.False(t, scheduleMatchesPlan(plan, "60", &Integration{ReplicationFrequency: "manual"}))

	manualPlan := SchedulePlan{
		ReplicationFrequency: "manual",
	}
	require.True(t, scheduleMatchesPlan(manualPlan, "manual", &Integration{ReplicationFrequency: "manual"}))
	require.True(t, scheduleMatchesPlan(manualPlan, "manual", &Integration{ReplicationFrequency: ""}))
}

func TestAPIErrorFromResponse_IntegrationInCreation(t *testing.T) {
	body := []byte(`{"code":"IntegrationInCreation","message":"Integration is in creation. Please try again later."}`)
	err := apiErrorFromResponse(409, body)
	require.ErrorIs(t, err, ErrIntegrationInCreation)
}

func TestIsIntegrationInCreationError(t *testing.T) {
	require.True(t, isIntegrationInCreationError(ErrIntegrationInCreation))
	require.True(t, isIntegrationInCreationError(
		errors.New(`API request failed with status 409: {"code":"IntegrationInCreation"}`),
	))
}
