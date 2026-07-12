package client

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

var ErrAssetNotFound = errors.New("asset not found")

const (
	defaultHTTPTimeout = 5 * time.Minute

	requestRetryMaxAttempts = 3
	requestRetryBaseDelay   = 500 * time.Millisecond
)

// MatiaClient is the API client shared across all resources.
type MatiaClient struct {
	BaseURL                string
	APIKey                 string
	HTTPClient             *http.Client
	Assets                 *AssetsClient
	Integrations           *IntegrationsClient
	HybridDeploymentAgents *HybridDeploymentAgentsClient

	pollInterval                       time.Duration
	integrationOperationTimeout        time.Duration
	integrationOperationAttemptTimeout time.Duration
}

func NewMatiaClient(baseURL, apiKey string) *MatiaClient {
	c := &MatiaClient{
		BaseURL: baseURL,
		APIKey:  apiKey,
		HTTPClient: &http.Client{
			Timeout: defaultHTTPTimeout,
		},
		pollInterval:                       defaultPollInterval,
		integrationOperationTimeout:        defaultIntegrationOperationTimeout,
		integrationOperationAttemptTimeout: defaultIntegrationOperationAttemptTimeout,
	}
	c.Assets = &AssetsClient{client: c}
	c.Integrations = &IntegrationsClient{client: c}
	c.HybridDeploymentAgents = &HybridDeploymentAgentsClient{client: c}
	return c
}

func (c *MatiaClient) apiURL(segments ...string) (string, error) {
	return url.JoinPath(c.BaseURL, segments...)
}

func (c *MatiaClient) doRequest(req *http.Request) (*http.Response, error) {
	req.Header.Set("X-Api-Key", c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	var lastErr error
	for attempt := range requestRetryMaxAttempts {
		if attempt > 0 {
			if err := resetRequestBody(req); err != nil {
				return nil, err
			}
			time.Sleep(requestRetryDelay(attempt))
		}

		resp, err := c.HTTPClient.Do(req)
		if err != nil {
			if isTransientNetworkError(err) && attempt < requestRetryMaxAttempts-1 {
				lastErr = err
				continue
			}
			return nil, err
		}

		if isRetryableHTTPStatus(resp.StatusCode) && attempt < requestRetryMaxAttempts-1 {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("API request failed with status %d", resp.StatusCode)
			continue
		}

		return resp, nil
	}

	return nil, lastErr
}

func requestRetryDelay(attempt int) time.Duration {
	return requestRetryBaseDelay * time.Duration(1<<(attempt-1))
}

func resetRequestBody(req *http.Request) error {
	if req.Body == nil || req.GetBody == nil {
		return nil
	}

	body, err := req.GetBody()
	if err != nil {
		return err
	}
	req.Body = body
	return nil
}
