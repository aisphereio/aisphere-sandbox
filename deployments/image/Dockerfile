# syntax=docker/dockerfile:1.7
# AgentKit secure sandbox image.
#
# The default base is a standard devcontainer image so the sandbox has a
# familiar Linux development environment. In private/offline environments,
# mirror this base image and all apt packages into your internal registry.
ARG BASE_IMAGE=mcr.microsoft.com/devcontainers/base:debian-12
FROM ${BASE_IMAGE}

ARG INSTALL_BROWSER=true
ARG SANDBOX_USER=aisphere
ARG SANDBOX_UID=1000
ARG SANDBOX_GID=1000

ENV DEBIAN_FRONTEND=noninteractive \
    AISPHERE_WORKSPACE=/workspace \
    AISPHERE_TOOL_PORT=18081 \
    AISPHERE_BROWSER_PORT=9222 \
    AISPHERE_WEB_PORT=6080 \
    AISPHERE_NETWORK_MODE=offline \
    AISPHERE_ENABLE_SHELL=false \
    AISPHERE_MAX_OUTPUT_BYTES=1048576 \
    PYTHONUNBUFFERED=1

USER root

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        bash zsh sudo ca-certificates curl wget git git-lfs openssh-client \
        jq yq ripgrep fd-find fzf tree less vim nano \
        unzip zip tar gzip xz-utils zstd \
        python3 python3-pip python3-venv python3-dev \
        nodejs npm \
        golang-go make gcc g++ pkg-config \
        sqlite3 postgresql-client default-mysql-client redis-tools \
        procps psmisc lsof iproute2 iputils-ping dnsutils netcat-openbsd \
        tini gosu \
    && if [ "${INSTALL_BROWSER}" = "true" ]; then \
        apt-get install -y --no-install-recommends chromium; \
       fi \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/*

RUN if ! getent group ${SANDBOX_USER} >/dev/null; then groupadd --gid ${SANDBOX_GID} ${SANDBOX_USER}; fi \
    && if ! id -u ${SANDBOX_USER} >/dev/null 2>&1; then useradd --uid ${SANDBOX_UID} --gid ${SANDBOX_GID} -m -s /bin/bash ${SANDBOX_USER}; fi \
    && mkdir -p /workspace /opt/aisphere/bin /opt/aisphere/tools /opt/aisphere/skills /etc/aisphere/sandbox /tmp/aisphere \
    && chown -R ${SANDBOX_UID}:${SANDBOX_GID} /workspace /opt/aisphere /tmp/aisphere

COPY bin/aisphere-tool-server.py /opt/aisphere/bin/aisphere-tool-server.py
COPY bin/aisphere-capability-server.py /opt/aisphere/bin/aisphere-capability-server.py
COPY bin/agentkit-session-worker.py /opt/aisphere/bin/agentkit-session-worker.py
COPY bin/aisphere-sandbox-entrypoint.sh /usr/local/bin/aisphere-sandbox-entrypoint
COPY tools/default-tools.json /opt/aisphere/tools/default-tools.json
COPY skills /opt/aisphere/skills
RUN chmod +x /opt/aisphere/bin/aisphere-tool-server.py /opt/aisphere/bin/aisphere-capability-server.py /opt/aisphere/bin/agentkit-session-worker.py /usr/local/bin/aisphere-sandbox-entrypoint \
    && chown -R ${SANDBOX_UID}:${SANDBOX_GID} /opt/aisphere /usr/local/bin/aisphere-sandbox-entrypoint

WORKDIR /workspace
EXPOSE 8088 18081 9222 6080
USER ${SANDBOX_UID}:${SANDBOX_GID}
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/aisphere-sandbox-entrypoint"]
