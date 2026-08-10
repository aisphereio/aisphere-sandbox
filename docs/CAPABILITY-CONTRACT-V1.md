# Sandbox Executor Capability Contract V1

Status: **Accepted / implementation in progress**  
Date: 2026-08-07

## Boundary

Sandbox does not own AISphere `Tool`, `ToolVersion`, Agent bindings, business IAM, ApprovalGrant, Credential Broker, prompt assembly, or Agent Loop.

Sandbox owns only the low-level executor capabilities available inside one leased isolation environment.

```text
Hub ToolVersion
  -> Runtime ToolCompiler
  -> Tool Broker
  -> SandboxAdapter
  -> Sandbox capability
  -> executor implementation
```

A model-facing Tool name and an executor capability id are separate contracts.

```text
model Tool: skill.pull
connector: sandbox
executor capability: git.fetch
```

The Sandbox must not infer the ToolVersion from the capability id.

## Endpoints

### `GET /v1/capabilities`

Returns only capabilities that the running Sandbox can really execute under the current profile/configuration.

```json
{
  "contractVersion": "v1",
  "capabilities": [
    {
      "id": "workspace.read",
      "contractVersion": "v1",
      "description": "...",
      "inputSchema": {"type": "object"},
      "metadata": {
        "executor": "aisphere-sandbox",
        "networkMode": "offline"
      }
    }
  ]
}
```

The manifest is an executor fact, not a model Tool catalog. Runtime may use it for readiness/capability validation, but an Agent sees only ToolVersions selected in its immutable ExecutionSnapshot.

### `POST /v1/capabilities/call`

```json
{
  "capability": "workspace.read",
  "input": {"path": "README.md"},
  "context": {
    "runId": "run_...",
    "sessionId": "session_...",
    "snapshotId": "snapshot_...",
    "toolInvocationId": "ti_...",
    "traceId": "...",
    "attempt": 1
  }
}
```

Success:

```json
{
  "ok": true,
  "capability": "workspace.read",
  "result": {},
  "context": {},
  "durationMillis": 12
}
```

Failure is structured and never converted into a fake success:

```json
{
  "ok": false,
  "capability": "workspace.read",
  "error": {
    "code": "FileNotFoundError",
    "message": "..."
  },
  "durationMillis": 4
}
```

## Capabilities present in the current image

Verified from `deployments/image/bin/aisphere-tool-server.py`:

```text
workspace.list
workspace.read
workspace.write
workspace.patch
workspace.delete
workspace.mkdir
workspace.search_files
workspace.search_text
shell.exec          # only when AISPHERE_ENABLE_SHELL=true
browser.status
browser.open
```

Not currently implemented:

```text
git.*
skill.*
python.exec
```

The control plane and Runtime must fail closed if a ToolVersion references a capability absent from this manifest. Do not advertise planned capabilities as if they already existed.

## Legacy migration

`/v1/tools` and `/v1/tools/call` remain temporarily available through the legacy Handler so existing images/clients do not break during the stacked Runtime migration.

They are not architectural truth. New integrations use `/v1/capabilities*`.

Once Runtime SandboxAdapter and Sandbox manager proxy have migrated, remove the legacy endpoints and rename historical `Tool Server` terminology to `Capability Server`/`Executor`.

## Next

1. Runtime `sandboxclient` consumes Capability V1.
2. Sandbox manager/lease proxy exposes the same capability contract without changing semantics.
3. SandboxLease binds tenant/project/session/run/snapshot/profile.
4. Add Git executor capabilities only after the lease + credential delegation path is defined.
5. Map `skill.*` ToolVersions to `service` or `sandbox` per operation; never invent a `skill` connector kind.
