package netbird

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func NewClient(baseURL, token string, timeout time.Duration) *Client {
	return &Client{
		baseURL: baseURL,
		token:   token,
		http:    &http.Client{Timeout: timeout},
	}
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal: %w", err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Authorization", "Token "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		buf, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("netbird api %s %s: %d %s", method, path, resp.StatusCode, string(buf))
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}

func (c *Client) ListServices(ctx context.Context) ([]Service, error) {
	var out []Service
	if err := c.do(ctx, http.MethodGet, "/api/reverse-proxies/services", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) CreateService(ctx context.Context, s *Service) (*Service, error) {
	var out Service
	if err := c.do(ctx, http.MethodPost, "/api/reverse-proxies/services", s, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpdateService(ctx context.Context, id string, s *Service) (*Service, error) {
	var out Service
	if err := c.do(ctx, http.MethodPut, "/api/reverse-proxies/services/"+id, s, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DeleteService(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/api/reverse-proxies/services/"+id, nil, nil)
}

func (c *Client) GetService(ctx context.Context, id string) (*Service, error) {
	var out Service
	if err := c.do(ctx, http.MethodGet, "/api/reverse-proxies/services/"+id, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- lookup endpoints used by the name resolver ---

func (c *Client) ListPeers(ctx context.Context) ([]Peer, error) {
	var out []Peer
	if err := c.do(ctx, http.MethodGet, "/api/peers", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) ListNetworks(ctx context.Context) ([]Network, error) {
	var out []Network
	if err := c.do(ctx, http.MethodGet, "/api/networks", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) ListNetworkResources(ctx context.Context, networkID string) ([]NetworkResource, error) {
	var out []NetworkResource
	if err := c.do(ctx, http.MethodGet, "/api/networks/"+networkID+"/resources", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) ListProxyClusters(ctx context.Context) ([]ProxyCluster, error) {
	var out []ProxyCluster
	if err := c.do(ctx, http.MethodGet, "/api/reverse-proxies/clusters", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) ListGroups(ctx context.Context) ([]Group, error) {
	var out []Group
	if err := c.do(ctx, http.MethodGet, "/api/groups", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// --- network / resource / router provisioning ---

func (c *Client) CreateNetwork(ctx context.Context, body *NetworkCreate) (*Network, error) {
	var out Network
	if err := c.do(ctx, http.MethodPost, "/api/networks", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) CreateNetworkResource(ctx context.Context, networkID string, res *NetworkResource) (*NetworkResource, error) {
	var out NetworkResource
	if err := c.do(ctx, http.MethodPost, "/api/networks/"+networkID+"/resources", res, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpdateNetworkResource(ctx context.Context, networkID, resourceID string, res *NetworkResource) (*NetworkResource, error) {
	var out NetworkResource
	if err := c.do(ctx, http.MethodPut, "/api/networks/"+networkID+"/resources/"+resourceID, res, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ListNetworkRouters(ctx context.Context, networkID string) ([]NetworkRouter, error) {
	var out []NetworkRouter
	if err := c.do(ctx, http.MethodGet, "/api/networks/"+networkID+"/routers", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) CreateNetworkRouter(ctx context.Context, networkID string, r *NetworkRouter) (*NetworkRouter, error) {
	var out NetworkRouter
	if err := c.do(ctx, http.MethodPost, "/api/networks/"+networkID+"/routers", r, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpdateNetworkRouter(ctx context.Context, networkID, routerID string, r *NetworkRouter) (*NetworkRouter, error) {
	var out NetworkRouter
	if err := c.do(ctx, http.MethodPut, "/api/networks/"+networkID+"/routers/"+routerID, r, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
