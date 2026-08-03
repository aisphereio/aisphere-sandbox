from __future__ import annotations

import importlib.util
import json
import os
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.request import Request, urlopen


class WorkerFlowTest(unittest.TestCase):
    def test_skill_context_and_allowlisted_tool_loop(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            manifest = root / "tool-manifest.json"
            manifest.write_text(json.dumps({"metadata": {
                "allowedTools": ["workspace.read"],
                "skillRefs": [{"name": "demo"}],
                "agentDefinition": {
                    "entryPoint": "root_agent.yaml",
                    "files": {
                        "root_agent.yaml": "name: test_agent\nmodel: agent-defined-model\ndescription: Test agent\ninstruction: |\n  Follow the agent-defined instruction.\nskills:\n  - demo\n"
                    },
                },
            }}), encoding="utf-8")
            skill = root / ".aisphere" / "skills" / "demo" / "SKILL.md"
            skill.parent.mkdir(parents=True)
            skill.write_text("Use workspace.read for inspected files.", encoding="utf-8")
            calls: list[dict] = []
            model_requests: list[dict] = []
            tool_server = _ToolServer(calls)
            model_server = _ModelServer(model_requests)
            tool_server.start()
            model_server.start()
            try:
                os.environ.update({
                    "AISPHERE_WORKSPACE": str(root),
                    "AISPHERE_TOOL_MANIFEST": str(manifest),
                    "AISPHERE_TOOL_SERVER": f"http://127.0.0.1:{tool_server.port}",
                    "AISPHERE_MODEL_BASE_URL": f"http://127.0.0.1:{model_server.port}/v1",
                    "AISPHERE_MODEL_PROFILE": "test-model",
                    "AISPHERE_SESSION_ID": "sess_test",
                    "AISPHERE_AGENT_ID": "agent_test",
                })
                spec = importlib.util.spec_from_file_location("agentkit_session_worker", Path(__file__).with_name("agentkit-session-worker.py"))
                assert spec and spec.loader
                worker = importlib.util.module_from_spec(spec)
                spec.loader.exec_module(worker)
                reply, _ = worker.call_model_gateway_with_tools("run_test", "read notes")
                self.assertEqual(reply, "done")
                self.assertEqual(calls[0]["tool"], "workspace.read")
                self.assertEqual(len(model_requests), 2)
                self.assertIn("Use workspace.read", model_requests[0]["messages"][0]["content"])
                self.assertIn("Follow the agent-defined instruction.", model_requests[0]["messages"][0]["content"])
                self.assertEqual(model_requests[0]["model"], "agent-defined-model")
                self.assertEqual(model_requests[0]["tools"][0]["function"]["name"], "workspace.read")
            finally:
                tool_server.shutdown()
                model_server.shutdown()


class SandboxToolServerTest(unittest.TestCase):
    def test_real_workspace_tool_server_round_trip(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            os.environ["AISPHERE_WORKSPACE"] = tmp
            os.environ["AISPHERE_ENABLE_SHELL"] = "false"
            path = Path(__file__).with_name("aisphere-tool-server.py")
            spec = importlib.util.spec_from_file_location("aisphere_tool_server", path)
            assert spec and spec.loader
            module = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(module)
            server = ThreadingHTTPServer(("127.0.0.1", 0), module.Handler)
            threading.Thread(target=server.serve_forever, daemon=True).start()
            try:
                base = f"http://127.0.0.1:{server.server_port}"
                with urlopen(base + "/v1/tools") as response:
                    tools = json.loads(response.read().decode())["tools"]
                self.assertTrue(any(item["name"] == "workspace.write" for item in tools))
                payload = json.dumps({"tool": "workspace.write", "input": {"path": "notes.txt", "content": "hello"}}).encode()
                request = Request(base + "/v1/tools/call", data=payload, headers={"Content-Type": "application/json"}, method="POST")
                with urlopen(request) as response:
                    result = json.loads(response.read().decode())
                self.assertTrue(result["ok"])
                self.assertEqual((Path(tmp) / "notes.txt").read_text(encoding="utf-8"), "hello")
            finally:
                server.shutdown()


class _ToolServer:
    def __init__(self, calls: list[dict]):
        self.calls = calls
        parent = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_args):
                return

            def do_GET(self):
                body = {"tools": [{"name": "workspace.read", "description": "read", "inputSchema": {"type": "object"}}, {"name": "shell.exec"}]}
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(json.dumps(body).encode())

            def do_POST(self):
                size = int(self.headers.get("Content-Length", "0"))
                parent.calls.append(json.loads(self.rfile.read(size)))
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(json.dumps({"ok": True, "tool": "workspace.read", "result": {"content": "notes"}}).encode())

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.port = self.server.server_port

    def start(self):
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def shutdown(self):
        self.server.shutdown()


class _ModelServer:
    def __init__(self, requests: list[dict]):
        self.requests = requests
        parent = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_args):
                return

            def do_POST(self):
                size = int(self.headers.get("Content-Length", "0"))
                parent.requests.append(json.loads(self.rfile.read(size)))
                if len(parent.requests) == 1:
                    message = {"role": "assistant", "content": None, "tool_calls": [{"id": "call_1", "type": "function", "function": {"name": "workspace.read", "arguments": json.dumps({"path": "notes.txt"})}}]}
                else:
                    message = {"role": "assistant", "content": "done"}
                body = {"model": "test-model", "choices": [{"message": message}], "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}}
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(json.dumps(body).encode())

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.port = self.server.server_port

    def start(self):
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def shutdown(self):
        self.server.shutdown()


if __name__ == "__main__":
    unittest.main()
