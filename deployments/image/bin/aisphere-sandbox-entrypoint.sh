#!/usr/bin/env bash
set -euo pipefail

: "${AISPHERE_WORKSPACE:=/workspace}"
: "${AISPHERE_TOOL_PORT:=18081}"
: "${AISPHERE_BROWSER_PORT:=9222}"
: "${AISPHERE_WORKER_PORT:=8088}"
: "${AISPHERE_SESSION_WORKER_ENABLED:=false}"
: "${AISPHERE_NETWORK_MODE:=offline}"
: "${AISPHERE_ENABLE_BROWSER:=auto}"

mkdir -p "${AISPHERE_WORKSPACE}" /tmp/aisphere

start_browser=false
if [[ "${AISPHERE_ENABLE_BROWSER}" == "true" ]]; then
  start_browser=true
elif [[ "${AISPHERE_ENABLE_BROWSER}" == "auto" ]] && command -v chromium >/dev/null 2>&1; then
  start_browser=true
fi

if [[ "${start_browser}" == "true" ]]; then
  chromium \
    --headless=new \
    --no-sandbox \
    --disable-dev-shm-usage \
    --disable-gpu \
    --remote-debugging-address=0.0.0.0 \
    --remote-debugging-port="${AISPHERE_BROWSER_PORT}" \
    --user-data-dir=/tmp/aisphere/chromium-profile \
    about:blank >/tmp/aisphere/chromium.log 2>&1 &
fi

if [[ "${AISPHERE_SESSION_WORKER_ENABLED}" == "true" ]]; then
  python3 /opt/aisphere/bin/aisphere-capability-server.py >/tmp/aisphere/tool-server.log 2>&1 &
  export AISPHERE_TOOL_SERVER="${AISPHERE_TOOL_SERVER:-http://127.0.0.1:${AISPHERE_TOOL_PORT}}"
  exec python3 /opt/aisphere/bin/agentkit-session-worker.py
fi

exec python3 /opt/aisphere/bin/aisphere-capability-server.py
