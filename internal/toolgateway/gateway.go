package toolgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type HTTPGateway struct{ client *http.Client }

func NewHTTPGateway() *HTTPGateway {
	return &HTTPGateway{client: &http.Client{Timeout: 60 * time.Second}}
}

// ListCapabilities returns executor facts from the Sandbox capability server.
// These are not Hub ToolDefinitions and must never be exposed to an Agent
// unless Runtime has independently selected a matching ToolVersion.
func (g *HTTPGateway) ListCapabilities(ctx context.Context, endpoint string) (map[string]interface{}, error) {
	return g.get(ctx, endpoint, "/v1/capabilities", "list capabilities")
}

// CallCapability invokes one low-level Sandbox executor capability. Business
// IAM, approval and credential delegation are Runtime Tool Broker concerns and
// must happen before the manager receives this request.
func (g *HTTPGateway) CallCapability(ctx context.Context, endpoint string, reqBody map[string]interface{}) (map[string]interface{}, error) {
	return g.post(ctx, endpoint, "/v1/capabilities/call", reqBody, "call capability")
}

// ListTools is the pre-V1 debug proxy. Keep it only while Runtime and existing
// clients migrate to ListCapabilities.
func (g *HTTPGateway) ListTools(ctx context.Context, endpoint string) (map[string]interface{}, error) {
	return g.get(ctx, endpoint, "/v1/tools", "list legacy tools")
}

// Call is the pre-V1 debug proxy. New execution paths use CallCapability.
func (g *HTTPGateway) Call(ctx context.Context, endpoint string, reqBody map[string]interface{}) (map[string]interface{}, error) {
	return g.post(ctx, endpoint, "/v1/tools/call", reqBody, "call legacy tool")
}

func (g *HTTPGateway) get(ctx context.Context, endpoint, path, operation string) (map[string]interface{}, error) {
	endpoint = strings.TrimRight(endpoint, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("sandbox capability server %s http %d: %s", operation, resp.StatusCode, string(b))
	}
	var out map[string]interface{}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (g *HTTPGateway) post(ctx context.Context, endpoint, path string, reqBody map[string]interface{}, operation string) (map[string]interface{}, error) {
	endpoint = strings.TrimRight(endpoint, "/") + path
	b, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("sandbox capability server %s http %d: %s", operation, resp.StatusCode, string(rb))
	}
	var out map[string]interface{}
	if err := json.Unmarshal(rb, &out); err != nil {
		return nil, err
	}
	return out, nil
}
