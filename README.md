# KubeDB MCP Server

A [Model Context Protocol](https://modelcontextprotocol.io) server that lets AI agents manage [KubeDB](https://kubedb.com) databases running in Kubernetes and OpenShift clusters: Postgres, MySQL, MongoDB, Redis, Kafka, Elasticsearch, MariaDB, RabbitMQ, Druid, SingleStore and 25+ other engines.

Written in Go on the official MCP Go SDK. Single static binary, dual transport (stdio for local use, streamable HTTP for in-cluster deployment), discovery driven so it works with any KubeDB release and any subset of installed CRDs.

## What agents can do with it

Inventory and observe: list every database across kinds and namespaces with version, phase, replicas, and storage. Pull full manifests, health reports (conditions, pod readiness, warning events), connection endpoints, and the version catalog with upgrade constraints.

Provision and manage lifecycle: create databases with sensible defaults, apply full manifests with server side apply, patch specs, pause and resume, delete with an explicit confirmation gate.

Day-2 operations: every KubeDB OpsRequest type, with dedicated tools for the common paths (restart, version update with catalog validation, horizontal and vertical scaling, volume expansion) and a generic escape hatch for the rest (Reconfigure, ReconfigureTLS, RotateAuth, StorageMigration, Reprovision, database specific types). Plus compute and storage autoscaler configuration.

Data plane resources: schema manager databases (schema.kubedb.com), Kafka ConnectClusters and Connectors, Postgres Publishers and Subscribers, archivers, and Elasticsearch dashboards are all reachable through the generic resource tools.

## Tools

| Tool | Kind | Description |
|------|------|-------------|
| kubedb_list_kinds | read | Discover installed KubeDB API groups and kinds |
| kubedb_list_contexts | read | List kubeconfig contexts (multi-cluster) |
| kubedb_list_databases | read | Database inventory with compact summaries |
| kubedb_list_resources | read | List any KubeDB family resource |
| kubedb_get_resource | read | Full manifest of any object, YAML or JSON |
| kubedb_database_health | read | Phase, conditions, pods, warning events |
| kubedb_get_connection_info | read | Endpoints, auth secret, TLS state, optional credentials |
| kubedb_list_versions | read | Catalog versions with deprecation and upgrade constraints |
| kubedb_list_ops_requests | read | Day-2 operation status |
| kubedb_create_database | write | Guided provisioning with dryRun |
| kubedb_apply_manifest | write | Server side apply for any KubeDB manifest |
| kubedb_patch_resource | write | JSON merge patch or JSON Patch |
| kubedb_halt_database | write | Pause and resume |
| kubedb_create_ops_request | write | Generic OpsRequest builder, all types |
| kubedb_restart_database | write | Safe ordered restart |
| kubedb_update_version | write | Upgrade with catalog validation |
| kubedb_scale_database | write | Horizontal and vertical scaling |
| kubedb_expand_volume | write | Online and offline volume expansion |
| kubedb_configure_autoscaler | write | Compute and storage autoscaling |
| kubedb_delete_resource | destructive | Delete with confirm=true gate |

Every write tool supports dryRun for server side validation without persisting.

## Quick start (local, stdio)

```bash
go install kubedb.dev/mcp-server/cmd/kubedb-mcp@latest
```

Claude Code:

```bash
claude mcp add kubedb -- kubedb-mcp
```

Claude Desktop or any MCP client (uses your current kubeconfig context, switch per call via the context parameter):

```json
{
  "mcpServers": {
    "kubedb": {
      "command": "kubedb-mcp",
      "args": ["--allow-credentials"]
    }
  }
}
```

## Safety model

| Flag | Env | Effect |
|------|-----|--------|
| --read-only | KUBEDB_MCP_READ_ONLY | Only the 9 read tools are registered |
| --disable-destructive | KUBEDB_MCP_DISABLE_DESTRUCTIVE | Removes delete, blocks halt |
| --allow-credentials | KUBEDB_MCP_ALLOW_CREDENTIALS | Opt-in for credential decoding |

Defense in depth beyond the flags: the server refuses to touch any resource outside the `*.kubedb.com` API groups, deletion requires an explicit confirm=true argument, all tools carry accurate MCP annotations (readOnlyHint, destructiveHint) so clients can apply their own policies, and effective permissions are always bounded by the RBAC of the kubeconfig user or service account.

## In-cluster deployment (HTTP)

The container runs the streamable HTTP transport in stateless JSON mode on port 8080, serving MCP at `/mcp` and health at `/healthz`.

```bash
make image                       # UBI based build via Dockerfile
kubectl create ns kubedb-mcp
kubectl apply -f deploy/openshift/rbac.yaml
kubectl apply -f deploy/openshift/mcpserver.yaml   # MCP lifecycle operator CR
```

`deploy/openshift/mcpserver.yaml` targets the [MCP lifecycle operator](https://github.com/kubernetes-sigs/mcp-lifecycle-operator) (`mcp.x-k8s.io/v1alpha1`), the operator behind the OpenShift AI MCP catalog. On clusters without the operator, a plain Deployment plus Service works the same way; the binary needs nothing beyond the service account.

RBAC ships in three tiers: `kubedb-mcp-read` for observe-only deployments, `kubedb-mcp-full` for lifecycle and day-2 tools, and `kubedb-mcp-credentials` (secrets get) only if credential decoding is enabled.

## Red Hat OpenShift AI MCP catalog

The MCP catalog in OpenShift AI 3.4+ (AI hub) lists validated MCP servers that admins deploy through the MCP lifecycle operator and consume through the MCP gateway and gen AI studio. This server is built to meet the catalog's technical bar:

1. Streamable HTTP transport, stateless, load balancer friendly (`mcp.stateless: true`).
2. UBI based image: the Dockerfile builds on `ubi9/go-toolset` and ships on `ubi9-micro`, non root (UID 65532), read only root filesystem, restricted SCC compatible, license in `/licenses`.
3. Required image labels (name, vendor, version, release, summary, description) for Red Hat container certification scanning.
4. Health endpoint at `/healthz` and MCP at `/mcp`, matching the operator's defaults.

Onboarding path for the catalog listing:

1. Certify the image through [Red Hat Partner Connect](https://connect.redhat.com) container certification (AppsCode already maintains certified KubeDB operator images, so this reuses the existing partner account and pipeline). The certified image publishes to `registry.connect.redhat.com`.
2. Submit the server for MCP catalog inclusion through the OpenShift AI partner pipeline (partner consent plus technical scanning). Red Hat's validation covers provenance, vulnerability scanning, and transport conformance.
3. Optionally publish `server.json` to the [official MCP registry](https://registry.modelcontextprotocol.io) for discovery outside OpenShift; the file in this repo follows the registry schema.

## Development

```bash
make build       # static binary in bin/
make vet
make inspector   # interactive testing with the MCP Inspector
```

## License

Apache 2.0. Copyright AppsCode Inc.
