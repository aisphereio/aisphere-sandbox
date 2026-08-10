#!/usr/bin/env python3
"""AISphere Sandbox executor capability protocol V1.

This module deliberately wraps the existing executor implementation instead of
copying it. Hub ToolVersion semantics and model-facing schemas are not owned by
the Sandbox. The Sandbox exposes only the low-level capabilities it can really
execute and accepts capability invocations from the trusted Runtime Tool Broker.

Legacy /v1/tools and /v1/tools/call endpoints remain available through the
wrapped Handler during migration, but new Runtime integrations must use the
/v1/capabilities contract.
"""
from __future__ import annotations

import importlib.util
import json
import os
import sys
import time
from http.server import ThreadingHTTPServer
from pathlib import Path
from typing import Any

LEGACY_SERVER = Path("/opt/aisphere/bin/aisphere-tool-server.py")
CONTRACT_VERSION = "v1"


def load_executor_module() -> Any:
    spec = importlib.util.spec_from_file_location("aisphere_executor", LEGACY_SERVER)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load sandbox executor module: {LEGACY_SERVER}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


executor = load_executor_module()


def now_ms() -> int:
    return int(time.time() * 1000)


def enabled_capabilities() -> dict[str, Any]:
    capabilities = dict(executor.TOOLS)
    if not executor.ENABLE_SHELL:
        capabilities.pop("shell.exec", None)
    return capabilities


def capability_manifest() -> list[dict[str, Any]]:
    schemas = {
        str(item.get("name", "")): item
        for item in executor.load_tool_schemas()
        if isinstance(item, dict) and item.get("name")
    }
    manifest: list[dict[str, Any]] = []
    for capability in sorted(enabled_capabilities()):
        schema = schemas.get(capability, {})
        manifest.append(
            {
                "id": capability,
                "contractVersion": CONTRACT_VERSION,
                "description": str(schema.get("description") or "Sandbox executor capability"),
                "inputSchema": schema.get("inputSchema") or {"type": "object"},
                "metadata": {
                    "executor": "aisphere-sandbox",
                    "networkMode": executor.NETWORK_MODE,
                },
            }
        )
    return manifest


def validate_context(context: Any) -> dict[str, Any]:
    if context is None:
        return {}
    if not isinstance(context, dict):
        raise ValueError("context must be an object")
    # Runtime identity/lease binding is intentionally separate from business
    # IAM. The fields are propagated now so the SandboxLease validator can make
    # them mandatory without changing the ToolVersion or executor capability.
    allowed = {
        "runId",
        "sessionId",
        "snapshotId",
        "toolInvocationId",
        "traceId",
        "attempt",
    }
    return {key: value for key, value in context.items() if key in allowed and value not in (None, "")}


class CapabilityHandler(executor.Handler):
    server_version = "AisphereSandboxCapabilityServer/1"

    def do_GET(self) -> None:  # noqa: N802
        if self.path == "/v1/capabilities":
            return executor.json_response(
                self,
                200,
                {
                    "contractVersion": CONTRACT_VERSION,
                    "capabilities": capability_manifest(),
                },
            )
        return super().do_GET()

    def do_POST(self) -> None:  # noqa: N802
        if self.path != "/v1/capabilities/call":
            return super().do_POST()

        started = now_ms()
        capability = ""
        try:
            body = executor.read_json(self)
            capability = str(body.get("capability") or "").strip()
            if not capability:
                return executor.json_response(
                    self,
                    400,
                    {
                        "ok": False,
                        "error": {"code": "CAPABILITY_REQUIRED", "message": "capability is required"},
                    },
                )
            handlers = enabled_capabilities()
            handler = handlers.get(capability)
            if handler is None:
                return executor.json_response(
                    self,
                    404,
                    {
                        "ok": False,
                        "capability": capability,
                        "error": {"code": "CAPABILITY_NOT_FOUND", "message": capability},
                    },
                )
            inp = body.get("input") or {}
            if not isinstance(inp, dict):
                raise ValueError("input must be an object")
            context = validate_context(body.get("context"))
            result = handler(inp)
            return executor.json_response(
                self,
                200,
                {
                    "ok": True,
                    "capability": capability,
                    "result": result,
                    "context": context,
                    "durationMillis": now_ms() - started,
                },
            )
        except Exception as exc:  # structured executor failure, not HTTP ambiguity
            return executor.json_response(
                self,
                500,
                {
                    "ok": False,
                    "capability": capability,
                    "error": {"code": exc.__class__.__name__, "message": str(exc)},
                    "durationMillis": now_ms() - started,
                },
            )


def main() -> None:
    executor.WORKSPACE.mkdir(parents=True, exist_ok=True)
    os.chdir(str(executor.WORKSPACE))
    server = ThreadingHTTPServer(("0.0.0.0", executor.TOOL_PORT), CapabilityHandler)
    print(
        json.dumps(
            {
                "event": "sandbox_capability_server_start",
                "contractVersion": CONTRACT_VERSION,
                "port": executor.TOOL_PORT,
                "workspace": str(executor.WORKSPACE),
                "networkMode": executor.NETWORK_MODE,
                "capabilities": sorted(enabled_capabilities()),
            },
            ensure_ascii=False,
        ),
        flush=True,
    )
    server.serve_forever()


if __name__ == "__main__":
    main()
