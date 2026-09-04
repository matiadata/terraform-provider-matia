package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

const (
	integrationsPath       = "integrations"
	integrationSchemasPath = "schemas"
)

// IntegrationsClient handles Matia integration API calls.
type IntegrationsClient struct {
	client *MatiaClient
}

type IntegrationEndpoint struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// Integration mirrors GET /v1/integrations/:id.
type Integration struct {
	ID                   string              `json:"id"`
	Name                 string              `json:"name"`
	Paused               bool                `json:"paused"`
	Source               IntegrationEndpoint `json:"source"`
	Destination          IntegrationEndpoint `json:"destination"`
	CreatedAt            string              `json:"createdAt,omitempty"`
	ReplicationFrequency string              `json:"replicationFrequency,omitempty"`
	CronExpression       string              `json:"cronExpression,omitempty"`
	BaseTime             string              `json:"baseTime,omitempty"`
	DestinationSchema    string              `json:"destinationSchema,omitempty"`
	OnSchemaUpdate       string              `json:"onSchemaUpdate,omitempty"`
	AgentID              *string             `json:"agentId"`
}

// CreateIntegrationRequest is the POST /v1/integrations body.
type CreateIntegrationRequest struct {
	Name                 string         `json:"name,omitempty"`
	SourceID             string         `json:"sourceId"`
	DestinationID        string         `json:"destinationId"`
	ReplicationFrequency string         `json:"replicationFrequency"`
	DestinationSchema    string         `json:"destinationSchema"`
	SourceSettings       map[string]any `json:"sourceSettings,omitempty"`
	DestinationSettings  map[string]any `json:"destinationSettings,omitempty"`
	BaseTime             string         `json:"baseTime,omitempty"`
	CronExpression       string         `json:"cronExpression,omitempty"`
	OnSchemaUpdate       string         `json:"onSchemaUpdate,omitempty"`
	Enabled              *bool          `json:"enabled,omitempty"`
	Tags                 []string       `json:"tags,omitempty"`
	AgentID              string         `json:"agentId,omitempty"`
}

type createIntegrationResponse struct {
	ID string `json:"id"`
}

// ModifyIntegrationRequest is the PATCH /v1/integrations/:id body.
type ModifyIntegrationRequest struct {
	Name                 string         `json:"name,omitempty"`
	Paused               *bool          `json:"paused,omitempty"`
	SourceSettings       map[string]any `json:"sourceSettings,omitempty"`
	ReplicationFrequency string         `json:"replicationFrequency,omitempty"`
	CronExpression       string         `json:"cronExpression,omitempty"`
	BaseTime             string         `json:"baseTime,omitempty"`
	DestinationSchema    string         `json:"destinationSchema,omitempty"`
	OnSchemaUpdate       string         `json:"onSchemaUpdate,omitempty"`
	// Tri-state: nil omits agentId, a string sets it, and (*string)(nil) sends agentId: null.
	AgentID any `json:"agentId,omitempty"`
}

func (c *IntegrationsClient) Create(ctx context.Context, req CreateIntegrationRequest) (*Integration, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	url, err := c.client.apiURL(integrationsPath)
	if err != nil {
		return nil, err
	}
	tflog.Debug(ctx, "Matia API create integration request", map[string]any{
		"method":     "POST",
		"url":        url,
		"body_bytes": len(body),
	})

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	resp, err := c.client.doRequest(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := decodeV1DataResponse[createIntegrationResponse](resp, ErrIntegrationNotFound)
	if err != nil {
		return nil, err
	}

	integration, err := c.Get(ctx, data.ID)
	if err != nil {
		return nil, createdButUnreadable("integration", data.ID, "Import it into state instead.", err)
	}
	return integration, nil
}

func (c *IntegrationsClient) Get(ctx context.Context, id string) (*Integration, error) {
	url, err := c.client.apiURL(integrationsPath, id)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.client.doRequest(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	integration, err := decodeV1DataResponse[Integration](resp, ErrIntegrationNotFound)
	if err != nil {
		return nil, err
	}

	if integration.ID == "" {
		return nil, errors.New("API response missing integration id")
	}

	return &integration, nil
}

func (c *IntegrationsClient) Delete(ctx context.Context, id string) error {
	url, err := c.client.apiURL(integrationsPath, id)
	if err != nil {
		return err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}

	resp, err := c.client.doRequest(httpReq)
	if err != nil {
		if isHTTPTimeoutError(err) {
			if _, getErr := c.Get(ctx, id); errors.Is(getErr, ErrIntegrationNotFound) {
				return nil
			}
		}
		return err
	}
	defer resp.Body.Close()

	return decodeV1EmptyResponse(resp, ErrIntegrationNotFound)
}

func (c *IntegrationsClient) Modify(ctx context.Context, id string, req ModifyIntegrationRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}

	url, err := c.client.apiURL(integrationsPath, id)
	if err != nil {
		return err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		return err
	}

	resp, err := c.client.doRequest(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return decodeV1EmptyResponse(resp, ErrIntegrationNotFound)
}

func (c *IntegrationsClient) ModifyWithRetry(ctx context.Context, id string, req ModifyIntegrationRequest) error {
	return c.retryWhileRetryableIntegrationError(
		ctx,
		isRetryableIntegrationOperationError,
		"integration to finish creation",
		func(attemptCtx context.Context) error {
			return c.Modify(attemptCtx, id, req)
		},
	)
}

func (c *IntegrationsClient) GetSchemaConfig(ctx context.Context, id string) (string, error) {
	url, err := c.client.apiURL(integrationsPath, id, integrationSchemasPath)
	if err != nil {
		return "", err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}

	resp, err := c.client.doRequest(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	data, err := decodeV1DataResponse[map[string]any](resp, ErrIntegrationNotFound)
	if err != nil {
		return "", err
	}

	if data == nil {
		return "{}", nil
	}

	encoded, err := json.Marshal(data)
	if err != nil {
		return "", err
	}

	return string(encoded), nil
}

func (c *IntegrationsClient) UpdateSchemaConfig(
	ctx context.Context,
	id string,
	config json.RawMessage,
) (string, error) {
	var updated string
	err := c.retryWhileRetryableIntegrationError(
		ctx,
		isRetryableIntegrationOperationError,
		"integration to finish creation",
		func(attemptCtx context.Context) error {
			var err error
			updated, err = c.updateSchemaConfigOnce(attemptCtx, id, config)
			return err
		},
	)
	return updated, err
}

func (c *IntegrationsClient) updateSchemaConfigOnce(
	ctx context.Context,
	id string,
	config json.RawMessage,
) (string, error) {
	url, err := c.client.apiURL(integrationsPath, id, integrationSchemasPath)
	if err != nil {
		return "", err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(config))
	if err != nil {
		return "", err
	}

	resp, err := c.client.doRequest(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	data, err := decodeV1DataResponse[map[string]any](resp, ErrIntegrationNotFound)
	if err != nil {
		return "", err
	}

	if data == nil {
		return "{}", nil
	}

	encoded, err := json.Marshal(data)
	if err != nil {
		return "", err
	}

	return string(encoded), nil
}
