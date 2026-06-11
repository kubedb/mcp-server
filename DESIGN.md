# KubeDB MCP Server: Design

## Goals

Give AI agents (Claude, gen AI studio agents, IDE assistants) a safe, complete interface to KubeDB across its whole surface: provisioning, day-2 operations, autoscaling, data plane resources, and observability. Ship one binary that works on a laptop against a kubeconfig and in-cluster behind the OpenShift AI MCP gateway.

## Architecture

```
                    stdio (local)            streamable HTTP, stateless (in-cluster)
                         |                                  |
                         +----------- mcp.Server ----------+
                         |   internal/server: tool registry,
                         |   safety gating, annotations
                         |
                  internal/tools: 20 tool handlers
                         |
                  internal/k8s: client factory
                  per-context cache { dynamic, typed, discovery }
                         |
                  Kubernetes API server(s)
```

Roughly 1,800 lines of Go. Dependencies: the official MCP Go SDK, client-go, apimachinery, sigs.k8s.io/yaml. No dependency on kubedb.dev/apimachinery.

## Key decisions

### Dynamic client + API discovery, not typed clients

The server resolves kinds at runtime by filtering API discovery to groups matching `kubedb.com` and `*.kubedb.com`, then drives everything through the dynamic client and unstructured objects.

Why: KubeDB spans 11 API groups, 100+ kinds, and multiple versions (v1, v1alpha1, v1alpha2) that move at their own pace. Importing apimachinery types would pin the server to one KubeDB release, bloat the binary, and require a rebuild for every new engine. Discovery keeps it version agnostic: when KubeDB adds a kind (Db2, Oracle, HanaDB, Milvus all arrived recently), the server picks it up with zero code changes. The trade-off, weaker compile-time guarantees, is mitigated by server side validation (every write supports dryRun) and by the operator's own admission webhooks.

Kind resolution is case-insensitive, retries once with a discovery cache refresh (newly installed CRDs), and on failure returns the installed kinds so the agent can self-correct.

### Generic tools + workflow tools

Two layers, per the MCP guidance of comprehensive coverage first:

1. Five generic tools (list_resources, get_resource, apply_manifest, patch_resource, delete_resource) cover every KubeDB family resource, including the data plane groups (schema.kubedb.com, kafka.kubedb.com, postgres.kubedb.com, archiver.kubedb.com). Nothing in the KubeDB surface is unreachable.
2. Workflow tools encode the common paths with guardrails: create_database builds a minimal valid spec, update_version validates the target against the catalog and rejects deprecated versions, scale and expand_volume map simple arguments onto the right OpsRequest shape, list_databases and database_health return curated summaries instead of full manifests to conserve agent context.

Ops request payload shapes vary per engine (a MongoDB horizontal scale is `{"shard":{...}}`, a Postgres one is `{"replicas":N}`), so workflow tools accept a raw JSON `spec` argument as an override alongside their typed shortcuts, and kubedb_create_ops_request exposes every type generically, including engine specific ones (ForceFailOver, ReplaceSentinel, Horizons).

### Day-2 operations go through OpsRequests, not spec edits

Restart, upgrade, scale, and expand all create `*OpsRequest` objects rather than patching the database spec. This matches the operator's intended workflow: the enterprise operator executes them with ordering, health checks, and rollback semantics, and the request object itself becomes the audit trail (visible via kubedb_list_ops_requests).

### Multi-cluster via per-call context

Every tool takes an optional `context` parameter naming a kubeconfig context; the factory caches a client set per context. In-cluster mode has one implicit context from the service account. This serves the dev workflow (one Claude session spanning staging and prod read-only) without inventing a registration mechanism.

### Safety model

Layered, because agent mistakes are a when not an if:

1. Server scope: writes are refused for any group outside the KubeDB family, checked against the manifest's apiVersion. The server cannot be talked into touching Deployments or Secrets (the only secret read is the opt-in credentials path).
2. Startup posture: --read-only registers 9 of 20 tools; --disable-destructive removes delete and blocks halting. Unregistered tools are invisible to the client, not merely rejected.
3. Call-level gates: delete requires confirm=true; credentials require both the server flag and the per-call opt-in; every write accepts dryRun.
4. Honest annotations: readOnlyHint and destructiveHint are set accurately per tool so client-side policy engines (and the MCP gateway's audit layer) can act on them.
5. RBAC floor: the server never escalates; kubeconfig user or service account permissions bound everything. Shipped cluster roles come in read, full, and credentials tiers.

### Transports

stdio for local agent use. Streamable HTTP in stateless JSON mode for in-cluster: no session affinity needed, horizontal scaling is trivial, and it matches the OpenShift AI MCP catalog requirement and the MCP lifecycle operator's defaults (/mcp endpoint, /healthz probe). Authentication for the HTTP path is delegated to the platform (MCP gateway, OAuth proxy, or NetworkPolicy) in v0.1; native OAuth resource server support is future work.

## Error handling

Errors name the object and operation, then point at the next action: unknown kinds list what is installed, a missing version lists the catalog, missing KubeDB CRDs link to the install docs, withheld credentials explain the flag and the kubectl fallback. The goal is that the agent's retry after an error usually succeeds.

## OpenShift AI MCP catalog fit

Requirements derived from the OpenShift AI 3.4 catalog (developer preview, May 2026): streamable HTTP transport, UBI based scanned images, deployment via the MCP lifecycle operator (mcp.x-k8s.io/v1alpha1 MCPServer), connectivity via the MCP gateway. The repo carries the conformant Dockerfile (ubi9-micro, non root, /licenses, certification labels), an MCPServer CR with restricted-SCC security context and probes, tiered RBAC, and a registry-format server.json. Onboarding goes through AppsCode's existing Red Hat Partner Connect account: container certification first, then the MCP catalog partner pipeline (consent + scanning).

## Testing

Verified in this iteration: clean build and vet; stdio handshake and tools/list (20 tools; 9 in read-only mode; 19 with destructive disabled); HTTP transport serving initialize on /mcp and ok on /healthz. Against a live cluster, the MCP Inspector (make inspector) exercises tools interactively.

Future: an eval suite per the MCP builder methodology (10 read-only multi-step questions against a kind cluster running KubeDB with seeded databases), golden tests for OpsRequest construction across engines, and a CI matrix against the last three KubeDB releases.

## Future work

ui.kubedb.com insight tools (slow queries, schema overviews) surfaced as first class read tools when the UI server is installed; KubeStash backup and restore tools; OAuth resource server metadata for direct HTTP auth; MCP resources exposing the version catalog; prompts encoding runbooks (upgrade with pre-flight checks, incident triage).
