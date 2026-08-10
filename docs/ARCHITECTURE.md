# AISphere Sandbox Architecture

> 当前架构以 [`docs/architecture/ADR-001-sandbox-boundary.md`](architecture/ADR-001-sandbox-boundary.md) 为准。

## 1. 定位

`aisphere-sandbox` 是 AISphere 的**隔离计算资源面**，不是 Agent Runtime。

它只解决：

> 不可信代码、文件操作、Shell、Python、Browser 等动作在哪里、以什么资源/网络边界执行。

Agent 推理、模型调用、Prompt/Context 装配、Tool 选择、业务授权与审批全部属于 AISphere Runtime。

```text
Hub (definitions)
       |
       | immutable references / profile
       v
AISphere Runtime
  Run / Snapshot / Agent Loop / Tool Broker
       |
       | authorized structured invocation
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

**Hub 不直接调用 Sandbox 数据面。** Hub 只定义 `SandboxProfile` 等控制面资产；Runtime 根据不可变 ExecutionSnapshot 消费这些定义并申请 Lease。

## 2. 组件职责

### 2.1 Sandbox Manager

负责：

- `SandboxProfile` 的基础设施映射与校验；
- Sandbox / SandboxClaim / SandboxWarmPool 适配；
- Lease 创建、续期、校验和回收；
- Tenant/Project 资源 Quota；
- Workspace/PVC 生命周期；
- CPU / Memory / GPU / Storage 限制；
- NetworkPolicy / egress mode；
- Ready / Restart / Delete / Idle GC；
- Logs、诊断和 Artifact 导出；
- 多集群调度扩展点。

不负责：

- Agent Loop；
- 模型调用；
- Prompt/Context Builder；
- Skill 选择或注入；
- Tool 是否向模型暴露；
- Tool 参数业务策略；
- 用户操作审批；
- 用户长期 Credential；
- Hub Catalog；
- IAM 业务授权模型。

### 2.2 `kubernetes-sigs/agent-sandbox`

负责 Kubernetes 原生生命周期：

```text
Sandbox
SandboxTemplate
SandboxClaim
SandboxWarmPool
Pod / PVC / Service
```

`aisphere-sandbox` 不重复实现这套 Controller。

### 2.3 Sandbox Pod

Sandbox Pod 只运行 Executor/Tool Server 类型进程，例如：

```text
workspace executor
shell executor
python executor
browser sidecar
artifact worker
log forwarder
optional local MCP stdio adapter
```

**生产 Pod 不运行 Agent Loop，不调用 Model Gateway。**

## 3. 权限边界

身份入口由平台 Gateway/OIDC 建立可信 Principal；业务资源权限由 `aisphere-iam` 判断。

Runtime 在进入 Sandbox 前已经完成：

```text
Tool allowlist
-> input schema
-> argument/resource policy
-> IAM authorization
-> risk evaluation
-> approval
-> credential delegation (when needed)
```

Sandbox Manager 只做基础设施级校验：

```text
Lease identity
Tenant / Project binding
Profile digest
Resource quota
Executor capability
Network / filesystem boundary
```

因此：

- IAM 判断“这个 Principal 有没有资格执行该业务动作”；
- Runtime Approval 判断“这一次具体副作用是否已确认”；
- Sandbox 判断“这个已授权调用是否属于当前 Lease，并且没有突破隔离边界”。

三者不能互相替代。

## 4. Lease 是唯一稳定运行契约

Runtime 不感知 Pod/PVC/Service/CRD 细节，只消费 Lease：

```text
sandboxId
lease identity / workload identity
expiresAt
profileDigest
workspaceRef
capabilities
serviceRef / endpoint reference
phase
```

Lease 至少绑定：

```text
tenantId
projectId
sessionId
runId
snapshotId
profileDigest
```

动态 Pod IP、PVC 名称、ServiceAccount token 等不得写入 Runtime ExecutionSnapshot。

## 5. 标准调用链

### 5.1 创建/复用隔离环境

```text
Runtime
 -> read pinned SandboxProfile from ExecutionSnapshot
 -> Sandbox Manager Ensure
 -> validate quota/profile
 -> agent-sandbox Sandbox/SandboxClaim
 -> executor ready
 -> SandboxLease
 -> Runtime binds lease to current Run/Session
```

Sandbox readiness 指的是**所需 Executor capability ready**，不是 Session Worker ready。

### 5.2 调用 Sandbox Tool

```text
Model FunctionCall
 -> Runtime Tool Broker
 -> Unified Invocation Pipeline
 -> Sandbox Adapter
 -> Sandbox Manager / Lease validation
 -> Sandbox Tool Server
 -> Executor
 -> normalized result / artifact refs
 -> RuntimeEvent + ToolInvocation
 -> Model FunctionResponse
```

Tool 调用失败首先回到 Runtime Event Ledger；Hub 不是高频执行事件的数据面。

## 6. Tool Server Contract

Tool Server 只执行已解析、已授权的结构化请求。

输入建议固定为：

```text
runId
sessionId
snapshotId
attemptId
toolInvocationId
canonicalToolRef
validatedArguments
lease/workload identity
```

输出统一为：

```text
status
result metadata
artifact refs
stdout/stderr summary
error code
resource usage
```

禁止 Tool Server：

- 自己从 Hub 拉“latest Tool”；
- 自己决定用户是否批准；
- 保存用户长期 OAuth/API Key；
- 自己启动模型推理。

## 7. Workspace 生命周期

Workspace 与 Sandbox Pod 生命周期解耦：

```text
Session / Project Workspace
        |
        +-- Sandbox Pod #1
        |
        +-- Sandbox Pod #2 after recycle/retry
```

这样 Idle GC 可以回收计算资源而不丢工作区；是否保留 Workspace 由 Runtime/产品策略通过 Sandbox API 明确表达。

## 8. Network Modes

推荐标准化：

- `offline`：默认拒绝 egress；
- `restricted`：DNS + allowlisted destinations；
- `online`：受集群基线策略约束的通用出网。

Tool 是否有业务层网络权限由 Runtime Policy 决定；NetworkPolicy 是最后一道基础设施防线。

## 9. Deprecated / Remove

以下均不再是生产架构组成部分：

- `agentkit-session-worker` Agent Loop；
- Sandbox 内 Model Gateway client；
- Worker endpoint `:8088` 作为 Agent 消息入口；
- `AISPHERE_SESSION_WORKER_ENABLED` 生产模式；
- Hub -> Sandbox direct execution；
- Sandbox 自己解析 AgentDefinition/Skill instruction；
- Sandbox 持有用户长期 Credential。

这些遗留实现应逐步物理删除；有价值的 workspace/shell/python/browser 能力迁移到 Executor/Tool Server。

## 10. 当前代码收口顺序

```text
P0 删除 Session Worker 生产构建/启动路径
P0 Ensure readiness 从 worker-ready 改为 executor-ready
P0 Lease contract 增加 run/snapshot/profile digest 绑定
P1 Tool Server 请求/响应 schema 固化
P1 Lease/workload identity 校验
P1 Workspace 与 Pod 生命周期解耦
P1 Quota + Idle GC
P2 WarmPool / multi-cluster scheduling
```

任何新增功能如果需要 Sandbox 调模型、组装 Prompt 或决定业务授权，应直接拒绝并放回 Runtime/IAM 设计。
