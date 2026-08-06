# ADR-001: AISphere Sandbox 隔离执行边界

- 状态：Accepted
- 日期：2026-08-06
- 适用仓库：`aisphere-sandbox`

## 背景

当前 Sandbox 同时存在两种定位：

1. 隔离执行基础设施，提供 Workspace、Shell、Python、Browser 和 Tool Server。
2. Session-Native Agent 宿主，在 Pod 内运行 Python Session Worker、Agent Loop 和模型调用。

第二种定位与 AgentKit GoRunner 形成双 Agent Loop，导致 Prompt、Skill、Tool、模型、审批和事件协议重复建设。

本 ADR 采用破坏性重构策略，将 Sandbox 固定为零信任计算资源面。

## 决策

Sandbox 只回答：

> 不可信代码或需要隔离的动作在哪里、以什么资源和网络策略执行？

Sandbox 不回答：

> Agent 应如何推理、看到什么上下文、何时调用模型或 Tool？

## Sandbox Manager 职责

`aisphere-sandbox` 管理：

- SandboxProfile 的基础设施实现
- Sandbox / SandboxClaim / SandboxWarmPool 适配
- Lease
- Quota
- Workspace / PVC
- CPU、Memory、GPU 与存储限制
- Network Policy
- Ready / Restart / Delete / Idle GC
- Logs 与诊断
- Artifact 导出
- 多集群调度扩展点

底层继续复用 `kubernetes-sigs/agent-sandbox`，不重复实现 Pod/PVC/Service Controller。

## Sandbox Pod 职责

Sandbox Pod 可以运行：

```text
workspace tool server
shell executor
python executor
browser sidecar
artifact worker
log forwarder
optional local MCP stdio adapter
```

它接受 Runtime Tool Broker 发起的、已经授权的结构化调用。

## Sandbox 不拥有的能力

生产路径中 Sandbox 不再拥有：

- Agent Loop
- 模型调用
- Prompt Builder
- Context Builder
- Skill Router
- Memory 检索
- Hub Resolve
- Agent/Skill/Tool 分享规则
- 用户长期凭据
- Tool 审批状态机

## “Session Native”的重新定义

保留 Session 与 Sandbox 的绑定，但重新定义为：

> Session-scoped Workspace and Sandbox Lease

而不是：

> Agent process is born inside the Sandbox Pod

正确关系：

```text
Runtime Session
  -> optional SandboxLease
  -> Workspace + isolated executors
```

Agent Loop 始终位于 AISphere Runtime。

## 标准调用链

```text
AISphere Runtime
  -> Tool Broker
  -> Sandbox Adapter
  -> aisphere-sandbox Lease validation
  -> Sandbox Tool Server
  -> executor
  -> normalized result / artifact / event
```

Sandbox Manager 不承接所有高频调用的业务判断；它只验证 Lease、资源边界和基础设施策略。业务 Tool 权限、参数策略、审批和凭据委派由 Runtime Tool Broker 完成。

## Lease 契约

Runtime 只消费稳定 Lease，不感知底层 CRD：

```text
sandboxId
leaseToken / workload identity
expiresAt
profileDigest
workspaceRef
endpoints or service reference
capabilities
phase
```

Lease 必须绑定：

```text
tenant / org / project / session / run / profileDigest
```

## Skill 与文件

Sandbox 不加载 Skill Instruction 或 Reference 到模型上下文。

只有这些 Skill 资源可以由 Runtime 按需物化到 Sandbox：

- scripts
- templates
- binaries
- assets required by sandbox tools

物化内容必须绑定 SkillVersion digest，并默认只读。

## Tool Server

Tool Server 只实现执行协议，不决定 Tool 是否应向模型暴露。

输入至少包含：

```text
runId
sessionId
snapshotId
toolInvocationId
tool canonical ref
validated arguments
capability token / lease identity
```

输出必须标准化：

```text
status
result metadata
artifact refs
stdout/stderr summary
error code
resource usage
```

## 停止发展的方向

以下方向立即停止新增：

- Python Session Worker 的 Agent Loop 能力
- Sandbox 内模型客户端
- Sandbox 内 Prompt/Skill Context 组装
- 依赖 `WorkerEndpoint` 的新生产功能
- Sandbox 自行连接 Hub 并扩大权限

现有 Session Worker 仅作为迁移期兼容路径，目标是删除而不是长期维护双引擎。

## 迁移原则

1. GoRunner 成为唯一 Agent Loop 后，先停止构建 Session Worker 生产镜像。
2. 将 Session Worker 中有价值的文件、Shell、Browser、Python 能力迁入 Tool Server/Executor。
3. Sandbox Ensure 不再要求 Worker Ready，只要求所需 Executor capability Ready。
4. Lease cache key 必须包含 profile/image/security digest，避免跨安全级别复用。
5. Workspace 生命周期与 Sandbox Pod 生命周期解耦，支持保留 Workspace、回收 Pod。

## 成功标准

- Sandbox 中没有模型 API Key 和用户长期 OAuth Token。
- Sandbox 不调用 Model Gateway。
- Sandbox 不解析 AgentDefinition 来运行 Agent Loop。
- Runtime 可以替换 Sandbox 实现而不改变 Agent Loop。
- Sandbox 回收后，Run 和 Session 的事实状态仍由 Runtime 保持。
