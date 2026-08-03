#!/usr/bin/env python3
"""Minimal AgentKit session worker runtime for sandbox-native sessions.

This is intentionally small. It proves the architecture where a session is born
inside the sandbox. Production AgentKit will replace the placeholder message
handler with the real Agent loop while keeping the same HTTP/event contract.
"""
from __future__ import annotations

import json
import os
import queue
import sys
import urllib.error
import urllib.request
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any

WORKSPACE = Path(os.environ.get("AISPHERE_WORKSPACE", "/workspace")).resolve()
WORKER_PORT = int(os.environ.get("AISPHERE_WORKER_PORT", "8088"))
SESSION_ID = os.environ.get("AISPHERE_SESSION_ID", "")
AGENT_ID = os.environ.get("AISPHERE_AGENT_ID", "")
SNAPSHOT_ID = os.environ.get("AISPHERE_SNAPSHOT_ID", "")
TOOL_SERVER = os.environ.get("AISPHERE_TOOL_SERVER", "http://127.0.0.1:18081")
TOOL_MANIFEST = Path(os.environ.get("AISPHERE_TOOL_MANIFEST", "/etc/aisphere/sandbox/tool-manifest.json"))
MODEL_BASE_URL = os.environ.get("AISPHERE_MODEL_BASE_URL", "http://aisphere-gateway:18083/v1").rstrip("/")
MODEL_TOKEN = os.environ.get("AISPHERE_MODEL_TOKEN", "")
MODEL_PROFILE = os.environ.get("AISPHERE_MODEL_PROFILE", "deepseek-v4-agent")
EVENT_LOG = WORKSPACE / ".aisphere" / "session-events.jsonl"

EVENTS: "queue.Queue[dict[str, Any]]" = queue.Queue(maxsize=2048)


def manifest_metadata() -> dict[str, Any]:
    try:
        raw = json.loads(TOOL_MANIFEST.read_text(encoding="utf-8"))
        return raw.get("metadata") or {}
    except (OSError, ValueError):
        return {}


def selected_tool_names() -> set[str] | None:
    metadata = manifest_metadata()
    if "allowedTools" not in metadata:
        return None
    return {str(name) for name in metadata.get("allowedTools") or [] if str(name).strip()}


def filesystem_name(raw: str) -> str:
    out = "".join(ch if ch.isalnum() or ch in "-_." else "_" for ch in raw.strip()).strip(".")
    return out or "agent"


def agent_definition_from_manifest() -> dict[str, Any]:
    value = manifest_metadata().get("agentDefinition")
    return value if isinstance(value, dict) else {}


def agent_definition_dir() -> Path:
    return WORKSPACE / ".aisphere" / "agents" / filesystem_name(AGENT_ID or "agent")


def read_agent_definition_file() -> tuple[str, str]:
    definition = agent_definition_from_manifest()
    entry_point = str(definition.get("entryPoint") or "root_agent.yaml")
    files = definition.get("files") if isinstance(definition.get("files"), dict) else {}
    manifest_text = files.get(entry_point)
    if isinstance(manifest_text, str):
        return manifest_text, f"manifest:{entry_point}"

    root = agent_definition_dir()
    candidates = [root / entry_point, root / "root_agent.yaml"]
    candidates.extend(sorted((WORKSPACE / ".aisphere" / "agents").glob("*/root_agent.yaml")))
    for path in candidates:
        try:
            if path.exists() and path.is_file():
                return path.read_text(encoding="utf-8", errors="replace")[:256000], str(path)
        except OSError:
            continue
    return "", ""


def strip_yaml_scalar(value: str) -> str:
    value = value.strip()
    if len(value) >= 2 and value[0] == value[-1] and value[0] in ("'", '"'):
        return value[1:-1]
    return value


def parse_agent_yaml(text: str) -> dict[str, Any]:
    result: dict[str, Any] = {}
    lines = text.splitlines()
    i = 0
    while i < len(lines):
        raw = lines[i]
        if not raw.strip() or raw.lstrip().startswith("#") or raw.startswith((" ", "\t")):
            i += 1
            continue
        if ":" not in raw:
            i += 1
            continue
        key, value = raw.split(":", 1)
        key = key.strip()
        value = value.strip()
        if key in {"name", "description", "model", "instruction", "global_instruction"}:
            if value in {"|", "|-", "|+", ">", ">-", ">+"}:
                block: list[str] = []
                i += 1
                while i < len(lines) and (not lines[i].strip() or lines[i].startswith((" ", "\t"))):
                    block.append(lines[i][2:] if lines[i].startswith("  ") else lines[i].lstrip())
                    i += 1
                result[key] = "\n".join(block).strip()
                continue
            result[key] = strip_yaml_scalar(value)
        elif key == "skills":
            skills: list[str] = []
            i += 1
            while i < len(lines) and (not lines[i].strip() or lines[i].startswith((" ", "\t"))):
                stripped = lines[i].strip()
                if stripped.startswith("- "):
                    skills.append(strip_yaml_scalar(stripped[2:]))
                i += 1
            result[key] = [item for item in skills if item]
            continue
        i += 1
    return result


def agent_config() -> dict[str, Any]:
    text, source = read_agent_definition_file()
    cfg = parse_agent_yaml(text) if text else {}
    cfg["source"] = source
    definition = agent_definition_from_manifest()
    model = definition.get("model") if isinstance(definition.get("model"), dict) else {}
    if not cfg.get("model") and isinstance(model, dict):
        cfg["model"] = model.get("profile") or model.get("model") or model.get("profileCode") or model.get("logicalName") or ""
    return cfg


def active_model_profile() -> str:
    cfg = agent_config()
    return str(cfg.get("model") or MODEL_PROFILE)


def http_json(url: str, *, method: str = "GET", payload: dict[str, Any] | None = None, timeout: int = 60) -> dict[str, Any]:
    data = None if payload is None else json.dumps(payload, ensure_ascii=False).encode("utf-8")
    req = urllib.request.Request(url, data=data, method=method)
    req.add_header("Accept", "application/json")
    if data is not None:
        req.add_header("Content-Type", "application/json")
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return json.loads(resp.read().decode("utf-8"))


def model_tools() -> list[dict[str, Any]]:
    metadata = manifest_metadata()
    allowlist = selected_tool_names()
    response = {"tools": metadata.get("toolSchemas") or []}
    if not response["tools"]:
        response = http_json(TOOL_SERVER.rstrip("/") + "/v1/tools")
    out: list[dict[str, Any]] = []
    for item in response.get("tools") or []:
        name = str(item.get("name") or "")
        if not name or (allowlist is not None and name not in allowlist):
            continue
        out.append({
            "type": "function",
            "function": {
                "name": name,
                "description": str(item.get("description") or "Sandbox tool"),
                "parameters": item.get("inputSchema") or {"type": "object"},
            },
        })
    return out


def skill_context() -> str:
    metadata = manifest_metadata()
    selected = {str(item.get("name")) for item in metadata.get("skillRefs") or [] if isinstance(item, dict)}
    cfg = agent_config()
    selected.update(str(item) for item in cfg.get("skills") or [] if str(item).strip())
    roots = [WORKSPACE / ".aisphere" / "skills", Path("/opt/aisphere/skills")]
    chunks: list[str] = []
    seen: set[str] = set()
    for root in roots:
        if not root.exists():
            continue
        for path in sorted(root.glob("*/SKILL.md")):
            name = path.parent.name
            if (selected and name not in selected) or name in seen:
                continue
            try:
                content = path.read_text(encoding="utf-8", errors="replace")[:24000]
            except OSError:
                continue
            seen.add(name)
            chunks.append(f"## Skill: {name}\n{content}")
    return "\n\n".join(chunks)[:96000]


def emit(event: dict[str, Any]) -> None:
    event.setdefault("ts", time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()))
    try:
        EVENTS.put_nowait(event)
    except queue.Full:
        pass
    EVENT_LOG.parent.mkdir(parents=True, exist_ok=True)
    with EVENT_LOG.open("a", encoding="utf-8") as f:
        f.write(json.dumps(event, ensure_ascii=False) + "\n")


def read_json(handler: BaseHTTPRequestHandler) -> dict[str, Any]:
    n = int(handler.headers.get("Content-Length") or 0)
    if n <= 0:
        return {}
    return json.loads(handler.rfile.read(n).decode("utf-8"))


def write_json(handler: BaseHTTPRequestHandler, status: int, body: dict[str, Any]) -> None:
    data = json.dumps(body, ensure_ascii=False).encode("utf-8")
    handler.send_response(status)
    handler.send_header("Content-Type", "application/json; charset=utf-8")
    handler.send_header("Content-Length", str(len(data)))
    handler.end_headers()
    handler.wfile.write(data)


class Handler(BaseHTTPRequestHandler):
    server_version = "AgentKitSessionWorker/0.1"

    def log_message(self, fmt: str, *args: Any) -> None:
        sys.stderr.write("%s %s\n" % (self.log_date_time_string(), fmt % args))

    def do_GET(self) -> None:  # noqa: N802
        if self.path in ("/healthz", "/readyz"):
            return write_json(self, 200, {"ok": True, "sessionId": SESSION_ID, "agentId": AGENT_ID, "workspace": str(WORKSPACE), "toolServer": TOOL_SERVER})
        if self.path == "/v1/session":
            return write_json(self, 200, {"sessionId": SESSION_ID, "agentId": AGENT_ID, "snapshotId": SNAPSHOT_ID, "workspace": str(WORKSPACE), "toolServer": TOOL_SERVER})
        if self.path.startswith("/v1/events"):
            # Simple long-poll endpoint. The production worker can upgrade this to SSE.
            items: list[dict[str, Any]] = []
            deadline = time.time() + 2
            while time.time() < deadline and len(items) < 50:
                try:
                    items.append(EVENTS.get(timeout=0.2))
                except queue.Empty:
                    if items:
                        break
            return write_json(self, 200, {"items": items})
        return write_json(self, 404, {"error": "not_found"})

    def do_POST(self) -> None:  # noqa: N802
        if self.path not in ("/v1/messages", "/v1/session/messages"):
            return write_json(self, 404, {"error": "not_found"})
        body = read_json(self)
        run_id = body.get("runId") or "run_" + uuid.uuid4().hex[:16]
        text = str(body.get("message") or body.get("content") or "")
        emit({"type": "user_message", "runId": run_id, "content": text})
        try:
            reply, usage = call_model_gateway_with_tools(run_id, text)
            emit({"type": "model_usage", "runId": run_id, **usage})
        except Exception as exc:  # keep worker alive; surface error as event
            reply = f"模型网关调用失败：{exc}. 当前 session 仍在沙箱 {WORKSPACE} 内运行，tool server: {TOOL_SERVER}."
            emit({"type": "error", "runId": run_id, "message": str(exc), "source": "model_gateway"})
        emit({"type": "assistant_message", "runId": run_id, "content": reply})
        emit({"type": "run_done", "runId": run_id})
        return write_json(self, 200, {"ok": True, "runId": run_id, "accepted": True})


def call_model_gateway(run_id: str, text: str) -> tuple[str, dict[str, Any]]:
    payload = {
        "model": MODEL_PROFILE,
        "messages": [
            {"role": "system", "content": f"你运行在 AI Sphere Sandbox 内。当前工作目录是 {WORKSPACE}。所有文件操作都应限定在 /workspace。"},
            {"role": "user", "content": text},
        ],
        "stream": False,
    }
    data = json.dumps(payload, ensure_ascii=False).encode("utf-8")
    req = urllib.request.Request(MODEL_BASE_URL + "/chat/completions", data=data, method="POST")
    req.add_header("Content-Type", "application/json")
    if MODEL_TOKEN:
        req.add_header("Authorization", "Bearer " + MODEL_TOKEN)
    req.add_header("X-AISphere-Session-ID", SESSION_ID)
    req.add_header("X-AISphere-Run-ID", run_id)
    req.add_header("X-AISphere-Agent-ID", AGENT_ID)
    with urllib.request.urlopen(req, timeout=600) as resp:
        body = resp.read().decode("utf-8")
    root = json.loads(body)
    content = ""
    choices = root.get("choices") or []
    if choices:
        msg = choices[0].get("message") or {}
        content = str(msg.get("content") or "")
    usage = root.get("usage") or {}
    return content, {
        "model": root.get("model") or MODEL_PROFILE,
        "promptTokens": int(usage.get("prompt_tokens") or 0),
        "completionTokens": int(usage.get("completion_tokens") or 0),
        "totalTokens": int(usage.get("total_tokens") or 0),
    }


def call_model_gateway_with_tools(run_id: str, text: str) -> tuple[str, dict[str, Any]]:
    cfg = agent_config()
    system = (
        "You are running inside an AI Sphere sandbox. "
        f"The workspace is {WORKSPACE}; keep file operations inside /workspace."
    )
    if cfg.get("name") or cfg.get("description"):
        system += f"\n\nAgent identity:\nName: {cfg.get('name') or AGENT_ID}\nDescription: {cfg.get('description') or ''}".rstrip()
    instruction = str(cfg.get("instruction") or cfg.get("global_instruction") or "").strip()
    if instruction:
        system += "\n\nAgent instruction:\n" + instruction
    context = skill_context()
    if context:
        system += "\n\nThe following skills are active for this agent:\n" + context
    messages: list[dict[str, Any]] = [
        {"role": "system", "content": system},
        {"role": "user", "content": text},
    ]
    tools = model_tools()
    allowlist = selected_tool_names()
    totals = {"promptTokens": 0, "completionTokens": 0, "totalTokens": 0}
    model_name = active_model_profile()
    last_content = ""
    for _ in range(8):
        payload: dict[str, Any] = {"model": active_model_profile(), "messages": messages, "stream": False}
        if tools:
            payload["tools"] = tools
            payload["tool_choice"] = "auto"
        headers = {
            "Content-Type": "application/json",
            "X-AISphere-Session-ID": SESSION_ID,
            "X-AISphere-Run-ID": run_id,
            "X-AISphere-Agent-ID": AGENT_ID,
        }
        if MODEL_TOKEN:
            headers["Authorization"] = "Bearer " + MODEL_TOKEN
        data = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        req = urllib.request.Request(MODEL_BASE_URL + "/chat/completions", data=data, method="POST", headers=headers)
        with urllib.request.urlopen(req, timeout=600) as resp:
            root = json.loads(resp.read().decode("utf-8"))
        model_name = str(root.get("model") or model_name)
        usage = root.get("usage") or {}
        totals["promptTokens"] += int(usage.get("prompt_tokens") or 0)
        totals["completionTokens"] += int(usage.get("completion_tokens") or 0)
        totals["totalTokens"] += int(usage.get("total_tokens") or 0)
        choices = root.get("choices") or []
        message = (choices[0].get("message") if choices else None) or {}
        last_content = str(message.get("content") or "")
        tool_calls = message.get("tool_calls") or []
        if not tool_calls:
            return last_content, {"model": model_name, **totals}
        messages.append(message)
        for call in tool_calls:
            function = call.get("function") or {}
            name = str(function.get("name") or "")
            try:
                args = json.loads(function.get("arguments") or "{}")
            except (TypeError, ValueError):
                args = {}
            emit({"type": "tool_call", "runId": run_id, "tool": name, "data": {"input": args}})
            if allowlist is not None and name not in allowlist:
                result = {"ok": False, "error": {"code": "TOOL_NOT_ALLOWED", "message": name}}
            else:
                try:
                    result = http_json(TOOL_SERVER.rstrip("/") + "/v1/tools/call", method="POST", payload={"tool": name, "input": args}, timeout=120)
                except Exception as exc:
                    result = {"ok": False, "error": {"code": "TOOL_CALL_FAILED", "message": str(exc)}}
            emit({"type": "tool_result", "runId": run_id, "tool": name, "data": result})
            messages.append({
                "role": "tool",
                "tool_call_id": str(call.get("id") or ""),
                "name": name,
                "content": json.dumps(result, ensure_ascii=False),
            })
    return last_content or "The agent reached the maximum tool-call depth.", {"model": model_name, **totals}


def main() -> None:
    WORKSPACE.mkdir(parents=True, exist_ok=True)
    os.chdir(WORKSPACE)
    emit({"type": "worker_start", "sessionId": SESSION_ID, "agentId": AGENT_ID, "snapshotId": SNAPSHOT_ID, "workspace": str(WORKSPACE)})
    server = ThreadingHTTPServer(("0.0.0.0", WORKER_PORT), Handler)
    print(json.dumps({"event": "session_worker_start", "port": WORKER_PORT, "sessionId": SESSION_ID, "workspace": str(WORKSPACE)}, ensure_ascii=False), flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
