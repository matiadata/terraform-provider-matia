package client

import (
	"errors"
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

	// Ceiling on a server-supplied Retry-After, so an oversized value cannot stall an
	// apply for minutes while Terraform holds the resource lock.
	maxRetryAfterDelay = 30 * time.Second
)

// MatiaClient is the API client shared across all resources.
type MatiaClient struct {
	BaseURL                string
	APIKey                 string
	HTTPClient             *http.Client
	Assets                 *AssetsClient
	Integrations           *IntegrationsClient
	HybridDeploymentAgents *HybridDeploymentAgentsClient

	retryBaseDelay                     time.Duration
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
		retryBaseDelay:                     requestRetryBaseDelay,
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

	var retryDelay time.Duration
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			if err := resetRequestBody(req); err != nil {
				return nil, err
			}
			time.Sleep(retryDelay)
		}
		finalAttempt := attempt == requestRetryMaxAttempts-1

		resp, err := c.HTTPClient.Do(req)
		if err != nil {
			if finalAttempt || !shouldRetryNetworkError(req.Method, err) {
				return nil, err
			}
			retryDelay = c.backoffDelay(attempt + 1)
			continue
		}

		if finalAttempt || !shouldRetryStatus(req.Method, resp.StatusCode) {
			return resp, nil
		}

		retryDelay = c.retryDelayFor(resp, attempt+1)
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
}

func (c *MatiaClient) backoffDelay(attempt int) time.Duration {
	return c.retryBaseDelay * time.Duration(1<<(attempt-1))
}

// retryDelayFor prefers the server's own Retry-After over local backoff, so a throttled
// client waits as long as the limiter actually asked for.
func (c *MatiaClient) retryDelayFor(resp *http.Response, attempt int) time.Duration {
	if delay, ok := parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()); ok {
		return min(delay, maxRetryAfterDelay)
	}
	return c.backoffDelay(attempt)
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
