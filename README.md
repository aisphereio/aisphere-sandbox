# aisphere-sandbox

`aisphere-sandbox` 是 AISphere 的**零信任隔离计算资源面**。它不重复实现 Kubernetes Pod/PVC/Service 控制器，而是复用 `kubernetes-sigs/agent-sandbox` 作为底层 CRD/Controller，并向 AISphere Runtime 提供稳定的 Profile、Lease、Workspace 和 Executor API。

> 架构方向已经固定：生产 Agent Loop 位于 AISphere Runtime。Sandbox 只提供工作区和隔离执行能力，不负责模型调用、Prompt/Context 组装、Skill 路由、长期记忆、业务授权或用户审批。详见 [ADR-001](docs/architecture/ADR-001-sandbox-boundary.md)。

## 系统位置

```text
Hub (definitions / SandboxProfile)
        |
        v
AISphere Runtime / Tool Broker
        |
        v
aisphere-sandbox
  Lease / Quota / Infra Policy / CRD Adapter
        |
        v
kubernetes-sigs/agent-sandbox
        |
        v
Sandbox Pod
  workspace / shell / python / browser / artifact executors
```

## 关键边界

- `Hub`：定义 Agent/Skill/Tool/SandboxProfile 等控制面资产；不直接驱动 Sandbox 执行。
- `AISphere Runtime`：拥有 Session、Run、ExecutionSnapshot、唯一 Agent Loop、模型调用、Tool Compiler/Broker、业务权限协调与审批。
- `aisphere-iam`：业务资源授权权威；Sandbox 不复制 ReBAC/分享规则。
- `aisphere-sandbox`：Profile 基础设施映射、Quota、Lease、CRD Adapter、Workspace、网络/资源隔离、状态、日志和诊断。
- `kubernetes-sigs/agent-sandbox`：K8s 原生 Sandbox/SandboxTemplate/SandboxClaim/SandboxWarmPool 生命周期。
- `Sandbox Pod`：零信任 Executor 环境，不运行生产 Agent Loop，不调用 Model Gateway。

## 当前迁移状态

- 默认 driver：`agent-sandbox`。
- 迁移期仍存在 `direct-kubernetes` fallback；目标是底层生命周期统一交给 `agent-sandbox`。
- `POST /v1/sandboxes/ensure` 支持创建 `agents.x-k8s.io/v1beta1 Sandbox`。
- 可选 `useClaim=true` + `defaultWarmPool`，走 `extensions.agents.x-k8s.io/v1beta1 SandboxClaim`。
- 历史镜像仍可能残留 `agentkit-session-worker` 相关代码；该路径已被 ADR-001 否决，后续只迁移其中有价值的 Workspace/Shell/Python/Browser 能力到 Tool Server / Executor，然后物理删除 Worker Agent Loop。

## API

当前 API：

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

目标 API contract 的核心对象不是 Pod，而是 `SandboxLease`。Runtime 不应感知底层使用 Sandbox CR、SandboxClaim、WarmPool 还是未来多集群调度。

## Lease Contract

目标 Lease 至少包含：

```text
sandboxId
lease/workload identity
expiresAt
profileDigest
workspaceRef
executor capabilities
service reference
phase
```

并绑定：

```text
tenantId
projectId
sessionId
runId
snapshotId
profileDigest
```

动态 Pod IP、PVC 名称、ServiceAccount token 等不得进入 Runtime ExecutionSnapshot。

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
    "runId": "run-001",
    "projectId": "project-a",
    "snapshotId": "snapshot-001",
    "profile": "default-python-offline"
  }'
```

Ensure 的 Ready 语义应是**本次需要的 Executor capability ready**，而不是历史 Session Worker ready。

## 文档

- [Sandbox 边界 ADR](docs/architecture/ADR-001-sandbox-boundary.md)
- [当前架构](docs/ARCHITECTURE.md)
- [API](docs/API.md)

被 Accepted ADR 否决的 Session-Worker 旧设计不再保留在主文档树中；Git history 即历史档案。

## 下一步收口

```text
P0 删除 Session Worker 生产构建/启动路径
P0 Ensure readiness 改为 executor-ready
P0 Lease 增加 run/snapshot/profile digest 绑定
P1 Tool Server contract 固化
P1 Lease/workload identity 校验
P1 Workspace 与 Pod 生命周期解耦
P1 Quota / Idle GC
P2 WarmPool / multi-cluster
```
