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
	"strconv"
	"strings"
	"syscall"
	"time"
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

	if requestNeverReachedServer(err) {
		return true
	}

	return errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE)
}

func isRetryableHTTPStatus(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests || statusCode >= http.StatusInternalServerError
}

// isIdempotentMethod reports whether replaying method can be trusted not to create a
// second resource.
//
// This is deliberately wider than net/http, which refuses to replay any body-carrying
// request without an Idempotency-Key: idempotent-per-RFC and safe-to-replay-under-
// concurrent-modification are different properties. Including PUT/DELETE/PATCH here
// rests on Terraform being the sole writer of the resources it manages, which is the
// assumption that breaks first - someone editing the same integration in the Matia
// console mid-apply.
//
// PATCH qualifies only because every Matia PATCH is an absolute field/config set, not
// an append/toggle; an append-style PATCH would have to be excluded so it is not
// silently retried on a 5xx.
func isIdempotentMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPut, http.MethodDelete, http.MethodPatch:
		return true
	default:
		return false
	}
}

// requestNeverReachedServer reports failures raised before any request byte reached the
// server, so replaying them cannot duplicate a server-side effect.
func requestNeverReachedServer(err error) bool {
	if err == nil {
		return false
	}
	// Dial-phase failures - connection refused, connect timeout, most DNS failures -
	// surface as an OpError with Op "dial"; the connection did not exist yet.
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return true
	}
	// A resolver failure can also arrive unwrapped, e.g. from a custom dialer.
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && (dnsErr.IsNotFound || dnsErr.IsTemporary) {
		return true
	}
	// No errors.Is target for a TLS handshake timeout, so match its message.
	return strings.Contains(strings.ToLower(err.Error()), "tls handshake timeout")
}

func shouldRetryNetworkError(method string, err error) bool {
	if requestNeverReachedServer(err) {
		return true
	}
	return isIdempotentMethod(method) && isTransientNetworkError(err)
}

func shouldRetryStatus(method string, statusCode int) bool {
	if !isRetryableHTTPStatus(statusCode) {
		return false
	}
	// Treating 429 as safe for any method assumes the request was rejected before the
	// handler ran, which holds for an edge or gateway limiter. An app-level limiter
	// that throttles after doing partial work would break this, and 429 would have to
	// move into the idempotent-only branch below.
	rejectedBeforeProcessing := statusCode == http.StatusTooManyRequests
	return rejectedBeforeProcessing || isIdempotentMethod(method)
}

// parseRetryAfter reads an RFC 7231 Retry-After value, which is either delay-seconds or
// an HTTP-date. The bool separates "no usable value" from a legitimate zero delay.
func parseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}

	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds < 0 {
			return 0, false
		}
		return time.Duration(seconds) * time.Second, true
	}

	date, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	if delay := date.Sub(now); delay > 0 {
		return delay, true
	}
	return 0, true
}

// createdButUnreadable reports a create the server accepted whose read-back failed.
// Terraform records no id for a failed create, so the id has to reach the user in the
// error text: a plain re-apply would create a second resource instead of adopting this
// one.
func createdButUnreadable(kind, id, recovery string, err error) error {
	return fmt.Errorf(
		"%s %q was created but could not be read back: %w; it exists on the server, "+
			"so re-applying would create a duplicate. %s",
		kind, id, err, recovery,
	)
}

var (
	ErrIntegrationNotFound = errors.New("integration not found")
)

const (
	v1ErrorCodeIntegrationNotFound = "NotFound_Integration"
	v1ErrorCodeSchemaNotFound      = "NotFound_SchemaConfig"
	v1ErrorCodeTableNotFound       = "NotFound_TableConfig"
)

// SchemaNotFoundMessage opens the error a schemas PATCH naming an unknown schema produces. The
// integration_schema resource quotes it when warning that a declared schema has left the catalog,
// so that warning and the failure it predicts stay worded alike; the API's own SCHEMA_NOT_FOUND
// code never reaches Terraform's output.
const SchemaNotFoundMessage = "schema not found in integration catalog"

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
			SchemaNotFoundMessage+": %s (run GET /integrations/{id}/schemas to see discovered schemas)",
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
				SchemaNotFoundMessage+": %s (run GET /integrations/{id}/schemas to see discovered schemas)",
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
