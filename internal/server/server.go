// Package server assembles the MCP server and registers the KubeDB toolset.
package server

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"kubedb.dev/mcp-server/internal/tools"
)

func ptr[T any](v T) *T { return &v }

func readOnly() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		ReadOnlyHint:  true,
		OpenWorldHint: ptr(false),
	}
}

func additive() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		DestructiveHint: ptr(false),
		OpenWorldHint:   ptr(false),
	}
}

func destructive(idempotent bool) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		DestructiveHint: ptr(true),
		IdempotentHint:  idempotent,
		OpenWorldHint:   ptr(false),
	}
}

// New builds the MCP server with every tool permitted by the configuration.
func New(ts *tools.Toolset, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{
		Name:    "kubedb",
		Title:   "KubeDB MCP Server",
		Version: version,
	}, &mcp.ServerOptions{
		Instructions: "Manage KubeDB databases (Postgres, MySQL, MongoDB, Redis, Kafka, Elasticsearch and 30+ other engines) running in Kubernetes. " +
			"Start with kubedb_list_kinds to see what is installed, kubedb_list_databases for inventory, and kubedb_database_health for status. " +
			"Day-2 operations (version updates, scaling, volume expansion, restarts, TLS, password rotation) are performed by creating OpsRequest objects; prefer the dedicated tools and fall back to kubedb_create_ops_request for advanced cases. " +
			"All write tools accept dryRun=true; use it to validate before mutating. Database kinds and exact spec shapes vary, so check kubedb_get_resource on an existing object when unsure.",
	})

	// Read and observe.
	mcp.AddTool(s, &mcp.Tool{
		Name:        "kubedb_list_kinds",
		Description: "Discover which KubeDB API groups and kinds are installed in the cluster (databases, ops requests, versions, autoscalers, schema managers, Kafka connect, archivers).",
		Annotations: readOnly(),
	}, ts.ListKinds)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "kubedb_list_contexts",
		Description: "List the kubeconfig contexts this server can target. Pass a context name to other tools to work across clusters.",
		Annotations: readOnly(),
	}, ts.ListContexts)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "kubedb_list_databases",
		Description: "List KubeDB databases with a compact summary (kind, version, phase, replicas, storage, age). Filter by kind, namespace, label selector, or phase.",
		Annotations: readOnly(),
	}, ts.ListDatabases)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "kubedb_list_resources",
		Description: "List any KubeDB family resource: ops requests, catalog versions, autoscalers, schema manager databases, Kafka ConnectClusters and Connectors, Postgres Publishers and Subscribers, archivers, dashboards.",
		Annotations: readOnly(),
	}, ts.ListResources)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "kubedb_get_resource",
		Description: "Get the full manifest of any KubeDB family object as YAML or JSON, including spec and status.",
		Annotations: readOnly(),
	}, ts.GetResource)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "kubedb_database_health",
		Description: "Health report for one database: phase, status conditions, per pod readiness and restarts, and recent warning events.",
		Annotations: readOnly(),
	}, ts.DatabaseHealth)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "kubedb_get_connection_info",
		Description: "Connection endpoints (service DNS names and ports), auth secret name, and TLS state for a database. Can optionally decode credentials when the server allows it.",
		Annotations: readOnly(),
	}, ts.ConnectionInfo)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "kubedb_list_versions",
		Description: "List the catalog versions available for a database kind, with deprecation flags and allowed upgrade paths (updateConstraints).",
		Annotations: readOnly(),
	}, ts.ListVersions)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "kubedb_list_ops_requests",
		Description: "List day-2 operation requests (upgrades, scaling, restarts, etc.) and their phases, optionally filtered by database kind, name, or namespace.",
		Annotations: readOnly(),
	}, ts.ListOpsRequests)

	if ts.Cfg.ReadOnly {
		return s
	}

	// Provisioning and lifecycle.
	mcp.AddTool(s, &mcp.Tool{
		Name:        "kubedb_create_database",
		Description: "Provision a new database with sensible defaults: kind, version, replicas, storage, deletion policy. Advanced fields go in specPatch. Supports dryRun.",
		Annotations: additive(),
	}, ts.CreateDatabase)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "kubedb_apply_manifest",
		Description: "Server-side apply one or more KubeDB family manifests (YAML or JSON). Use for full control over any resource: databases, schema manager, Kafka connectors, Postgres pub/sub, archivers, autoscalers. Refuses non KubeDB groups. Supports dryRun.",
		Annotations: destructive(true),
	}, ts.ApplyManifest)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "kubedb_patch_resource",
		Description: "Patch any KubeDB family object with a JSON merge patch or JSON Patch. Supports dryRun.",
		Annotations: destructive(false),
	}, ts.PatchResource)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "kubedb_halt_database",
		Description: "Pause (halted=true) or resume (halted=false) a database. Halting deletes pods but keeps storage and secrets.",
		Annotations: destructive(true),
	}, ts.HaltDatabase)

	// Day-2 operations.
	mcp.AddTool(s, &mcp.Tool{
		Name:        "kubedb_create_ops_request",
		Description: "Create any day-2 OpsRequest: UpdateVersion, HorizontalScaling, VerticalScaling, VolumeExpansion, Restart, Reconfigure, ReconfigureTLS, RotateAuth, StorageMigration, Reprovision, and database specific types. The escape hatch when a dedicated tool does not fit. Supports dryRun.",
		Annotations: destructive(false),
	}, ts.CreateOpsRequest)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "kubedb_restart_database",
		Description: "Smart restart of a database via a Restart OpsRequest. The operator restarts pods in a safe order, respecting replication topology.",
		Annotations: destructive(true),
	}, ts.RestartDatabase)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "kubedb_update_version",
		Description: "Upgrade or downgrade a database version via an UpdateVersion OpsRequest. Validates the target against the catalog first. Supports dryRun.",
		Annotations: destructive(false),
	}, ts.UpdateVersion)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "kubedb_scale_database",
		Description: "Scale a database horizontally (replicas, shards) or vertically (cpu, memory) via an OpsRequest. Supports dryRun.",
		Annotations: destructive(false),
	}, ts.ScaleDatabase)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "kubedb_expand_volume",
		Description: "Expand database storage volumes via a VolumeExpansion OpsRequest (Online or Offline). Supports dryRun.",
		Annotations: destructive(false),
	}, ts.ExpandVolume)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "kubedb_configure_autoscaler",
		Description: "Create or update a compute and/or storage autoscaler for a database. Supports dryRun.",
		Annotations: additive(),
	}, ts.ConfigureAutoscaler)

	if !ts.Cfg.DisableDestructive {
		mcp.AddTool(s, &mcp.Tool{
			Name:        "kubedb_delete_resource",
			Description: "Delete a KubeDB family object. Requires confirm=true. The database's deletionPolicy decides the fate of data: Delete keeps backups, WipeOut removes everything, Halt keeps PVCs and secrets.",
			Annotations: destructive(true),
		}, ts.DeleteResource)
	}
	return s
}
