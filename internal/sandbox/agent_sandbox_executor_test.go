package sandbox

import (
	"testing"

	"github.com/actionlab-ai/aisphere-sandbox/internal/model"
)

func TestBuildSandboxCRDoesNotProvisionSessionWorker(t *testing.T) {
	manager := &AgentSandboxManager{K8sManager: &K8sManager{cfg: Config{
		ToolPort:           18081,
		BrowserPort:        9222,
		VNCOrWebPort:       6080,
		WorkspaceMountPath: "/workspace",
	}}}

	cr := manager.buildSandboxCR(model.SandboxEnsureRequest{
		SandboxID:     "sandbox-1",
		SessionID:     "session-1",
		RunID:         "run-1",
		AgentID:       "agent-1",
		SnapshotID:    "snapshot-1",
		Image:         "sandbox:executor-only",
		WorkspaceSize: "1Gi",
	})

	spec := cr["spec"].(map[string]interface{})
	podTemplate := spec["podTemplate"].(map[string]interface{})
	podSpec := podTemplate["spec"].(map[string]interface{})
	containers := podSpec["containers"].([]map[string]interface{})
	container := containers[0]

	ports := container["ports"].([]map[string]interface{})
	for _, port := range ports {
		if port["name"] == "worker" || port["containerPort"] == 8088 {
			t.Fatalf("legacy session worker port is still provisioned: %+v", port)
		}
	}

	env := container["env"].([]map[string]string)
	for _, item := range env {
		if item["name"] == "AISPHERE_SESSION_WORKER_ENABLED" {
			t.Fatalf("legacy session worker enable flag is still provisioned: %+v", item)
		}
	}
}

func TestAgentSandboxEndpointsExposeExecutorsOnly(t *testing.T) {
	manager := &AgentSandboxManager{K8sManager: &K8sManager{cfg: Config{
		ToolPort:     18081,
		BrowserPort:  9222,
		VNCOrWebPort: 6080,
	}}}

	endpoints := manager.agentSandboxEndpoints("sandbox.default.svc")
	seenTools := false
	for _, endpoint := range endpoints {
		if endpoint.Name == "worker" || endpoint.Port == 8088 {
			t.Fatalf("legacy session worker endpoint is still exposed: %+v", endpoint)
		}
		if endpoint.Name == "tools" && endpoint.Port == 18081 {
			seenTools = true
		}
	}
	if !seenTools {
		t.Fatal("tools executor endpoint is missing")
	}
}
