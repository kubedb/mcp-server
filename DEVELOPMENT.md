# Developer Guide

Audience: a developer comfortable with Linux and Kubernetes who is new to some combination of MCP, Go's Kubernetes client machinery, and KubeDB internals. This guide maps what you need to learn, where to learn it, and how this codebase uses it.

## Project map

```
cmd/kubedb-mcp/main.go      flags, env, transport selection, graceful shutdown
internal/k8s/client.go      client factory, per-context cache, API discovery, kind resolution
internal/k8s/registry.go    KubeDB group constants, ops type to spec key mapping
internal/tools/tools.go     shared deps, safety guards, JSON helpers
internal/tools/observe.go   list_kinds, list_contexts, list_databases, list_resources,
                            get_resource, database_health
internal/tools/catalog.go   list_versions (catalog.kubedb.com)
internal/tools/connection.go get_connection_info (endpoints, auth secret, credentials)
internal/tools/lifecycle.go create_database, apply_manifest, patch, delete, halt
internal/tools/ops.go       OpsRequest builder + restart, update_version, scale,
                            expand_volume, list_ops_requests, configure_autoscaler
internal/server/server.go   tool registration, descriptions, annotations, safety filtering
deploy/openshift/           MCPServer CR + RBAC for in-cluster use
Dockerfile                  UBI multi-stage build for Red Hat certification
server.json                 MCP registry metadata
TEST.md                     test plan
```

Reading order for a first pass: main.go, then server.go, then k8s/client.go, then one tool file (observe.go), then ops.go.

## Concept 1: Model Context Protocol

MCP is JSON-RPC 2.0 between a client (Claude, an IDE, gen AI studio) and servers exposing tools, resources, and prompts. This server only exposes tools. The handshake is initialize, then notifications/initialized, then tools/list and tools/call.

Learn:

- Spec and concepts: https://modelcontextprotocol.io (read Architecture, Transports, and Tools; the spec versions are dated, this server targets 2025-06-18 semantics via the SDK)
- Go SDK: https://github.com/modelcontextprotocol/go-sdk and API docs at https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk/mcp
- Debugging client: https://github.com/modelcontextprotocol/inspector (run via `make inspector`)
- Registry ecosystem: https://registry.modelcontextprotocol.io (the server.json format)

How this repo uses it: `mcp.AddTool(server, &mcp.Tool{...}, handler)` with a typed handler `func(ctx, req, In) (*mcp.CallToolResult, Out, error)`. The SDK generates the JSON Schema for tool inputs from the In struct: `json` tags name the fields, omitempty marks them optional, and the `jsonschema` struct tag becomes the field description the model reads. Those descriptions are prompt engineering; treat them as carefully as code. Returning a Go error becomes a tool error the model sees, so error strings should tell the model what to try next.

Transports: stdio (`mcp.StdioTransport`, newline-delimited JSON over stdin/stdout, so never write logs to stdout in stdio mode) and streamable HTTP (`mcp.NewStreamableHTTPHandler` with `Stateless: true`, meaning no session state and any replica can serve any request).

Tool annotations (readOnlyHint, destructiveHint, idempotentHint) are hints to client policy engines. Keep them honest; server.go centralizes them.

## Concept 2: client-go and the dynamic client

This server deliberately avoids typed clients and the kubedb.dev/apimachinery dependency. Everything flows through three client-go pieces:

1. Discovery (`ServerPreferredResources`): asks the API server what resource types exist. We filter to groups ending in kubedb.com and cache the result (`memory.NewMemCacheClient`). This is how the server stays correct across KubeDB releases without recompiling.
2. Dynamic client (`dynamic.Interface`): CRUD on arbitrary GVRs using `unstructured.Unstructured` (a map[string]any with helpers). See `unstructured.NestedString` and friends used throughout the tool files.
3. clientcmd: kubeconfig loading rules (KUBECONFIG env, ~/.kube/config, explicit path) plus context overrides, falling back to `rest.InClusterConfig` inside a pod.

Key vocabulary if you have used kubectl but not client-go: GVK is group/version/kind (what a manifest says), GVR is group/version/resource (the plural in the URL path, what the dynamic client needs). `FindKind` in client.go is our GVK-to-GVR resolver built on discovery instead of a RESTMapper.

Learn:

- client-go docs: https://pkg.go.dev/k8s.io/client-go and examples at https://github.com/kubernetes/client-go/tree/master/examples (the dynamic-create-update-delete-deployment example is the closest to what we do)
- API concepts (GVK/GVR, server side apply, dry-run): https://kubernetes.io/docs/reference/using-api/api-concepts/
- Server side apply specifically: https://kubernetes.io/docs/reference/using-api/server-side-apply/ (apply_manifest uses `types.ApplyPatchType` with FieldManager "kubedb-mcp" and Force, which is the idempotent create-or-update primitive)
- Unstructured helpers: https://pkg.go.dev/k8s.io/apimachinery/pkg/apis/meta/v1/unstructured

## Concept 3: KubeDB's API surface

KubeDB splits its API across groups, and the tool design mirrors that split:

| Group | What lives there | Tools that touch it |
|-------|------------------|---------------------|
| kubedb.com | The database kinds (Postgres, MongoDB, ... 35+) | list/create/halt/health/connection |
| ops.kubedb.com | `<Kind>OpsRequest`: declarative day-2 operations | all ops tools |
| catalog.kubedb.com | `<Kind>Version`: allowed versions, images, updateConstraints | list_versions, update_version validation |
| autoscaling.kubedb.com | `<Kind>Autoscaler` | configure_autoscaler |
| schema.kubedb.com | Schema manager (per-database logical DBs, users via Vault) | generic tools |
| kafka.kubedb.com | ConnectCluster, Connector, SchemaRegistry, RestProxy | generic tools |
| postgres.kubedb.com | Publisher, Subscriber (logical replication) | generic tools |
| archiver.kubedb.com | Point-in-time archiving | generic tools |
| elasticsearch.kubedb.com | ElasticsearchDashboard (Kibana etc.) | generic tools |
| ui.kubedb.com | Read-only insight resources (served by an aggregated API server) | generic tools, future first-class tools |

The day-2 model is the single most important thing to internalize: you do not edit a database spec to upgrade or scale it. You create an OpsRequest object (`spec.databaseRef`, `spec.type`, plus one type-specific section) and the enterprise operator executes it safely. The shared ops types are UpdateVersion, HorizontalScaling, VerticalScaling, VolumeExpansion, Restart, Reconfigure, ReconfigureTLS, RotateAuth, StorageMigration, plus engine specific extras. `internal/k8s/registry.go` maps each type to its spec key; payload shapes vary per engine.

Learn:

- User docs: https://kubedb.com/docs/latest/ (read the guides for one engine end to end, Postgres or MongoDB, including the ops request and autoscaler sections)
- Source of truth for spec shapes: https://github.com/kubedb/apimachinery, locally at `$GOPATH/src/kubedb.dev/apimachinery`. The `apis/ops/v1alpha1/*_ops_types.go` files define exactly which fields each OpsRequest type accepts for each engine; grep there before extending any ops tool.
- Concepts pages for OpsRequest lifecycle and deletionPolicy semantics (Delete vs WipeOut vs Halt vs DoNotTerminate), which matter for delete_resource messaging.

Useful labels the server relies on: KubeDB stamps offshoot pods and services with `app.kubernetes.io/instance=<db-name>` and `app.kubernetes.io/managed-by=kubedb.com`; database_health and get_connection_info select on these.

## Concept 4: the OpenShift AI MCP ecosystem

The deployment story targets the MCP catalog in Red Hat OpenShift AI 3.4+:

- MCP lifecycle operator (deploys catalog servers, owns the `mcp.x-k8s.io/v1alpha1` MCPServer CRD): https://github.com/kubernetes-sigs/mcp-lifecycle-operator and https://mcp-lifecycle-operator.sigs.k8s.io
- MCP gateway (identity-aware routing, per-tool metrics): https://github.com/Kuadrant/mcp-gateway
- Catalog announcement and requirements: https://www.redhat.com/en/blog/mcp-catalog-here-discover-deploy-and-connect-red-hat-openshift-ai
- UBI base images: https://catalog.redhat.com/software/base-images and certification via https://connect.redhat.com

Constraints that flow back into this codebase: streamable HTTP must be the in-cluster transport, /healthz must answer for probes, the image must build on UBI, run non root with a read only root filesystem, carry the certification labels, and ship its license in /licenses. If you change ports or paths, update Dockerfile and deploy/openshift/mcpserver.yaml together.

## How the pieces fit: life of a tool call

`kubedb_update_version` end to end:

1. SDK validates the input JSON against the schema generated from `UpdateVersionInput`, unmarshals, calls the handler.
2. Handler gets a client set from the factory (cached per kubeconfig context).
3. Catalog pre-check: FindKind("PostgresVersion") via discovery, list, verify the target exists and is not deprecated; on failure return an error listing valid names.
4. `createOpsRequest` resolves the database kind and the PostgresOpsRequest kind, verifies the target database exists, builds an unstructured object with `spec.type: UpdateVersion` and `spec.updateVersion.targetVersion`, and creates it (honoring dryRun).
5. Result text tells the model how to track progress, closing the loop.

Safety gates sit at two levels: registration (read-only mode never registers write tools; see server.New) and runtime (guardWrite, guardDestructive, the confirm flag, the credentials double opt-in).

## Common tasks

### Add a new tool

1. Define the input struct in the right `internal/tools/*.go` file. Every optional field gets omitempty; every field gets a `jsonschema` description written for a model, with examples.
2. Write the handler method on Toolset. Call guardWrite or guardDestructive first if it mutates. Return actionable errors.
3. Register it in `internal/server/server.go` with a name prefixed `kubedb_`, a description that says when to use it, and honest annotations. Place it inside the right safety block.
4. Update the smoke test expectations in TEST.md (tool counts) and the README table.

### Support a new engine or ops type

Usually nothing to do: engines arrive via discovery automatically. For a new ops type, add it to `OpsRequestTypes` and `OpsSpecKey` in registry.go after checking the field name in apimachinery's ops types file.

### Bump dependencies

```bash
go get github.com/modelcontextprotocol/go-sdk@latest
go get k8s.io/client-go@latest k8s.io/apimachinery@latest k8s.io/api@latest
go mod tidy && go build ./... && go vet ./...
```

k8s.io modules version-lock together (v0.36.x line); bump them as a set. After an SDK bump, re-run the TEST.md section 2 smoke tests; the SDK owns the wire format.

### Debug protocol traffic

stdio: pipe raw JSON-RPC lines as in TEST.md section 2.1, or set the client to log frames. HTTP: curl with `Accept: application/json, text/event-stream` and read the SSE events. Remember stdout is the protocol channel in stdio mode; all logging goes to stderr.

### Local cluster loop

```bash
kind create cluster
helm install kubedb oci://ghcr.io/appscode-charts/kubedb -n kubedb --create-namespace \
  --set-file global.license=license.txt
make inspector       # point tools at the kind context
```

## Conventions

Errors must tell the model the next step, not just what failed. Tool descriptions and jsonschema tags are part of the interface; review them like API changes. Keep tool count lean: prefer extending a generic tool over adding near-duplicates, since every tool consumes client context. House style for all prose (docs, comments, commit messages): no em-dashes; use commas, periods, colons, or parentheses.

## Link index

| Topic | Link |
|-------|------|
| MCP spec and docs | https://modelcontextprotocol.io |
| MCP Go SDK | https://github.com/modelcontextprotocol/go-sdk |
| MCP Inspector | https://github.com/modelcontextprotocol/inspector |
| MCP registry | https://registry.modelcontextprotocol.io |
| client-go | https://pkg.go.dev/k8s.io/client-go |
| K8s API concepts | https://kubernetes.io/docs/reference/using-api/api-concepts/ |
| Server side apply | https://kubernetes.io/docs/reference/using-api/server-side-apply/ |
| KubeDB docs | https://kubedb.com/docs/latest/ |
| KubeDB apimachinery | https://github.com/kubedb/apimachinery |
| MCP lifecycle operator | https://github.com/kubernetes-sigs/mcp-lifecycle-operator |
| MCP gateway | https://github.com/Kuadrant/mcp-gateway |
| OpenShift AI MCP catalog | https://www.redhat.com/en/blog/mcp-catalog-here-discover-deploy-and-connect-red-hat-openshift-ai |
| UBI images | https://catalog.redhat.com/software/base-images |
| Red Hat Partner Connect | https://connect.redhat.com |
| Effective Go | https://go.dev/doc/effective_go |
