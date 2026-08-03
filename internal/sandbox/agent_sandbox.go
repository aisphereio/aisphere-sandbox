package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/actionlab-ai/aisphere-sandbox/internal/model"
)

const (
	agentSandboxGroup     = "agents.x-k8s.io"
	agentSandboxExtGroup  = "extensions.agents.x-k8s.io"
	agentSandboxVersion   = "v1beta1"
	agentSandboxManagedBy = "aisphere-sandbox-adapter"
	annotationProfile     = "aisphere.io/sandbox-profile"
	annotationTemplateRef = "aisphere.io/template-ref"
	annotationWarmPoolRef = "aisphere.io/warm-pool-ref"
	annotationPodName     = "agents.x-k8s.io/pod-name"
)

// AgentSandboxManager is the recommended driver. It does not create Pods/PVCs
// directly. Instead it creates kubernetes-sigs/agent-sandbox CRs and lets the
// upstream controller own the low-level lifecycle.
type AgentSandboxManager struct{ *K8sManager }

func NewAgentSandboxManager(cfg Config) (*AgentSandboxManager, error) {
	if cfg.Driver == "" {
		cfg.Driver = model.SandboxDriverAgentSandbox
	}
	if cfg.AgentSandboxAPIVersion == "" {
		cfg.AgentSandboxAPIVersion = agentSandboxVersion
	}
	base, err := NewKubernetesManager(cfg)
	if err != nil {
		return nil, err
	}
	return &AgentSandboxManager{K8sManager: base}, nil
}

func (m *AgentSandboxManager) Ensure(ctx context.Context, req model.SandboxEnsureRequest) (*model.SandboxStatus, error) {
	req = m.normalizeRequest(req)
	if req.Restart {
		_, _ = m.Restart(ctx, req.SandboxID)
	}
	if err := m.ensureConfigMap(ctx, req); err != nil {
		return nil, err
	}
	if m.cfg.UseClaim && firstNonEmpty(req.WarmPoolRef, m.cfg.DefaultWarmPool) != "" {
		if err := m.ensureSandboxClaim(ctx, req); err != nil {
			return nil, err
		}
		return m.Get(ctx, req.SandboxID)
	}
	if err := m.ensureSandboxCR(ctx, req); err != nil {
		return nil, err
	}
	return m.Get(ctx, req.SandboxID)
}

func (m *AgentSandboxManager) Get(ctx context.Context, sandboxID string) (*model.SandboxStatus, error) {
	id := cleanDNSName(sandboxID)
	if m.cfg.UseClaim && m.cfg.DefaultWarmPool != "" {
		var claim agentSandboxObject
		if err := m.getJSON(ctx, m.claimPath(id), &claim); err == nil {
			return m.statusFromClaim(ctx, id, &claim), nil
		} else if !isNotFound(err) {
			return nil, err
		}
	}
	var sb agentSandboxObject
	if err := m.getJSON(ctx, m.sandboxPath(id), &sb); err != nil {
		return nil, err
	}
	return m.statusFromSandbox(ctx, id, &sb), nil
}

func (m *AgentSandboxManager) List(ctx context.Context, q ListQuery) ([]*model.SandboxStatus, error) {
	selector := labelManagedBy + "=" + agentSandboxManagedBy
	if q.OrgID != "" {
		selector += "," + labelOrgID + "=" + labelValue(q.OrgID)
	}
	if q.ProjectID != "" {
		selector += "," + labelProjectID + "=" + labelValue(q.ProjectID)
	}
	if q.SessionID != "" {
		selector += "," + labelSessionID + "=" + labelValue(q.SessionID)
	}
	if q.AgentID != "" {
		selector += "," + labelAgentID + "=" + labelValue(q.AgentID)
	}
	var list agentSandboxList
	path := m.sandboxesPath() + "?labelSelector=" + url.QueryEscape(selector)
	if m.cfg.UseClaim && m.cfg.DefaultWarmPool != "" {
		path = m.claimsPath() + "?labelSelector=" + url.QueryEscape(selector)
	}
	if err := m.getJSON(ctx, path, &list); err != nil {
		return nil, err
	}
	out := make([]*model.SandboxStatus, 0, len(list.Items))
	for i := range list.Items {
		id := list.Items[i].Metadata.Labels[labelSandboxID]
		if id == "" {
			id = list.Items[i].Metadata.Name
		}
		if q.OwnerSubject != "" && list.Items[i].Metadata.Annotations["aisphere.io/owner-subject"] != q.OwnerSubject {
			continue
		}
		if m.cfg.UseClaim && m.cfg.DefaultWarmPool != "" {
			out = append(out, m.statusFromClaim(ctx, id, &list.Items[i]))
		} else {
			out = append(out, m.statusFromSandbox(ctx, id, &list.Items[i]))
		}
	}
	return out, nil
}

func (m *AgentSandboxManager) Restart(ctx context.Context, sandboxID string) (*model.SandboxStatus, error) {
	id := cleanDNSName(sandboxID)
	if m.cfg.UseClaim && m.cfg.DefaultWarmPool != "" {
		// Claim-backed sandboxes are usually recycled by deleting the claim and
		// creating a new one. The adapter keeps the API stable and returns the
		// current claim state when it cannot reconstruct the original request.
		_ = m.patchJSON(ctx, m.claimPath(id), map[string]interface{}{"metadata": map[string]interface{}{"annotations": map[string]string{"aisphere.io/restarted-at": time.Now().UTC().Format(time.RFC3339)}}})
		return m.Get(ctx, id)
	}
	_ = m.patchJSON(ctx, m.sandboxPath(id), map[string]interface{}{"spec": map[string]string{"operatingMode": "Suspended"}})
	time.Sleep(500 * time.Millisecond)
	if err := m.patchJSON(ctx, m.sandboxPath(id), map[string]interface{}{"spec": map[string]string{"operatingMode": "Running"}, "metadata": map[string]interface{}{"annotations": map[string]string{"aisphere.io/restarted-at": time.Now().UTC().Format(time.RFC3339)}}}); err != nil {
		return nil, err
	}
	return m.Get(ctx, id)
}

func (m *AgentSandboxManager) Delete(ctx context.Context, sandboxID string, deleteWorkspace bool) error {
	id := cleanDNSName(sandboxID)
	if !deleteWorkspace {
		// Preserve the session environment by suspending the Sandbox. The upstream
		// controller terminates runtime resources while the CR remains as the lease
		// anchor. Workspace persistence depends on the configured template/PVC policy.
		if m.cfg.UseClaim && m.cfg.DefaultWarmPool != "" {
			return m.patchJSON(ctx, m.claimPath(id), map[string]interface{}{"metadata": map[string]interface{}{"annotations": map[string]string{"aisphere.io/suspended-at": time.Now().UTC().Format(time.RFC3339)}}})
		}
		return m.patchJSON(ctx, m.sandboxPath(id), map[string]interface{}{"spec": map[string]string{"operatingMode": "Suspended"}})
	}
	path := m.sandboxPath(id)
	if m.cfg.UseClaim && m.cfg.DefaultWarmPool != "" {
		path = m.claimPath(id)
	}
	return m.delete(ctx, path)
}

func (m *AgentSandboxManager) Logs(ctx context.Context, sandboxID string, q model.SandboxLogQuery) (string, error) {
	id := cleanDNSName(sandboxID)
	podName := ""
	if st, err := m.Get(ctx, id); err == nil {
		podName = st.PodName
	}
	if podName == "" {
		podName = "aisb-" + id
	}
	if q.Container == "" {
		q.Container = "worker"
	}
	if q.TailLines <= 0 || q.TailLines > 2000 {
		q.TailLines = 200
	}
	p := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/log?container=%s&tailLines=%d", url.PathEscape(m.namespace), url.PathEscape(podName), url.QueryEscape(q.Container), q.TailLines)
	b, err := m.do(ctx, http.MethodGet, p, nil, "")
	if err != nil && q.Container == "worker" {
		p = fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/log?tailLines=%d", url.PathEscape(m.namespace), url.PathEscape(podName), q.TailLines)
		b, err = m.do(ctx, http.MethodGet, p, nil, "")
	}
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (m *AgentSandboxManager) normalizeRequest(req model.SandboxEnsureRequest) model.SandboxEnsureRequest {
	req.SandboxID = normalizeSandboxID(req)
	if req.Profile == "" {
		req.Profile = m.cfg.DefaultProfile
	}
	if req.TemplateRef == "" {
		req.TemplateRef = m.cfg.DefaultTemplate
	}
	if req.WarmPoolRef == "" {
		req.WarmPoolRef = m.cfg.DefaultWarmPool
	}
	if req.Image == "" {
		req.Image = m.cfg.Image
	}
	if req.ImagePullPolicy == "" {
		req.ImagePullPolicy = m.cfg.ImagePullPolicy
	}
	if req.WorkspaceSize == "" {
		req.WorkspaceSize = firstNonEmpty(req.Limits.Storage, m.cfg.WorkspaceSize)
	}
	if req.StorageClass == "" {
		req.StorageClass = m.cfg.StorageClass
	}
	if req.Limits.CPU == "" {
		req.Limits.CPU = m.cfg.DefaultCPU
	}
	if req.Limits.Memory == "" {
		req.Limits.Memory = m.cfg.DefaultMemory
	}
	if req.Limits.IdleTTLSeconds == 0 {
		req.Limits.IdleTTLSeconds = int64(m.cfg.IdleTTLSeconds)
	}
	req.Network.Mode = normalizeNetworkMode(firstNonEmpty(req.Network.Mode, m.cfg.DefaultNetworkMode))
	return req
}

func (m *AgentSandboxManager) ensureSandboxCR(ctx context.Context, req model.SandboxEnsureRequest) error {
	obj := m.buildSandboxCR(req)
	return m.createIgnoreExists(ctx, m.sandboxesPath(), obj)
}

func (m *AgentSandboxManager) ensureSandboxClaim(ctx context.Context, req model.SandboxEnsureRequest) error {
	labels := sandboxLabels(req)
	labels[labelManagedBy] = agentSandboxManagedBy
	ann := sandboxAnnotations(req)
	ann[annotationProfile] = req.Profile
	ann[annotationTemplateRef] = req.TemplateRef
	ann[annotationWarmPoolRef] = req.WarmPoolRef
	claim := map[string]interface{}{
		"apiVersion": "extensions.agents.x-k8s.io/" + firstNonEmpty(m.cfg.AgentSandboxAPIVersion, agentSandboxVersion),
		"kind":       "SandboxClaim",
		"metadata":   map[string]interface{}{"name": req.SandboxID, "labels": labels, "annotations": ann},
		"spec": map[string]interface{}{
			"warmPoolRef":           map[string]string{"name": req.WarmPoolRef},
			"additionalPodMetadata": map[string]interface{}{"labels": labels, "annotations": ann},
			"env": []map[string]string{
				{"name": "AISPHERE_SANDBOX_ID", "value": req.SandboxID},
				{"name": "AISPHERE_SESSION_ID", "value": req.SessionID},
				{"name": "AISPHERE_AGENT_ID", "value": req.AgentID},
				{"name": "AISPHERE_SNAPSHOT_ID", "value": req.SnapshotID},
				{"name": "AISPHERE_WORKSPACE", "value": firstNonEmpty(m.cfg.WorkspaceMountPath, "/workspace")},
			},
		},
	}
	return m.createIgnoreExists(ctx, m.claimsPath(), claim)
}

func (m *AgentSandboxManager) buildSandboxCR(req model.SandboxEnsureRequest) map[string]interface{} {
	labels := sandboxLabels(req)
	labels[labelManagedBy] = agentSandboxManagedBy
	ann := sandboxAnnotations(req)
	ann[annotationProfile] = req.Profile
	ann[annotationTemplateRef] = req.TemplateRef
	mountPath := firstNonEmpty(m.cfg.WorkspaceMountPath, "/workspace")
	resources := map[string]interface{}{"requests": map[string]string{"cpu": req.Limits.CPU, "memory": req.Limits.Memory}, "limits": map[string]string{"cpu": firstNonEmpty(m.cfg.MaxCPU, req.Limits.CPU), "memory": firstNonEmpty(m.cfg.MaxMemory, req.Limits.Memory)}}
	container := map[string]interface{}{
		"name":            "worker",
		"image":           req.Image,
		"imagePullPolicy": req.ImagePullPolicy,
		"workingDir":      mountPath,
		"resources":       resources,
		"ports":           []map[string]interface{}{{"name": "worker", "containerPort": 8088}, {"name": "tools", "containerPort": m.cfg.ToolPort}, {"name": "browser", "containerPort": m.cfg.BrowserPort}, {"name": "web", "containerPort": m.cfg.VNCOrWebPort}},
		"volumeMounts":    []map[string]interface{}{{"name": "workspace", "mountPath": mountPath}, {"name": "sandbox-manifest", "mountPath": "/etc/aisphere/sandbox", "readOnly": true}},
		"env": []map[string]string{
			{"name": "AISPHERE_SANDBOX_ID", "value": req.SandboxID}, {"name": "AISPHERE_RUNTIME_ID", "value": req.RuntimeID}, {"name": "AISPHERE_SESSION_ID", "value": req.SessionID}, {"name": "AISPHERE_RUN_ID", "value": req.RunID}, {"name": "AISPHERE_AGENT_ID", "value": req.AgentID}, {"name": "AISPHERE_SNAPSHOT_ID", "value": req.SnapshotID}, {"name": "AISPHERE_WORKSPACE", "value": mountPath}, {"name": "AISPHERE_TOOL_PORT", "value": fmt.Sprintf("%d", m.cfg.ToolPort)}, {"name": "AISPHERE_BROWSER_PORT", "value": fmt.Sprintf("%d", m.cfg.BrowserPort)}, {"name": "AISPHERE_SESSION_WORKER_ENABLED", "value": "true"},
		},
		"securityContext": map[string]interface{}{"allowPrivilegeEscalation": false, "privileged": false, "capabilities": map[string][]string{"drop": {"ALL"}}},
	}
	podSpec := map[string]interface{}{
		"restartPolicy":      "Always",
		"serviceAccountName": m.cfg.ServiceAccount,
		"containers":         []map[string]interface{}{container},
		"volumes":            []map[string]interface{}{{"name": "sandbox-manifest", "configMap": map[string]string{"name": configMapName(req.SandboxID)}}},
		"securityContext":    map[string]interface{}{"runAsNonRoot": true, "runAsUser": 1000, "runAsGroup": 1000, "fsGroup": 1000, "seccompProfile": map[string]string{"type": "RuntimeDefault"}},
	}
	if m.cfg.RuntimeClassName != "" {
		podSpec["runtimeClassName"] = m.cfg.RuntimeClassName
	}
	service := true
	return map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/" + firstNonEmpty(m.cfg.AgentSandboxAPIVersion, agentSandboxVersion),
		"kind":       "Sandbox",
		"metadata":   map[string]interface{}{"name": req.SandboxID, "labels": labels, "annotations": ann},
		"spec": map[string]interface{}{
			"service":              service,
			"operatingMode":        "Running",
			"podTemplate":          map[string]interface{}{"metadata": map[string]interface{}{"labels": labels, "annotations": ann}, "spec": podSpec},
			"volumeClaimTemplates": []map[string]interface{}{{"metadata": map[string]interface{}{"name": "workspace", "labels": labels, "annotations": ann}, "spec": map[string]interface{}{"accessModes": []string{"ReadWriteOnce"}, "resources": map[string]interface{}{"requests": map[string]string{"storage": req.WorkspaceSize}}}}},
		},
	}
}

func (m *AgentSandboxManager) statusFromSandbox(ctx context.Context, id string, sb *agentSandboxObject) *model.SandboxStatus {
	ann := copyMap(sb.Metadata.Annotations)
	labels := copyMap(sb.Metadata.Labels)
	phase, reason, msg := phaseFromConditions(sb.Status.Conditions)
	podName := firstNonEmpty(ann[annotationPodName], ann["aisphere.io/pod-name"])
	if podName == "" {
		podName = m.findPodName(ctx, id, sb.Status.Selector)
	}
	// agent-sandbox controller names the headless Service after the Sandbox
	// resource itself. Do not apply the direct-kubernetes `aisb-` prefix while
	// status.serviceFQDN is still being populated during the first Get call.
	serviceName := firstNonEmpty(sb.Status.Service, id)
	toolsHost := sb.Status.ServiceFQDN
	if toolsHost == "" {
		toolsHost = fmt.Sprintf("%s.%s.svc", serviceName, m.namespace)
	}
	return &model.SandboxStatus{SandboxID: id, Namespace: m.namespace, Driver: model.SandboxDriverAgentSandbox, Phase: phase, Reason: reason, Message: msg, PodName: podName, PodIP: firstString(sb.Status.PodIPs), NodeName: sb.Status.NodeName, ServiceName: serviceName, WorkspacePVC: ann["aisphere.io/workspace-pvc"], Profile: ann[annotationProfile], TemplateRef: ann[annotationTemplateRef], WarmPoolRef: ann[annotationWarmPoolRef], Image: ann["aisphere.io/image"], NetworkMode: ann["aisphere.io/network-mode"], RuntimeID: ann["aisphere.io/runtime-id"], SessionID: ann["aisphere.io/session-id"], RunID: ann["aisphere.io/run-id"], AgentID: ann["aisphere.io/agent-id"], SnapshotID: ann["aisphere.io/snapshot-id"], Endpoints: m.agentSandboxEndpoints(toolsHost), Labels: labels, Annotations: ann, CreatedAt: sb.Metadata.CreationTimestamp, UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
}

func (m *AgentSandboxManager) statusFromClaim(ctx context.Context, id string, claim *agentSandboxObject) *model.SandboxStatus {
	ann := copyMap(claim.Metadata.Annotations)
	labels := copyMap(claim.Metadata.Labels)
	phase, reason, msg := phaseFromConditions(claim.Status.Conditions)
	sandboxName := firstNonEmpty(claim.Status.Sandbox.Name, ann["agents.x-k8s.io/sandbox-name"])
	st := &model.SandboxStatus{SandboxID: id, Namespace: m.namespace, Driver: model.SandboxDriverAgentSandbox, Phase: phase, Reason: reason, Message: msg, WarmPoolRef: ann[annotationWarmPoolRef], Profile: ann[annotationProfile], TemplateRef: ann[annotationTemplateRef], RuntimeID: ann["aisphere.io/runtime-id"], SessionID: ann["aisphere.io/session-id"], RunID: ann["aisphere.io/run-id"], AgentID: ann["aisphere.io/agent-id"], SnapshotID: ann["aisphere.io/snapshot-id"], Labels: labels, Annotations: ann, CreatedAt: claim.Metadata.CreationTimestamp, UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
	if sandboxName != "" {
		var sb agentSandboxObject
		if err := m.getJSON(ctx, m.sandboxPath(sandboxName), &sb); err == nil {
			return m.statusFromSandbox(ctx, sandboxName, &sb)
		}
	}
	return st
}

func (m *AgentSandboxManager) agentSandboxEndpoints(host string) []model.SandboxEndpoint {
	return []model.SandboxEndpoint{{Name: "worker", URL: fmt.Sprintf("http://%s:%d", host, 8088), Port: 8088}, {Name: "tools", URL: fmt.Sprintf("http://%s:%d", host, m.cfg.ToolPort), Port: m.cfg.ToolPort}, {Name: "browser", URL: fmt.Sprintf("http://%s:%d", host, m.cfg.BrowserPort), Port: m.cfg.BrowserPort}, {Name: "web", URL: fmt.Sprintf("http://%s:%d", host, m.cfg.VNCOrWebPort), Port: m.cfg.VNCOrWebPort}}
}

func (m *AgentSandboxManager) findPodName(ctx context.Context, id, selector string) string {
	if selector == "" {
		selector = labelSandboxID + "=" + labelValue(id)
	}
	var list k8sList
	if err := m.getJSON(ctx, "/api/v1/namespaces/"+url.PathEscape(m.namespace)+"/pods?labelSelector="+url.QueryEscape(selector), &list); err == nil && len(list.Items) > 0 {
		return list.Items[0].Metadata.Name
	}
	return ""
}

func phaseFromConditions(conds []agentSandboxCondition) (string, string, string) {
	if len(conds) == 0 {
		return model.SandboxPhasePending, "", ""
	}
	phase := model.SandboxPhasePending
	reason, msg := "", ""
	for _, c := range conds {
		if c.Type == "Ready" {
			reason, msg = c.Reason, c.Message
			if strings.EqualFold(c.Status, "True") {
				phase = model.SandboxPhaseRunning
			}
			if strings.EqualFold(c.Status, "False") && c.Reason != "" {
				phase = model.SandboxPhasePending
			}
		}
	}
	return phase, reason, msg
}

func (m *AgentSandboxManager) sandboxesPath() string {
	return "/apis/agents.x-k8s.io/" + firstNonEmpty(m.cfg.AgentSandboxAPIVersion, agentSandboxVersion) + "/namespaces/" + url.PathEscape(m.namespace) + "/sandboxes"
}
func (m *AgentSandboxManager) sandboxPath(id string) string {
	return m.sandboxesPath() + "/" + url.PathEscape(cleanDNSName(id))
}
func (m *AgentSandboxManager) claimsPath() string {
	return "/apis/extensions.agents.x-k8s.io/" + firstNonEmpty(m.cfg.AgentSandboxAPIVersion, agentSandboxVersion) + "/namespaces/" + url.PathEscape(m.namespace) + "/sandboxclaims"
}
func (m *AgentSandboxManager) claimPath(id string) string {
	return m.claimsPath() + "/" + url.PathEscape(cleanDNSName(id))
}

func (m *K8sManager) patchJSON(ctx context.Context, p string, obj interface{}) error {
	b, _ := json.Marshal(obj)
	_, err := m.do(ctx, http.MethodPatch, p, b, "application/merge-patch+json")
	return err
}

type agentSandboxList struct {
	Items []agentSandboxObject `json:"items"`
}
type agentSandboxObject struct {
	Metadata struct {
		Name              string            `json:"name"`
		Labels            map[string]string `json:"labels"`
		Annotations       map[string]string `json:"annotations"`
		CreationTimestamp string            `json:"creationTimestamp"`
	} `json:"metadata"`
	Status struct {
		ServiceFQDN string                  `json:"serviceFQDN"`
		Service     string                  `json:"service"`
		Conditions  []agentSandboxCondition `json:"conditions"`
		Selector    string                  `json:"selector"`
		PodIPs      []string                `json:"podIPs"`
		NodeName    string                  `json:"nodeName"`
		Sandbox     struct {
			Name   string   `json:"name"`
			PodIPs []string `json:"podIPs"`
		} `json:"sandbox"`
	} `json:"status"`
}
type agentSandboxCondition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

func firstString(v []string) string {
	if len(v) > 0 {
		return v[0]
	}
	return ""
}
