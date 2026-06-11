# KubeDB MCP Server
#
# UBI based multi-stage build, suitable for Red Hat container certification
# and the Red Hat OpenShift AI MCP catalog.
#
# Build:
#   docker build -t ghcr.io/kubedb/mcp-server:v0.1.0 -f Dockerfile .

ARG VERSION=v0.1.0

FROM registry.access.redhat.com/ubi9/go-toolset:latest AS builder
ARG VERSION
ENV GOTOOLCHAIN=auto
WORKDIR /opt/app-root/src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o kubedb-mcp ./cmd/kubedb-mcp

FROM registry.access.redhat.com/ubi9/ubi-micro:latest
ARG VERSION

LABEL name="kubedb-mcp-server" \
      vendor="AppsCode Inc." \
      version="${VERSION}" \
      release="1" \
      summary="MCP server for KubeDB: manage 35+ database engines on Kubernetes with AI agents" \
      description="Model Context Protocol server exposing KubeDB operations as tools: provision databases, run day-2 operations (version upgrades, scaling, volume expansion, restarts, TLS, credential rotation), configure autoscaling, and inspect health and connection info across Postgres, MySQL, MongoDB, Redis, Kafka, Elasticsearch and more." \
      io.k8s.display-name="KubeDB MCP Server" \
      io.k8s.description="MCP server for managing KubeDB databases in Kubernetes and OpenShift clusters" \
      io.openshift.tags="kubedb,mcp,model-context-protocol,database,ai,agent,appscode" \
      maintainer="AppsCode Inc. <support@appscode.com>" \
      url="https://kubedb.com"

COPY --from=builder /opt/app-root/src/kubedb-mcp /usr/local/bin/kubedb-mcp
COPY LICENSE /licenses/LICENSE

# Non root, arbitrary UID friendly (OpenShift restricted SCC compatible).
USER 65532:65532

EXPOSE 8080
ENV KUBEDB_MCP_TRANSPORT=http \
    KUBEDB_MCP_LISTEN=:8080

ENTRYPOINT ["/usr/local/bin/kubedb-mcp"]
