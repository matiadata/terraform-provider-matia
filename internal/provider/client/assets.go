package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

const assetsPath = "assets"

// AssetsClient handles Matia asset API calls.
type AssetsClient struct {
	client *MatiaClient
}

// AssetOwner is the populated owner object returned on asset reads/creates.
type AssetOwner struct {
	ID string `json:"_id"`
}

// Asset mirrors the fields returned by the Matia assets API that the provider uses.
type Asset struct {
	ID             string         `json:"_id"`
	AltID          string         `json:"id"`
	Name           string         `json:"name"`
	Description    string         `json:"description"`
	Type           string         `json:"type"`
	IsConnected    bool           `json:"isConnected"`
	IsDraft        bool           `json:"isDraft"`
	ConnectionID   string         `json:"connectionId"`
	ConnectionType string         `json:"connectionType"`
	AuthMethod     string         `json:"authMethod,omitempty"`
	Connection     map[string]any `json:"connection,omitempty"`
	Owners         []AssetOwner   `json:"owners"`
	CreatedAt      string         `json:"createdAt"`
	UpdatedAt      string         `json:"updatedAt"`
}

func (a *Asset) AssetID() string {
	if a.ID != "" {
		return a.ID
	}
	return a.AltID
}

func (a *Asset) OwnerIDs() []string {
	ids := make([]string, len(a.Owners))
	for i, owner := range a.Owners {
		ids[i] = owner.ID
	}
	return ids
}

// CreateAssetRequest is the POST /v1/assets body for sources and destinations.
type CreateAssetRequest struct {
	Name           string         `json:"name"`
	Type           string         `json:"type"`
	Description    string         `json:"description,omitempty"`
	Connection     map[string]any `json:"connection"`
	ConnectionType string         `json:"connectionType"`
	Owners         []string       `json:"owners"`
	AuthMethod     string         `json:"authMethod,omitempty"`
	Tags           []string       `json:"tags,omitempty"`
}

// UpdateAssetRequest is the PATCH /v1/assets/:id body.
type UpdateAssetRequest struct {
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	Connection  map[string]any `json:"connection,omitempty"`
	AuthMethod  string         `json:"authMethod,omitempty"`
	Owners      []string       `json:"owners,omitempty"`
}

func (c *AssetsClient) Create(ctx context.Context, req CreateAssetRequest) (*Asset, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	url, err := c.client.apiURL(assetsPath)
	if err != nil {
		return nil, err
	}
	tflog.Debug(ctx, "Matia API create asset request", map[string]any{
		"method":         "POST",
		"url":            url,
		"connectionType": req.ConnectionType,
		"body_bytes":     len(body),
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

	created, err := c.decodeAssetResponse(resp)
	if err != nil {
		return nil, err
	}

	asset, err := c.Get(ctx, created.AssetID())
	if err != nil {
		return nil, createdButUnreadable("asset", created.AssetID(), "Remove it before retrying.", err)
	}
	return asset, nil
}

func (c *AssetsClient) Get(ctx context.Context, id string) (*Asset, error) {
	url, err := c.client.apiURL(assetsPath, id)
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

	return c.decodeAssetResponse(resp)
}

func (c *AssetsClient) GetSource(ctx context.Context, id string) (*Asset, error) {
	return c.Get(ctx, id)
}

func (c *AssetsClient) GetDestination(ctx context.Context, id string) (*Asset, error) {
	return c.Get(ctx, id)
}

func (c *AssetsClient) Update(ctx context.Context, id string, req UpdateAssetRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}

	url, err := c.client.apiURL(assetsPath, id)
	if err != nil {
		return err
	}
	tflog.Debug(ctx, "Matia API update asset request", map[string]any{
		"method":     "PATCH",
		"url":        url,
		"body_bytes": len(body),
	})

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		return err
	}

	resp, err := c.client.doRequest(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return decodeAssetEmptyResponse(resp)
}

func (c *AssetsClient) Delete(ctx context.Context, id string) error {
	url, err := c.client.apiURL(assetsPath, id)
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
			if _, getErr := c.Get(ctx, id); errors.Is(getErr, ErrAssetNotFound) {
				return nil
			}
		}
		return err
	}
	defer resp.Body.Close()

	return decodeAssetEmptyResponse(resp)
}

func decodeAssetEmptyResponse(resp *http.Response) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode == http.StatusNotFound {
		return ErrAssetNotFound
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return apiErrorFromResponse(resp.StatusCode, body)
	}

	return nil
}

func (c *AssetsClient) decodeAssetResponse(resp *http.Response) (*Asset, error) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrAssetNotFound
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var v1 struct {
		Code string `json:"code"`
		Data Asset  `json:"data"`
	}
	if unmarshalErr := json.Unmarshal(body, &v1); unmarshalErr == nil && v1.Data.AssetID() != "" {
		return &v1.Data, nil
	}

	if apiErr, ok := errorFromInternalAPIEnvelope(body); ok {
		return nil, apiErr
	}

	var asset Asset
	if unmarshalErr := json.Unmarshal(body, &asset); unmarshalErr != nil {
		return nil, unmarshalErr
	}

	if asset.AssetID() == "" {
		return nil, fmt.Errorf("API response missing asset id: %s", string(body))
	}

	return &asset, nil
}
