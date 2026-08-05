FROM golang:1.23-bookworm AS builder
WORKDIR /src
COPY go.mod ./
COPY go.sum ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/aisphere-sandbox ./cmd/sandbox-manager

FROM debian:12-slim
RUN useradd -u 1000 -m app && apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata && rm -rf /var/lib/apt/lists/*
USER 1000:1000
COPY --from=builder /out/aisphere-sandbox /usr/local/bin/aisphere-sandbox
ENTRYPOINT ["/usr/local/bin/aisphere-sandbox"]
