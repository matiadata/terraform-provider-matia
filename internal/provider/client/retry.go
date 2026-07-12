package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrIntegrationInCreation = errors.New("integration is in creation")
	ErrIntegrationFailed     = errors.New("integration creation failed")
)

const (
	integrationReadyTimeout = 5 * time.Minute
	scheduleAppliedTimeout  = 2 * time.Minute
)

const (
	defaultIntegrationOperationTimeout        = 5 * time.Minute
	defaultPollInterval                       = 2 * time.Second
	defaultIntegrationOperationAttemptTimeout = 30 * time.Second
)

type matiaAPIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func parseMatiaAPIError(body []byte) matiaAPIError {
	var errPayload matiaAPIError
	if json.Unmarshal(body, &errPayload) == nil && errPayload.Code != "" {
		return errPayload
	}

	var wrapped struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &wrapped) == nil && wrapped.Code != "" {
		return wrapped
	}

	return matiaAPIError{}
}

func apiErrorFromResponse(statusCode int, body []byte) error {
	apiErr := parseMatiaAPIError(body)
	if apiErr.Code == "" {
		return fmt.Errorf("API request failed with status %d: %s", statusCode, string(body))
	}

	switch apiErr.Code {
	case "IntegrationInCreation":
		return ErrIntegrationInCreation
	case "IntegrationFailed":
		return fmt.Errorf("%w: %s", ErrIntegrationFailed, apiErr.Message)
	default:
		return fmt.Errorf("API request failed with status %d: %s", statusCode, string(body))
	}
}

func isIntegrationInCreationError(err error) bool {
	return errors.Is(err, ErrIntegrationInCreation) ||
		strings.Contains(err.Error(), "IntegrationInCreation")
}

func isRetryableIntegrationOperationError(err error) bool {
	return isIntegrationInCreationError(err) || isHTTPTimeoutError(err)
}

// SchedulePlan is the expected schedule state used when polling for PATCH to apply.
type SchedulePlan struct {
	ReplicationFrequency string
	CronExpression       string
	BaseTime             string
}

func (c *IntegrationsClient) WaitForIntegrationReady(ctx context.Context, integrationID string) error {
	var lastErr error
	waitReason := fmt.Sprintf("integration %q to finish creation", integrationID)
	return c.client.pollUntil(
		ctx,
		integrationReadyTimeout,
		waitReason,
		func() (bool, error) {
			err := c.probeIntegrationReady(ctx, integrationID)
			if err == nil {
				return true, nil
			}
			if !isIntegrationInCreationError(err) {
				return false, err
			}
			lastErr = err
			return false, nil
		},
		func(err error) error {
			if errors.Is(err, context.DeadlineExceeded) && lastErr != nil {
				return fmt.Errorf(
					"timed out after %s waiting for %s: %w",
					integrationReadyTimeout,
					waitReason,
					lastErr,
				)
			}
			if errors.Is(err, context.DeadlineExceeded) {
				return fmt.Errorf("timed out after %s waiting for %s", integrationReadyTimeout, waitReason)
			}
			return fmt.Errorf("context canceled while waiting for %s: %w", waitReason, err)
		},
	)
}

func (c *IntegrationsClient) probeIntegrationReady(ctx context.Context, integrationID string) error {
	_, err := c.Get(ctx, integrationID)
	return err
}

func (c *IntegrationsClient) WaitForScheduleApplied(ctx context.Context, connectionID string, plan SchedulePlan) error {
	expectedFrequency, err := ResolveReplicationFrequency(plan.ReplicationFrequency)
	if err != nil {
		return err
	}

	var lastIntegration *Integration
	waitReason := fmt.Sprintf("connection %q schedule to apply", connectionID)
	return c.client.pollUntil(
		ctx,
		scheduleAppliedTimeout,
		waitReason,
		func() (bool, error) {
			integration, getErr := c.Get(ctx, connectionID)
			if getErr != nil {
				return false, getErr
			}
			lastIntegration = integration
			return scheduleMatchesPlan(plan, expectedFrequency, integration), nil
		},
		func(err error) error {
			if errors.Is(err, context.DeadlineExceeded) && lastIntegration != nil {
				return fmt.Errorf(
					"timed out after %s waiting for connection %q schedule to apply: expected replication_frequency %q, API has %q",
					scheduleAppliedTimeout,
					connectionID,
					plan.ReplicationFrequency,
					lastIntegration.ReplicationFrequency,
				)
			}
			if errors.Is(err, context.DeadlineExceeded) {
				return fmt.Errorf("timed out after %s waiting for %s", scheduleAppliedTimeout, waitReason)
			}
			return fmt.Errorf("context canceled while waiting for %s: %w", waitReason, err)
		},
	)
}

func scheduleMatchesPlan(plan SchedulePlan, expectedFrequency string, integration *Integration) bool {
	if !replicationFrequencyMatches(expectedFrequency, integration.ReplicationFrequency) {
		return false
	}

	if plan.CronExpression != "" {
		if integration.CronExpression != plan.CronExpression {
			return false
		}
	}

	if plan.BaseTime != "" {
		if integration.BaseTime != plan.BaseTime {
			return false
		}
	}

	return true
}

func replicationFrequencyMatches(expectedFrequency, apiFrequency string) bool {
	if apiFrequency == expectedFrequency {
		return true
	}
	return expectedFrequency == "manual" && apiFrequency == ""
}

func (c *IntegrationsClient) retryWhileRetryableIntegrationError(
	ctx context.Context,
	isRetryable func(error) bool,
	waitReason string,
	operation func(context.Context) error,
) error {
	var lastErr error
	return c.client.pollUntil(
		ctx,
		c.client.integrationOperationTimeout,
		waitReason,
		func() (bool, error) {
			attemptCtx, cancel := context.WithTimeout(ctx, c.client.integrationOperationAttemptTimeout)
			defer cancel()

			err := operation(attemptCtx)
			if err == nil {
				return true, nil
			}
			if !isRetryable(err) {
				return false, err
			}
			lastErr = err
			return false, nil
		},
		func(err error) error {
			if errors.Is(err, context.DeadlineExceeded) && lastErr != nil {
				return fmt.Errorf(
					"timed out after %s waiting for %s: %w",
					c.client.integrationOperationTimeout,
					waitReason,
					lastErr,
				)
			}
			return err
		},
	)
}

func (c *MatiaClient) pollUntil(
	ctx context.Context,
	timeout time.Duration,
	waitReason string,
	attempt func() (done bool, err error),
	timeoutError ...func(error) error,
) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for {
		done, err := attempt()
		if err != nil {
			return err
		}
		if done {
			return nil
		}

		if pollErr := c.waitForNextPoll(ctx); pollErr != nil {
			if len(timeoutError) > 0 && timeoutError[0] != nil {
				return timeoutError[0](pollErr)
			}
			if errors.Is(pollErr, context.DeadlineExceeded) {
				return fmt.Errorf("timed out after %s waiting for %s", timeout, waitReason)
			}
			return fmt.Errorf("context canceled while waiting for %s: %w", waitReason, pollErr)
		}
	}
}

func (c *MatiaClient) waitForNextPoll(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(c.pollInterval):
		return nil
	}
}
