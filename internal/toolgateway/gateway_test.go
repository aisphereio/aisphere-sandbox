package toolgateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListToolsCompatibilityFacadeUsesCapabilityV1(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/capabilities" {
			t.Fatalf("path = %q, want /v1/capabilities", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"contractVersion": "v1",
			"capabilities": []map[string]interface{}{{
				"id": "workspace.read",
				"inputSchema": map[string]interface{}{"type": "object"},
			}},
		})
	}))
	defer server.Close()

	out, err := NewHTTPGateway().ListTools(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	tools, ok := out["tools"].([]interface{})
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v, want one compatibility entry", out["tools"])
	}
	tool, ok := tools[0].(map[string]interface{})
	if !ok || tool["name"] != "workspace.read" {
		t.Fatalf("tool = %#v, want name workspace.read", tools[0])
	}
}

func TestCallCompatibilityFacadeUsesCapabilityV1(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/capabilities/call" {
			t.Fatalf("path = %q, want /v1/capabilities/call", r.URL.Path)
		}
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["capability"] != "workspace.read" {
			t.Fatalf("capability = %#v, want workspace.read", body["capability"])
		}
		contextBody, _ := body["context"].(map[string]interface{})
		if contextBody["runId"] != "run-1" || contextBody["snapshotId"] != "snapshot-1" || contextBody["sessionId"] != "session-1" {
			t.Fatalf("context = %#v", contextBody)
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"ok": true,
			"capability": "workspace.read",
			"result": map[string]interface{}{"content": "ok"},
		})
	}))
	defer server.Close()

	out, err := NewHTTPGateway().Call(context.Background(), server.URL, map[string]interface{}{
		"tool":  "workspace.read",
		"runId": "run-1",
		"input": map[string]interface{}{"path": "README.md"},
		"metadata": map[string]interface{}{
			"snapshotId": "snapshot-1",
			"sessionId":  "session-1",
		},
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if out["ok"] != true {
		t.Fatalf("Call() = %#v, want ok=true", out)
	}
}
