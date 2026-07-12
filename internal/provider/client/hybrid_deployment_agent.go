package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

const hybridDeploymentAgentsPath = "agent-gateway/hybrid-deployment-agents"

var ErrHybridDeploymentAgentNotFound = errors.New("hybrid deployment agent not found")

// HybridDeploymentAgent mirrors GET /v1/agent-gateway/hybrid-deployment-agents/:agentId.
type HybridDeploymentAgent struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	CreatedAt   string `json:"createdAt,omitempty"`
	Token       string `json:"-"`
}

// CreateHybridDeploymentAgentRequest is the POST /v1/agent-gateway/hybrid-deployment-agents body.
type CreateHybridDeploymentAgentRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type createHybridDeploymentAgentResponse struct {
	ID    string `json:"id"`
	Token string `json:"token,omitempty"`
}

func (a *HybridDeploymentAgent) AgentID() string {
	return a.ID
}

type HybridDeploymentAgentsClient struct {
	client *MatiaClient
}

func (c *HybridDeploymentAgentsClient) Create(
	ctx context.Context,
	req CreateHybridDeploymentAgentRequest,
) (*HybridDeploymentAgent, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	url, err := c.client.apiURL(hybridDeploymentAgentsPath)
	if err != nil {
		return nil, err
	}
	tflog.Debug(ctx, "Matia API create hybrid deployment agent request", map[string]any{
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

	data, err := decodeV1DataResponse[createHybridDeploymentAgentResponse](resp, ErrHybridDeploymentAgentNotFound)
	if err != nil {
		return nil, err
	}

	agent, err := c.Get(ctx, data.ID)
	if err != nil {
		return nil, err
	}

	if data.Token != "" {
		agent.Token = data.Token
	}

	return agent, nil
}

func (c *HybridDeploymentAgentsClient) Get(ctx context.Context, agentID string) (*HybridDeploymentAgent, error) {
	url, err := c.client.apiURL(hybridDeploymentAgentsPath, agentID)
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

	agent, err := decodeV1DataResponse[HybridDeploymentAgent](resp, ErrHybridDeploymentAgentNotFound)
	if err != nil {
		return nil, err
	}

	if agent.AgentID() == "" {
		return nil, errors.New("API response missing hybrid deployment agent id")
	}

	return &agent, nil
}

func (c *HybridDeploymentAgentsClient) Delete(ctx context.Context, agentID string) error {
	url, err := c.client.apiURL(hybridDeploymentAgentsPath, agentID)
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
			if _, getErr := c.Get(ctx, agentID); errors.Is(getErr, ErrHybridDeploymentAgentNotFound) {
				return nil
			}
		}
		return err
	}
	defer resp.Body.Close()

	return decodeV1EmptyResponse(resp, ErrHybridDeploymentAgentNotFound)
}
