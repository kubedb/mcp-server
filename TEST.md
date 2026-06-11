# Testing the KubeDB MCP Server

This document covers four layers of testing: build verification, protocol smoke tests (no cluster needed), a live cluster test matrix, and in-cluster deployment tests. The protocol tests were last run against the code in this repo and the expected outputs below reflect actual results.

## Prerequisites

| Layer | Needs |
|-------|-------|
| Build, protocol | Go 1.25+ |
| Live cluster | A cluster (kind works), kubectl, helm, a KubeDB license from https://appscode.com/issue-license |
| Inspector | Node.js (npx) |
| Container | docker (or podman) |

## 1. Build verification

```bash
go build ./...
go vet ./...
make build          # static binary in bin/kubedb-mcp
./bin/kubedb-mcp --version
```

All three must pass with zero output (except the version print).

## 2. Protocol smoke tests (no cluster required)

These verify MCP wiring only. They work even with no kubeconfig because tool listing never touches the cluster; only tool calls do.

### 2.1 stdio handshake and tool registry

```bash
{ printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'
  sleep 1
} | ./bin/kubedb-mcp
```

Expected: the initialize response reports `"name":"kubedb"`, and tools/list returns exactly 20 tools.

### 2.2 Safety mode registration

Repeat 2.1 with flags and count the tools in the tools/list response:

| Invocation | Expected tool count | What disappears |
|------------|--------------------:|-----------------|
| (no flags) | 20 | nothing |
| --read-only | 9 | every write tool |
| --disable-destructive | 19 | kubedb_delete_resource |

### 2.3 HTTP transport

```bash
./bin/kubedb-mcp --transport http --listen 127.0.0.1:8099 &

curl -s http://127.0.0.1:8099/healthz
# expected: ok

curl -s -X POST http://127.0.0.1:8099/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}'
# expected: SSE event containing serverInfo and the instructions string

kill %1
```

### 2.4 MCP Inspector (interactive)

```bash
make inspector
```

Browse the tool list, check every tool has a description and annotations (readOnlyHint on the 9 read tools, destructiveHint on delete, restart, halt, patch, apply), and call tools against a live context.

## 3. Live cluster test matrix

### 3.1 Environment setup

```bash
kind create cluster --name kubedb-test
helm install kubedb oci://ghcr.io/appscode-charts/kubedb \
  --version <latest> --namespace kubedb --create-namespace \
  --set-file global.license=/path/to/license.txt
kubectl wait --for=condition=Ready pods -n kubedb --all --timeout=300s
```

Run the server with `--allow-credentials` for the credentials test cases. Drive it through the MCP Inspector or any MCP client.

### 3.2 Read and observe

| # | Tool call | Expected |
|---|-----------|----------|
| R1 | kubedb_list_kinds | All installed groups present: kubedb.com, ops.kubedb.com, catalog.kubedb.com, autoscaling.kubedb.com, plus the hint string |
| R2 | kubedb_list_kinds with refresh=true after installing a new CRD | New kind appears |
| R3 | kubedb_list_contexts | kind-kubedb-test listed with current=true |
| R4 | kubedb_list_databases (empty cluster) | count 0, no error |
| R5 | kubedb_list_versions kind=Postgres | Non-empty list, each row has name, version, updateConstraints where defined |
| R6 | kubedb_list_versions kind=NoSuchDB | Error naming installed database kinds |

### 3.3 Provisioning lifecycle (happy path)

Run in order; later cases depend on earlier ones.

| # | Tool call | Expected |
|---|-----------|----------|
| P1 | kubedb_create_database kind=Postgres name=demo-pg namespace=demo version=<from R5> storageSize=1Gi deletionPolicy=WipeOut dryRun=true | "Dry run OK" message, nothing created |
| P2 | Same as P1 with dryRun=false | Created message; kubectl get postgres -n demo shows demo-pg |
| P3 | kubedb_database_health kind=Postgres name=demo-pg namespace=demo (poll) | Phase moves Provisioning to Ready; pods listed 1/1 Ready |
| P4 | kubedb_list_databases phase=Ready | demo-pg present with version, storage 1Gi |
| P5 | kubedb_get_resource kind=Postgres name=demo-pg namespace=demo format=yaml | Full manifest, no managedFields |
| P6 | kubedb_get_connection_info ... includeCredentials=false | Endpoints with svc DNS and port 5432, authSecretName demo-pg-auth |
| P7 | Same with includeCredentials=true (server started with --allow-credentials) | username and password fields present, warning string present |
| P8 | Same with includeCredentials=true (server started without the flag) | credentials withheld message naming the flag |

### 3.4 Day-2 operations

| # | Tool call | Expected |
|---|-----------|----------|
| O1 | kubedb_restart_database databaseKind=Postgres databaseName=demo-pg namespace=demo | OpsRequest created; kubedb_list_ops_requests shows type Restart, phase reaches Successful |
| O2 | kubedb_update_version targetVersion=<bogus> | Error listing the real catalog versions, no object created |
| O3 | kubedb_update_version targetVersion=<valid newer, allowed by updateConstraints> | OpsRequest created, db version changes after completion |
| O4 | kubedb_scale_database mode=horizontal replicas=3 | HorizontalScaling OpsRequest; replicas reach 3 |
| O5 | kubedb_scale_database mode=vertical spec='{"postgres":{"resources":{"requests":{"cpu":"600m","memory":"1.1Gi"},"limits":{"memory":"1.1Gi"}}}}' | VerticalScaling OpsRequest succeeds; pod resources updated |
| O6 | kubedb_scale_database mode=vertical (no spec) | Error explaining the required payload with example |
| O7 | kubedb_expand_volume size=2Gi (default Online; kind clusters may need mode=Offline depending on CSI) | VolumeExpansion OpsRequest; PVC grows to 2Gi |
| O8 | kubedb_create_ops_request type=RotateAuth | OpsRequest created; auth secret rotates |
| O9 | kubedb_create_ops_request targeting a database that does not exist | Error "target database ... not found", nothing created |
| O10 | kubedb_configure_autoscaler with storage='{"postgres":{"trigger":"On","usageThreshold":80,"scalingThreshold":50}}' | PostgresAutoscaler created; visible via kubedb_list_resources kind=PostgresAutoscaler |

### 3.5 Generic tools and guardrails

| # | Tool call | Expected |
|---|-----------|----------|
| G1 | kubedb_apply_manifest with a valid Redis manifest | Applied; second identical call also succeeds (server side apply idempotency) |
| G2 | kubedb_apply_manifest with a ConfigMap manifest | Refused: group outside the KubeDB family |
| G3 | kubedb_patch_resource patch='{"spec":{"deletionPolicy":"Halt"}}' | Patched; verify via kubedb_get_resource |
| G4 | kubedb_halt_database halted=true | Pods deleted, PVCs remain, phase Halted |
| G5 | kubedb_halt_database halted=false | Database returns to Ready |
| G6 | kubedb_delete_resource confirm=false | Error explaining the confirm gate and deletionPolicy semantics |
| G7 | kubedb_delete_resource confirm=true | Deleted; G1's Redis gone |
| G8 | kubedb_get_resource kind=MongoDB name=missing | Not-found error suggesting kubedb_list_resources |

### 3.6 Safety posture against a live cluster

| # | Server flags | Tool call | Expected |
|---|--------------|-----------|----------|
| S1 | --read-only | any write tool | Tool absent from tools/list entirely |
| S2 | --disable-destructive | kubedb_halt_database halted=true | Runtime refusal naming the flag |
| S3 | --disable-destructive | kubedb_create_database | Succeeds (additive writes stay allowed) |
| S4 | RBAC: bind kubedb-mcp-read only | kubedb_create_database | Kubernetes forbidden error surfaces cleanly |

### 3.7 Cleanup

```bash
kind delete cluster --name kubedb-test
```

## 4. In-cluster deployment test

```bash
make image VERSION=v0.1.0
kind load docker-image ghcr.io/kubedb/mcp-server:v0.1.0 --name kubedb-test
kubectl create namespace kubedb-mcp
kubectl apply -f deploy/openshift/rbac.yaml
# With the MCP lifecycle operator installed:
kubectl apply -f deploy/openshift/mcpserver.yaml
kubectl get mcpserver -n kubedb-mcp        # READY True, ADDRESS ends in :8080/mcp

kubectl port-forward -n kubedb-mcp svc/kubedb-mcp 8080:8080
curl -s http://localhost:8080/healthz       # ok
```

Then repeat the 2.3 initialize POST against localhost:8080/mcp and one read tool call (kubedb_list_kinds) to confirm the service account RBAC works.

## 5. Future automated coverage

Planned, not yet implemented: unit tests for pure helpers (deepMerge, summarize, ops payload key mapping) with table driven cases; golden file tests asserting the exact unstructured objects built for each OpsRequest type across engines; an envtest based integration suite registering KubeDB CRDs; and an eval suite of 10 read-only multi-step questions against a seeded kind cluster, following the MCP builder evaluation methodology (independent, verifiable, stable answers).
