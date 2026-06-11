package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"kubedb.dev/mcp-server/internal/k8s"
)

// createOpsRequest builds and creates a <Kind>OpsRequest object.
func (t *Toolset) createOpsRequest(ctx context.Context, contextName, dbKind, dbName, namespace, opsType, name string, payload map[string]any, timeout, apply string, dryRun bool) (*mcp.CallToolResult, error) {
	if err := t.guardWrite(); err != nil {
		return nil, err
	}
	c, err := t.clients(contextName)
	if err != nil {
		return nil, err
	}
	dbInfo, err := c.FindKind(dbKind)
	if err != nil {
		return nil, err
	}
	if dbInfo.Group != k8s.GroupKubeDB {
		return nil, fmt.Errorf("kind %s is not a database kind", dbInfo.Kind)
	}
	opsInfo, err := c.FindKind(k8s.OpsKindFor(dbInfo.Kind))
	if err != nil {
		return nil, fmt.Errorf("no ops request kind for %s: %w", dbInfo.Kind, err)
	}
	ns := namespaceFor(dbInfo, namespace)

	// Verify the target database exists before creating the ops request.
	if _, err := c.Dynamic.Resource(dbInfo.GVR()).Namespace(ns).Get(ctx, dbName, metav1.GetOptions{}); err != nil {
		return nil, fmt.Errorf("target database %s %s/%s not found: %w", dbInfo.Kind, ns, dbName, err)
	}

	if name == "" {
		name = fmt.Sprintf("%s-%s-%d", dbName, strings.ToLower(opsType), time.Now().Unix())
	}
	spec := map[string]any{
		"databaseRef": map[string]any{"name": dbName},
		"type":        opsType,
	}
	if payload != nil {
		key := k8s.OpsSpecKey[opsType]
		if key == "" {
			// Database specific types follow the lowerCamel convention.
			key = strings.ToLower(opsType[:1]) + opsType[1:]
		}
		spec[key] = payload
	}
	if timeout != "" {
		spec["timeout"] = timeout
	}
	if apply != "" {
		if apply != "IfReady" && apply != "Always" {
			return nil, fmt.Errorf("apply must be IfReady or Always")
		}
		spec["apply"] = apply
	}

	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": opsInfo.APIVersion(),
		"kind":       opsInfo.Kind,
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"spec":       spec,
	}}
	created, err := c.Dynamic.Resource(opsInfo.GVR()).Namespace(ns).Create(ctx, obj, metav1.CreateOptions{DryRun: dryRunOpts(dryRun)})
	if err != nil {
		return nil, fmt.Errorf("creating %s %s/%s: %w", opsInfo.Kind, ns, name, err)
	}
	if dryRun {
		return textResult(fmt.Sprintf("Dry run OK: %s %s/%s (type %s) passed validation.", opsInfo.Kind, ns, name, opsType)), nil
	}
	return textResult(fmt.Sprintf("Created %s %s/%s (type %s) targeting %s %s/%s. Track it with kubedb_list_ops_requests or kubedb_get_resource.", opsInfo.Kind, ns, created.GetName(), opsType, dbInfo.Kind, ns, dbName)), nil
}

// ---- kubedb_create_ops_request (generic) ----

type CreateOpsRequestInput struct {
	Context      string `json:"context,omitempty" jsonschema:"Kubeconfig context to target. Omit to use the current context or in-cluster config."`
	DatabaseKind string `json:"databaseKind" jsonschema:"Kind of the target database, e.g. MongoDB, Postgres, MySQL."`
	DatabaseName string `json:"databaseName" jsonschema:"Name of the target database object."`
	Namespace    string `json:"namespace,omitempty" jsonschema:"Namespace of the target database. Defaults to default."`
	Type         string `json:"type" jsonschema:"Operation type: UpdateVersion, HorizontalScaling, VerticalScaling, VolumeExpansion, Restart, Reconfigure, ReconfigureTLS, RotateAuth, StorageMigration, Reprovision, or a database specific type like ForceFailOver."`
	Name         string `json:"name,omitempty" jsonschema:"Name for the ops request object. Auto generated when omitted."`
	Spec         string `json:"spec,omitempty" jsonschema:"JSON object for the type specific payload, placed under the matching spec key. Examples: HorizontalScaling {\"replicas\":5}; VerticalScaling {\"node\":{\"resources\":{\"requests\":{\"cpu\":\"1\",\"memory\":\"2Gi\"}}}}; UpdateVersion {\"targetVersion\":\"8.0.35\"}; VolumeExpansion {\"mode\":\"Online\",\"postgres\":\"20Gi\"}. Not needed for Restart."`
	Timeout      string `json:"timeout,omitempty" jsonschema:"Give up after this duration, e.g. 10m."`
	Apply        string `json:"apply,omitempty" jsonschema:"IfReady (default, only act on a healthy database) or Always."`
	DryRun       bool   `json:"dryRun,omitempty" jsonschema:"Validate against the API server without persisting."`
}

func (t *Toolset) CreateOpsRequest(ctx context.Context, req *mcp.CallToolRequest, in CreateOpsRequestInput) (*mcp.CallToolResult, any, error) {
	payload, err := parseJSONObject("spec", in.Spec)
	if err != nil {
		return nil, nil, err
	}
	res, err := t.createOpsRequest(ctx, in.Context, in.DatabaseKind, in.DatabaseName, in.Namespace, in.Type, in.Name, payload, in.Timeout, in.Apply, in.DryRun)
	return res, nil, err
}

// ---- kubedb_restart_database ----

type RestartDatabaseInput struct {
	Context      string `json:"context,omitempty" jsonschema:"Kubeconfig context to target. Omit to use the current context or in-cluster config."`
	DatabaseKind string `json:"databaseKind" jsonschema:"Kind of the target database, e.g. MongoDB, Postgres."`
	DatabaseName string `json:"databaseName" jsonschema:"Name of the target database object."`
	Namespace    string `json:"namespace,omitempty" jsonschema:"Namespace of the target database. Defaults to default."`
}

func (t *Toolset) RestartDatabase(ctx context.Context, req *mcp.CallToolRequest, in RestartDatabaseInput) (*mcp.CallToolResult, any, error) {
	res, err := t.createOpsRequest(ctx, in.Context, in.DatabaseKind, in.DatabaseName, in.Namespace, "Restart", "", nil, "", "", false)
	return res, nil, err
}

// ---- kubedb_update_version ----

type UpdateVersionInput struct {
	Context       string `json:"context,omitempty" jsonschema:"Kubeconfig context to target. Omit to use the current context or in-cluster config."`
	DatabaseKind  string `json:"databaseKind" jsonschema:"Kind of the target database, e.g. Postgres, MySQL."`
	DatabaseName  string `json:"databaseName" jsonschema:"Name of the target database object."`
	Namespace     string `json:"namespace,omitempty" jsonschema:"Namespace of the target database. Defaults to default."`
	TargetVersion string `json:"targetVersion" jsonschema:"Catalog version name to upgrade to, e.g. 16.4. Must exist in the catalog; check with kubedb_list_versions."`
	DryRun        bool   `json:"dryRun,omitempty" jsonschema:"Validate against the API server without persisting."`
}

func (t *Toolset) UpdateVersion(ctx context.Context, req *mcp.CallToolRequest, in UpdateVersionInput) (*mcp.CallToolResult, any, error) {
	c, err := t.clients(in.Context)
	if err != nil {
		return nil, nil, err
	}
	rows, err := t.versionRows(ctx, c, in.DatabaseKind)
	if err == nil {
		found := false
		var names []string
		for _, r := range rows {
			names = append(names, r.Name)
			if r.Name == in.TargetVersion {
				found = true
				if r.Deprecated {
					return nil, nil, fmt.Errorf("version %s is marked deprecated in the catalog; pick another with kubedb_list_versions", in.TargetVersion)
				}
			}
		}
		if !found {
			sort.Strings(names)
			return nil, nil, fmt.Errorf("version %q not found in the %s catalog. Available: %s", in.TargetVersion, in.DatabaseKind, strings.Join(names, ", "))
		}
	}
	res, err := t.createOpsRequest(ctx, in.Context, in.DatabaseKind, in.DatabaseName, in.Namespace, "UpdateVersion", "", map[string]any{"targetVersion": in.TargetVersion}, "", "", in.DryRun)
	return res, nil, err
}

// ---- kubedb_scale_database ----

type ScaleDatabaseInput struct {
	Context      string `json:"context,omitempty" jsonschema:"Kubeconfig context to target. Omit to use the current context or in-cluster config."`
	DatabaseKind string `json:"databaseKind" jsonschema:"Kind of the target database, e.g. MongoDB, Postgres."`
	DatabaseName string `json:"databaseName" jsonschema:"Name of the target database object."`
	Namespace    string `json:"namespace,omitempty" jsonschema:"Namespace of the target database. Defaults to default."`
	Mode         string `json:"mode" jsonschema:"horizontal (change replica counts) or vertical (change cpu and memory)."`
	Replicas     *int   `json:"replicas,omitempty" jsonschema:"Shortcut for horizontal scaling of non topology databases. Sets {\"replicas\": N}."`
	Spec         string `json:"spec,omitempty" jsonschema:"JSON payload for the scaling section. Horizontal topology example for MongoDB: {\"shard\":{\"shards\":5,\"replicas\":3}}. Vertical example: {\"node\":{\"resources\":{\"requests\":{\"cpu\":\"1\",\"memory\":\"2Gi\"},\"limits\":{\"memory\":\"2Gi\"}}}}. Overrides replicas when both are set."`
	DryRun       bool   `json:"dryRun,omitempty" jsonschema:"Validate against the API server without persisting."`
}

func (t *Toolset) ScaleDatabase(ctx context.Context, req *mcp.CallToolRequest, in ScaleDatabaseInput) (*mcp.CallToolResult, any, error) {
	payload, err := parseJSONObject("spec", in.Spec)
	if err != nil {
		return nil, nil, err
	}
	var opsType string
	switch strings.ToLower(in.Mode) {
	case "horizontal":
		opsType = "HorizontalScaling"
		if payload == nil {
			if in.Replicas == nil {
				return nil, nil, fmt.Errorf("horizontal scaling needs either replicas or a spec payload (topology databases need a per component payload, e.g. {\"shard\":{\"shards\":5}})")
			}
			payload = map[string]any{"replicas": int64(*in.Replicas)}
		}
	case "vertical":
		opsType = "VerticalScaling"
		if payload == nil {
			return nil, nil, fmt.Errorf("vertical scaling needs a spec payload with per component resources, e.g. {\"node\":{\"resources\":{\"requests\":{\"cpu\":\"1\",\"memory\":\"2Gi\"}}}}")
		}
	default:
		return nil, nil, fmt.Errorf("mode must be horizontal or vertical")
	}
	res, err := t.createOpsRequest(ctx, in.Context, in.DatabaseKind, in.DatabaseName, in.Namespace, opsType, "", payload, "", "", in.DryRun)
	return res, nil, err
}

// ---- kubedb_expand_volume ----

type ExpandVolumeInput struct {
	Context      string `json:"context,omitempty" jsonschema:"Kubeconfig context to target. Omit to use the current context or in-cluster config."`
	DatabaseKind string `json:"databaseKind" jsonschema:"Kind of the target database, e.g. Postgres, MySQL, MongoDB."`
	DatabaseName string `json:"databaseName" jsonschema:"Name of the target database object."`
	Namespace    string `json:"namespace,omitempty" jsonschema:"Namespace of the target database. Defaults to default."`
	Mode         string `json:"mode,omitempty" jsonschema:"Online (default) or Offline. Online requires a CSI driver with online expansion support."`
	Size         string `json:"size,omitempty" jsonschema:"New volume size, e.g. 50Gi. Shortcut that targets the main database component. For topology databases use spec instead."`
	Spec         string `json:"spec,omitempty" jsonschema:"JSON payload for volumeExpansion with per component sizes, e.g. {\"mode\":\"Online\",\"shard\":\"50Gi\",\"configServer\":\"10Gi\"}. Overrides size when set."`
	DryRun       bool   `json:"dryRun,omitempty" jsonschema:"Validate against the API server without persisting."`
}

func (t *Toolset) ExpandVolume(ctx context.Context, req *mcp.CallToolRequest, in ExpandVolumeInput) (*mcp.CallToolResult, any, error) {
	payload, err := parseJSONObject("spec", in.Spec)
	if err != nil {
		return nil, nil, err
	}
	mode := in.Mode
	if mode == "" {
		mode = "Online"
	}
	if mode != "Online" && mode != "Offline" {
		return nil, nil, fmt.Errorf("mode must be Online or Offline")
	}
	if payload == nil {
		if in.Size == "" {
			return nil, nil, fmt.Errorf("provide size (simple databases) or spec (topology databases)")
		}
		payload = map[string]any{
			"mode": mode,
			strings.ToLower(in.DatabaseKind): in.Size,
		}
	} else if _, ok := payload["mode"]; !ok {
		payload["mode"] = mode
	}
	res, err := t.createOpsRequest(ctx, in.Context, in.DatabaseKind, in.DatabaseName, in.Namespace, "VolumeExpansion", "", payload, "", "", in.DryRun)
	return res, nil, err
}

// ---- kubedb_list_ops_requests ----

type ListOpsRequestsInput struct {
	Context      string `json:"context,omitempty" jsonschema:"Kubeconfig context to target. Omit to use the current context or in-cluster config."`
	DatabaseKind string `json:"databaseKind,omitempty" jsonschema:"Limit to one database kind, e.g. MongoDB. Omit to list ops requests for every kind."`
	DatabaseName string `json:"databaseName,omitempty" jsonschema:"Limit to ops requests targeting this database name."`
	Namespace    string `json:"namespace,omitempty" jsonschema:"Namespace to search. Omit for all namespaces."`
}

type OpsRequestRow struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Type      string `json:"type"`
	Database  string `json:"database"`
	Phase     string `json:"phase,omitempty"`
	Age       string `json:"age,omitempty"`
}

func (t *Toolset) ListOpsRequests(ctx context.Context, req *mcp.CallToolRequest, in ListOpsRequestsInput) (*mcp.CallToolResult, any, error) {
	c, err := t.clients(in.Context)
	if err != nil {
		return nil, nil, err
	}
	var kinds []k8s.ResourceInfo
	if in.DatabaseKind != "" {
		info, err := c.FindKind(k8s.OpsKindFor(in.DatabaseKind))
		if err != nil {
			return nil, nil, err
		}
		kinds = []k8s.ResourceInfo{info}
	} else {
		kinds, err = c.GroupKinds(k8s.GroupOps)
		if err != nil {
			return nil, nil, err
		}
	}
	var rows []OpsRequestRow
	var failures []string
	for _, info := range kinds {
		list, err := c.Dynamic.Resource(info.GVR()).Namespace(in.Namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", info.Kind, err))
			continue
		}
		for i := range list.Items {
			u := &list.Items[i]
			db := nestedString(u, "spec", "databaseRef", "name")
			if in.DatabaseName != "" && db != in.DatabaseName {
				continue
			}
			rows = append(rows, OpsRequestRow{
				Kind:      info.Kind,
				Name:      u.GetName(),
				Namespace: u.GetNamespace(),
				Type:      nestedString(u, "spec", "type"),
				Database:  db,
				Phase:     nestedString(u, "status", "phase"),
				Age:       age(u),
			})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Namespace != rows[j].Namespace {
			return rows[i].Namespace < rows[j].Namespace
		}
		return rows[i].Name < rows[j].Name
	})
	out := map[string]any{"count": len(rows), "opsRequests": rows}
	if len(failures) > 0 {
		out["errors"] = failures
	}
	res, err := jsonResult(out)
	return res, nil, err
}

// ---- kubedb_configure_autoscaler ----

type ConfigureAutoscalerInput struct {
	Context           string `json:"context,omitempty" jsonschema:"Kubeconfig context to target. Omit to use the current context or in-cluster config."`
	DatabaseKind      string `json:"databaseKind" jsonschema:"Kind of the target database, e.g. MongoDB, Postgres."`
	DatabaseName      string `json:"databaseName" jsonschema:"Name of the target database object."`
	Namespace         string `json:"namespace,omitempty" jsonschema:"Namespace of the target database. Defaults to default."`
	Name              string `json:"name,omitempty" jsonschema:"Autoscaler object name. Defaults to <databaseName>-autoscaler."`
	Compute           string `json:"compute,omitempty" jsonschema:"JSON object for compute autoscaling, e.g. {\"standalone\":{\"trigger\":\"On\",\"minAllowed\":{\"cpu\":\"500m\",\"memory\":\"1Gi\"},\"maxAllowed\":{\"cpu\":\"2\",\"memory\":\"4Gi\"},\"controlledResources\":[\"cpu\",\"memory\"]}}."`
	Storage           string `json:"storage,omitempty" jsonschema:"JSON object for storage autoscaling, e.g. {\"standalone\":{\"trigger\":\"On\",\"usageThreshold\":80,\"scalingThreshold\":50}}."`
	OpsRequestOptions string `json:"opsRequestOptions,omitempty" jsonschema:"JSON object for generated ops request options, e.g. {\"apply\":\"IfReady\",\"timeout\":\"5m\"}."`
	DryRun            bool   `json:"dryRun,omitempty" jsonschema:"Validate against the API server without persisting."`
}

func (t *Toolset) ConfigureAutoscaler(ctx context.Context, req *mcp.CallToolRequest, in ConfigureAutoscalerInput) (*mcp.CallToolResult, any, error) {
	if err := t.guardWrite(); err != nil {
		return nil, nil, err
	}
	compute, err := parseJSONObject("compute", in.Compute)
	if err != nil {
		return nil, nil, err
	}
	storage, err := parseJSONObject("storage", in.Storage)
	if err != nil {
		return nil, nil, err
	}
	opts, err := parseJSONObject("opsRequestOptions", in.OpsRequestOptions)
	if err != nil {
		return nil, nil, err
	}
	if compute == nil && storage == nil {
		return nil, nil, fmt.Errorf("provide compute and/or storage autoscaling configuration")
	}
	c, err := t.clients(in.Context)
	if err != nil {
		return nil, nil, err
	}
	dbInfo, err := c.FindKind(in.DatabaseKind)
	if err != nil {
		return nil, nil, err
	}
	asInfo, err := c.FindKind(k8s.AutoscalerKindFor(dbInfo.Kind))
	if err != nil {
		return nil, nil, fmt.Errorf("no autoscaler kind for %s: %w", dbInfo.Kind, err)
	}
	ns := namespaceFor(dbInfo, in.Namespace)
	name := in.Name
	if name == "" {
		name = in.DatabaseName + "-autoscaler"
	}
	spec := map[string]any{"databaseRef": map[string]any{"name": in.DatabaseName}}
	if compute != nil {
		spec["compute"] = compute
	}
	if storage != nil {
		spec["storage"] = storage
	}
	if opts != nil {
		spec["opsRequestOptions"] = opts
	}
	manifest := map[string]any{
		"apiVersion": asInfo.APIVersion(),
		"kind":       asInfo.Kind,
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"spec":       spec,
	}
	b, err := json.Marshal(manifest)
	if err != nil {
		return nil, nil, err
	}
	res, _, err := t.ApplyManifest(ctx, req, ApplyManifestInput{Context: in.Context, Manifest: string(b), DryRun: in.DryRun})
	return res, nil, err
}
