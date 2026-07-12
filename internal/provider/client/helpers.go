package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
	"strings"
)

func isHTTPTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func isTransientNetworkError(err error) bool {
	if err == nil {
		return false
	}
	if isHTTPTimeoutError(err) {
		return true
	}
	if errors.Is(err, io.EOF) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "tls handshake timeout")
}

func isRetryableHTTPStatus(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests || statusCode >= http.StatusInternalServerError
}

var (
	ErrIntegrationNotFound = errors.New("integration not found")
)

const (
	v1ErrorCodeIntegrationNotFound = "NotFound_Integration"
	v1ErrorCodeSchemaNotFound      = "NotFound_SchemaConfig"
	v1ErrorCodeTableNotFound       = "NotFound_TableConfig"
)

var ReplicationFrequencyAliases = map[string]string{
	"manual": "manual",
	"hourly": "60",
	"daily":  "1440",
	"cron":   "cron",
}

// ResolveReplicationFrequency maps human-friendly schedule values to Matia API enum values.
func ResolveReplicationFrequency(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("replication_frequency must not be empty")
	}
	if mapped, ok := ReplicationFrequencyAliases[strings.ToLower(value)]; ok {
		return mapped, nil
	}
	return value, nil
}

func decodeV1DataResponse[T any](resp *http.Response, notFoundErr error) (T, error) {
	var zero T

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return zero, err
	}

	if resp.StatusCode == http.StatusNotFound {
		return zero, errorFromV1NotFound(body, notFoundErr)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return zero, apiErrorFromResponse(resp.StatusCode, body)
	}

	var v1 struct {
		Code string `json:"code"`
		Data T      `json:"data"`
	}
	if unmarshalErr := json.Unmarshal(body, &v1); unmarshalErr == nil {
		if v1DataUsable(v1.Data) {
			return v1.Data, nil
		}
		if v1.Code != "" {
			if softErr := errorFromV1SoftErrorEnvelope(body, notFoundErr); softErr != nil {
				return zero, softErr
			}
			return zero, errors.New("API response missing data")
		}
	}

	if apiErr, ok := errorFromInternalAPIEnvelope(body); ok {
		return zero, apiErr
	}

	if unmarshalErr := json.Unmarshal(body, &zero); unmarshalErr != nil {
		return zero, unmarshalErr
	}

	if !v1DataUsable(zero) {
		return zero, errors.New("API response missing data")
	}

	return zero, nil
}

func v1DataUsable[T any](data T) bool {
	value := reflect.ValueOf(data)
	if value.Kind() == reflect.Map {
		return true
	}
	return !value.IsZero()
}

// errorFromV1SoftErrorEnvelope handles agent-gateway style responses that return
// HTTP 200 with {"code":"error",...} instead of a non-2xx status.
// Returns nil when the body is not a soft-error envelope.
func errorFromV1SoftErrorEnvelope(body []byte, notFoundErr error) error {
	var envelope struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Error   *struct {
			Code string `json:"code"`
			Name string `json:"name"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && strings.EqualFold(envelope.Code, "error") {
		nestedCode := ""
		nestedName := ""
		if envelope.Error != nil {
			nestedCode = envelope.Error.Code
			nestedName = envelope.Error.Name
		}

		if nestedCode == "AGENT_NOT_FOUND" || nestedName == "AgentNotFoundError" {
			return notFoundErr
		}

		if envelope.Message != "" {
			return fmt.Errorf("API request failed: %s", envelope.Message)
		}
		return fmt.Errorf("API request failed: %s", string(body))
	}
	return nil
}

type internalAPIEnvelope struct {
	OK    *bool  `json:"ok"`
	Error string `json:"error"`
}

func errorFromInternalAPIEnvelope(body []byte) (error, bool) {
	var envelope internalAPIEnvelope
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.OK != nil && !*envelope.OK {
		if envelope.Error != "" {
			return fmt.Errorf("API request failed: %s", envelope.Error), true
		}
		return fmt.Errorf("API request failed: %s", string(body)), true
	}
	return nil, false
}

func decodeV1EmptyResponse(resp *http.Response, notFoundErr error) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode == http.StatusNotFound {
		return errorFromV1NotFound(body, notFoundErr)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return apiErrorFromResponse(resp.StatusCode, body)
	}

	return nil
}

func parseV1ErrorCode(body []byte) string {
	apiErr := parseMatiaAPIError(body)
	if apiErr.Code != "" {
		return apiErr.Code
	}

	var nested struct {
		Message struct {
			Code string `json:"code"`
		} `json:"message"`
	}
	if err := json.Unmarshal(body, &nested); err == nil && nested.Message.Code != "" {
		return nested.Message.Code
	}

	raw := string(body)
	switch {
	case strings.Contains(raw, v1ErrorCodeSchemaNotFound):
		return v1ErrorCodeSchemaNotFound
	case strings.Contains(raw, v1ErrorCodeTableNotFound):
		return v1ErrorCodeTableNotFound
	case strings.Contains(raw, v1ErrorCodeIntegrationNotFound):
		return v1ErrorCodeIntegrationNotFound
	}

	return ""
}

func errorFromV1NotFound(body []byte, notFoundErr error) error {
	code := parseV1ErrorCode(body)
	apiErr := parseMatiaAPIError(body)
	message := apiErr.Message
	if message == "" {
		var nested struct {
			Message struct {
				Message string `json:"message"`
			} `json:"message"`
		}
		if err := json.Unmarshal(body, &nested); err == nil && nested.Message.Message != "" {
			message = nested.Message.Message
		}
	}
	if message == "" {
		message = strings.TrimSpace(string(body))
	}

	switch code {
	case v1ErrorCodeIntegrationNotFound:
		return notFoundErr
	case v1ErrorCodeSchemaNotFound:
		return fmt.Errorf(
			"schema not found in integration catalog: %s (run GET /integrations/{id}/schemas to see discovered schemas)",
			message,
		)
	case v1ErrorCodeTableNotFound:
		return fmt.Errorf(
			"table not found in integration catalog: %s (run GET /integrations/{id}/schemas to see discovered tables)",
			message,
		)
	default:
		if strings.Contains(message, "SchemaConfig") {
			return fmt.Errorf(
				"schema not found in integration catalog: %s (run GET /integrations/{id}/schemas to see discovered schemas)",
				message,
			)
		}
		if strings.Contains(message, "TableConfig") {
			return fmt.Errorf(
				"table not found in integration catalog: %s (run GET /integrations/{id}/schemas to see discovered tables)",
				message,
			)
		}
		if code != "" {
			return fmt.Errorf("API request failed with status 404 (%s): %s", code, message)
		}
		return notFoundErr
	}
}
