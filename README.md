# aisphere-sandbox

`aisphere-sandbox` 是 AISphere 的隔离计算资源面。它不重复实现 Kubernetes Pod/PVC/Service 控制器，而是复用 `kubernetes-sigs/agent-sandbox` 作为底层 CRD/Controller，并向 AISphere Runtime 提供稳定的 Profile、Lease、Workspace 和 Executor API。

> 架构方向已经固定：生产 Agent Loop 位于 AISphere Runtime。Sandbox 只提供工作区和隔离执行能力，不负责模型调用、Prompt 组装、Skill 路由或长期记忆。详见 [ADR-001](docs/architecture/ADR-001-sandbox-boundary.md)。

本服务目标定位：

```text
AISphere Runtime / Tool Broker
  -> aisphere-sandbox API Adapter
  -> kubernetes-sigs/agent-sandbox CRD
  -> Sandbox Pod
  -> workspace / shell / python / browser / artifact executors
```

## 关键边界

- `agent-sandbox`：K8s 原生沙箱生命周期，负责 Sandbox/SandboxTemplate/SandboxClaim/SandboxWarmPool、Pod/PVC/Service。
- `aisphere-sandbox`：平台适配层，负责 Auth、Quota、Profile、Lease、CRD Adapter、状态、日志和调试代理。
- `AISphere Runtime`：负责 Session、Run、Agent Loop、模型调用、上下文装配、Tool 权限与审批。
- `Sandbox Pod`：零信任执行环境，提供 Workspace 和受控 Executor。

## 当前迁移状态

- 默认 driver：`agent-sandbox`。
- fallback driver：`direct-kubernetes`。
- `POST /v1/sandboxes/ensure` 创建 `agents.x-k8s.io/v1beta1 Sandbox`。
- 可选 `useClaim=true` + `defaultWarmPool`，走 `extensions.agents.x-k8s.io/v1beta1 SandboxClaim`。
- 当前镜像仍保留 `agentkit-session-worker` 兼容能力，但该路径已经 deprecated，不再作为生产目标继续增强。
- 后续将把 Session Worker 中有价值的文件、Shell、Python、Browser 能力收敛到 Tool Server / Executor，并移除 Sandbox 内 Agent Loop 和模型调用。

## API

```text
GET    /healthz
GET    /readyz
GET    /v1/sandboxes
POST   /v1/sandboxes/ensure
GET    /v1/sandboxes/{sandboxId}
POST   /v1/sandboxes/{sandboxId}/restart
DELETE /v1/sandboxes/{sandboxId}?deleteWorkspace=false
GET    /v1/sandboxes/{sandboxId}/logs?tail=200
GET    /v1/sandboxes/{sandboxId}/tools
POST   /v1/sandboxes/{sandboxId}/tools/call
```

## 本地编译

```bash
go test ./...
go build -o bin/aisphere-sandbox ./cmd/sandbox-manager
```

## 配置

复制配置：

```bash
cp configs/config.json.example config.json
```

默认 driver：

```json
{
  "sandbox": {
    "driver": "agent-sandbox",
    "agentSandboxApiVersion": "v1beta1",
    "useClaim": false,
    "defaultTemplate": "aisphere-agent-session",
    "defaultProfile": "default-python-offline"
  }
}
```

## 创建沙箱

```bash
curl -s http://127.0.0.1:18082/v1/sandboxes/ensure \
  -H 'Content-Type: application/json' \
  -d '{
    "sessionId": "sess-001",
    "orgId": "org-a",
    "projectId": "project-a",
    "agentId": "demo-agent",
    "snapshotId": "agent_snap_xxx",
    "profile": "default-python-offline"
  }'
```

目标 Lease 只向 Runtime 暴露稳定的执行契约：

```text
sandbox id
lease identity / expiry
workspace ref
profile digest
executor capabilities
service endpoints
```

Runtime 不应感知底层使用 Sandbox CR、SandboxClaim、WarmPool 还是未来的多集群调度实现。

## 文档

- [平台边界 ADR](docs/architecture/ADR-001-sandbox-boundary.md)
- [Session Native Sandbox 历史设计](docs/SESSION_NATIVE_SANDBOX_DESIGN.md)
- [API](docs/API.md)
- [架构](docs/ARCHITECTURE.md)

`SESSION_NATIVE_SANDBOX_DESIGN.md` 记录现有实现来源；其中“Agent Loop 在 Sandbox 内运行”的路线已被 ADR-001 取代。

## 部署示例

- `deployments/agent-sandbox/sandbox-direct-session.yaml`：直接创建 Sandbox CR。
- `deployments/agent-sandbox/sandboxtemplate-session-worker.yaml`：迁移期 Session Worker 模板。
- `deployments/agent-sandbox/sandboxwarmpool-session-worker.yaml`：迁移期预热池。
- `deployments/agent-sandbox/sandboxclaim-session-worker.yaml`：从 WarmPool 领取 Sandbox。
