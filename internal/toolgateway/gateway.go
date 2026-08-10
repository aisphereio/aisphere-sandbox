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

// ListTools is a compatibility facade for the pre-V1 manager API. The executor
// source of truth is already /v1/capabilities; this method only reshapes the
// response for old Runtime clients that still decode a `tools` array.
func (g *HTTPGateway) ListTools(ctx context.Context, endpoint string) (map[string]interface{}, error) {
	out, err := g.ListCapabilities(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	capabilities, _ := out["capabilities"].([]interface{})
	tools := make([]interface{}, 0, len(capabilities))
	for _, item := range capabilities {
		capability, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		legacy := cloneMap(capability)
		if id, _ := capability["id"].(string); id != "" {
			legacy["name"] = id
		}
		tools = append(tools, legacy)
	}
	return map[string]interface{}{
		"contractVersion": out["contractVersion"],
		"tools":           tools,
		"capabilities":    capabilities,
	}, nil
}

// Call is a compatibility facade for the pre-V1 manager API. It translates
// the historical `tool` field into a low-level executor `capability` and then
// invokes Capability V1. No fallback to the legacy Pod endpoint is allowed.
func (g *HTTPGateway) Call(ctx context.Context, endpoint string, reqBody map[string]interface{}) (map[string]interface{}, error) {
	body := cloneMap(reqBody)
	if _, ok := body["capability"]; !ok {
		body["capability"] = firstNonEmptyString(body["tool"], body["name"])
	}
	if _, ok := body["context"]; !ok {
		body["context"] = legacyInvocationContext(body)
	}
	return g.CallCapability(ctx, endpoint, body)
}

func legacyInvocationContext(body map[string]interface{}) map[string]interface{} {
	context := map[string]interface{}{}
	copyIfPresent(context, "runId", body)
	copyIfPresent(context, "traceId", body)
	copyIfPresent(context, "attempt", body)
	if metadata, ok := body["metadata"].(map[string]interface{}); ok {
		copyIfPresent(context, "sessionId", metadata)
		copyIfPresent(context, "snapshotId", metadata)
		copyIfPresent(context, "toolInvocationId", metadata)
	}
	return context
}

func copyIfPresent(dst map[string]interface{}, key string, src map[string]interface{}) {
	if value, ok := src[key]; ok && value != nil && fmt.Sprint(value) != "" {
		dst[key] = value
	}
}

func firstNonEmptyString(values ...interface{}) string {
	for _, value := range values {
		if value == nil {
			continue
		}
		if text := strings.TrimSpace(fmt.Sprint(value)); text != "" {
			return text
		}
	}
	return ""
}

func cloneMap(in map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
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
