package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"

	"kubedb.dev/mcp-server/internal/k8s"
)

// ---- kubedb_create_database ----

type CreateDatabaseInput struct {
	Context        string `json:"context,omitempty" jsonschema:"Kubeconfig context to target. Omit to use the current context or in-cluster config."`
	Kind           string `json:"kind" jsonschema:"Database kind to create, e.g. MongoDB, Postgres, MySQL, Redis, Kafka, Druid, RabbitMQ."`
	Name           string `json:"name" jsonschema:"Name for the new database object."`
	Namespace      string `json:"namespace,omitempty" jsonschema:"Target namespace. Defaults to default."`
	Version        string `json:"version" jsonschema:"Catalog version name, e.g. 16.4 for Postgres or 8.0.35 for MySQL. Use kubedb_list_versions to find valid names."`
	Replicas       *int   `json:"replicas,omitempty" jsonschema:"Replica count for non topology deployments. Omit for the kind's default."`
	StorageSize    string `json:"storageSize,omitempty" jsonschema:"Persistent volume size, e.g. 10Gi."`
	StorageClass   string `json:"storageClass,omitempty" jsonschema:"StorageClass name. Omit for the cluster default."`
	DeletionPolicy string `json:"deletionPolicy,omitempty" jsonschema:"One of Delete, WipeOut, Halt, DoNotTerminate. Defaults to the KubeDB default (Delete)."`
	SpecPatch      string `json:"specPatch,omitempty" jsonschema:"JSON object deep merged into the generated spec for advanced fields, e.g. {\"topology\":{...}} or {\"tls\":{...}} or {\"monitor\":{...}}."`
	DryRun         bool   `json:"dryRun,omitempty" jsonschema:"Validate against the API server without persisting."`
}

func (t *Toolset) CreateDatabase(ctx context.Context, req *mcp.CallToolRequest, in CreateDatabaseInput) (*mcp.CallToolResult, any, error) {
	if err := t.guardWrite(); err != nil {
		return nil, nil, err
	}
	c, err := t.clients(in.Context)
	if err != nil {
		return nil, nil, err
	}
	info, err := c.FindKind(in.Kind)
	if err != nil {
		return nil, nil, err
	}
	if info.Group != k8s.GroupKubeDB {
		return nil, nil, fmt.Errorf("kind %s is not a database kind; use kubedb_apply_manifest for %s resources", info.Kind, info.Group)
	}
	if in.Version == "" {
		return nil, nil, fmt.Errorf("version is required. Use kubedb_list_versions with kind=%s to pick one", info.Kind)
	}
	if in.DeletionPolicy != "" && !slices.Contains(k8s.DeletionPolicies, in.DeletionPolicy) {
		return nil, nil, fmt.Errorf("invalid deletionPolicy %q. Valid values: %s", in.DeletionPolicy, strings.Join(k8s.DeletionPolicies, ", "))
	}

	spec := map[string]any{"version": in.Version}
	if in.Replicas != nil {
		spec["replicas"] = int64(*in.Replicas)
	}
	if in.StorageSize != "" {
		storage := map[string]any{
			"accessModes": []any{"ReadWriteOnce"},
			"resources":   map[string]any{"requests": map[string]any{"storage": in.StorageSize}},
		}
		if in.StorageClass != "" {
			storage["storageClassName"] = in.StorageClass
		}
		spec["storage"] = storage
		spec["storageType"] = "Durable"
	}
	if in.DeletionPolicy != "" {
		spec["deletionPolicy"] = in.DeletionPolicy
	}
	if patch, err := parseJSONObject("specPatch", in.SpecPatch); err != nil {
		return nil, nil, err
	} else if patch != nil {
		spec = deepMerge(spec, patch)
	}

	ns := namespaceFor(info, in.Namespace)
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": info.APIVersion(),
		"kind":       info.Kind,
		"metadata":   map[string]any{"name": in.Name, "namespace": ns},
		"spec":       spec,
	}}

	created, err := c.Dynamic.Resource(info.GVR()).Namespace(ns).Create(ctx, obj, metav1.CreateOptions{DryRun: dryRunOpts(in.DryRun)})
	if err != nil {
		return nil, nil, fmt.Errorf("creating %s %s/%s: %w", info.Kind, ns, in.Name, err)
	}
	msg := fmt.Sprintf("Created %s %s/%s (version %s).", info.Kind, ns, created.GetName(), in.Version)
	if in.DryRun {
		msg = fmt.Sprintf("Dry run OK: %s %s/%s passed server side validation. Re-run with dryRun=false to create it.", info.Kind, ns, in.Name)
	} else {
		msg += " Track progress with kubedb_database_health; the phase moves from Provisioning to Ready."
	}
	return textResult(msg), nil, nil
}

// ---- kubedb_apply_manifest ----

type ApplyManifestInput struct {
	Context  string `json:"context,omitempty" jsonschema:"Kubeconfig context to target. Omit to use the current context or in-cluster config."`
	Manifest string `json:"manifest" jsonschema:"One or more YAML or JSON manifests (separated by ---). Every document must belong to a KubeDB API group (kubedb.com, ops.kubedb.com, catalog.kubedb.com, autoscaling.kubedb.com, schema.kubedb.com, archiver.kubedb.com, kafka.kubedb.com, postgres.kubedb.com, elasticsearch.kubedb.com)."`
	DryRun   bool   `json:"dryRun,omitempty" jsonschema:"Validate against the API server without persisting."`
}

func (t *Toolset) ApplyManifest(ctx context.Context, req *mcp.CallToolRequest, in ApplyManifestInput) (*mcp.CallToolResult, any, error) {
	if err := t.guardWrite(); err != nil {
		return nil, nil, err
	}
	c, err := t.clients(in.Context)
	if err != nil {
		return nil, nil, err
	}
	decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader([]byte(in.Manifest)), 4096)
	var applied []string
	for {
		var obj map[string]any
		if err := decoder.Decode(&obj); err != nil {
			if err == io.EOF {
				break
			}
			return nil, nil, fmt.Errorf("parsing manifest: %w", err)
		}
		if len(obj) == 0 {
			continue
		}
		u := &unstructured.Unstructured{Object: obj}
		gv, err := schema.ParseGroupVersion(u.GetAPIVersion())
		if err != nil || u.GetKind() == "" || u.GetName() == "" {
			return nil, nil, fmt.Errorf("each document needs apiVersion, kind, and metadata.name")
		}
		if !k8s.IsKubeDBGroup(gv.Group) {
			return nil, nil, fmt.Errorf("refusing to apply %s/%s: group %q is outside the KubeDB family. This server only manages KubeDB resources", u.GetKind(), u.GetName(), gv.Group)
		}
		info, err := c.FindKind(u.GetKind())
		if err != nil {
			return nil, nil, err
		}
		gvr := schema.GroupVersionResource{Group: gv.Group, Version: gv.Version, Resource: info.Resource}
		ns := ""
		if info.Namespaced {
			ns = u.GetNamespace()
			if ns == "" {
				ns = "default"
				u.SetNamespace(ns)
			}
		}
		data, err := json.Marshal(u.Object)
		if err != nil {
			return nil, nil, err
		}
		force := true
		res, err := c.Dynamic.Resource(gvr).Namespace(ns).Patch(ctx, u.GetName(), types.ApplyPatchType, data, metav1.PatchOptions{
			FieldManager: "kubedb-mcp",
			Force:        &force,
			DryRun:       dryRunOpts(in.DryRun),
		})
		if err != nil {
			return nil, nil, fmt.Errorf("applying %s %s/%s: %w", u.GetKind(), ns, u.GetName(), err)
		}
		applied = append(applied, fmt.Sprintf("%s %s/%s", res.GetKind(), ns, res.GetName()))
	}
	if len(applied) == 0 {
		return nil, nil, fmt.Errorf("no manifests found in input")
	}
	verb := "Applied"
	if in.DryRun {
		verb = "Dry run OK for"
	}
	return textResult(fmt.Sprintf("%s %d object(s):\n%s", verb, len(applied), strings.Join(applied, "\n"))), nil, nil
}

// ---- kubedb_patch_resource ----

type PatchResourceInput struct {
	Context   string `json:"context,omitempty" jsonschema:"Kubeconfig context to target. Omit to use the current context or in-cluster config."`
	Kind      string `json:"kind" jsonschema:"Any KubeDB family kind."`
	Name      string `json:"name" jsonschema:"Object name."`
	Namespace string `json:"namespace,omitempty" jsonschema:"Object namespace. Defaults to default for namespaced kinds."`
	Patch     string `json:"patch" jsonschema:"The patch body. For merge type, a JSON object such as {\"spec\":{\"replicas\":5}}. For json type, a JSON Patch array."`
	PatchType string `json:"patchType,omitempty" jsonschema:"merge (default) or json."`
	DryRun    bool   `json:"dryRun,omitempty" jsonschema:"Validate against the API server without persisting."`
}

func (t *Toolset) PatchResource(ctx context.Context, req *mcp.CallToolRequest, in PatchResourceInput) (*mcp.CallToolResult, any, error) {
	if err := t.guardWrite(); err != nil {
		return nil, nil, err
	}
	c, err := t.clients(in.Context)
	if err != nil {
		return nil, nil, err
	}
	info, err := c.FindKind(in.Kind)
	if err != nil {
		return nil, nil, err
	}
	pt := types.MergePatchType
	switch strings.ToLower(in.PatchType) {
	case "", "merge":
	case "json":
		pt = types.JSONPatchType
	default:
		return nil, nil, fmt.Errorf("unsupported patchType %q: use merge or json", in.PatchType)
	}
	ns := namespaceFor(info, in.Namespace)
	res, err := c.Dynamic.Resource(info.GVR()).Namespace(ns).Patch(ctx, in.Name, pt, []byte(in.Patch), metav1.PatchOptions{DryRun: dryRunOpts(in.DryRun)})
	if err != nil {
		return nil, nil, fmt.Errorf("patching %s %s/%s: %w", info.Kind, ns, in.Name, err)
	}
	verb := "Patched"
	if in.DryRun {
		verb = "Dry run OK for"
	}
	return textResult(fmt.Sprintf("%s %s %s/%s (resourceVersion %s).", verb, info.Kind, ns, res.GetName(), res.GetResourceVersion())), nil, nil
}

// ---- kubedb_delete_resource ----

type DeleteResourceInput struct {
	Context   string `json:"context,omitempty" jsonschema:"Kubeconfig context to target. Omit to use the current context or in-cluster config."`
	Kind      string `json:"kind" jsonschema:"Any KubeDB family kind."`
	Name      string `json:"name" jsonschema:"Object name."`
	Namespace string `json:"namespace,omitempty" jsonschema:"Object namespace. Defaults to default for namespaced kinds."`
	Confirm   bool   `json:"confirm" jsonschema:"Must be true. Acts as an explicit confirmation gate for deletion."`
}

func (t *Toolset) DeleteResource(ctx context.Context, req *mcp.CallToolRequest, in DeleteResourceInput) (*mcp.CallToolResult, any, error) {
	if err := t.guardDestructive(); err != nil {
		return nil, nil, err
	}
	if !in.Confirm {
		return nil, nil, fmt.Errorf("deletion requires confirm=true. Note: what happens to data depends on the object's deletionPolicy (Delete keeps backups, WipeOut removes everything, Halt keeps PVCs and secrets)")
	}
	c, err := t.clients(in.Context)
	if err != nil {
		return nil, nil, err
	}
	info, err := c.FindKind(in.Kind)
	if err != nil {
		return nil, nil, err
	}
	ns := namespaceFor(info, in.Namespace)
	if err := c.Dynamic.Resource(info.GVR()).Namespace(ns).Delete(ctx, in.Name, metav1.DeleteOptions{}); err != nil {
		return nil, nil, fmt.Errorf("deleting %s %s/%s: %w", info.Kind, ns, in.Name, err)
	}
	return textResult(fmt.Sprintf("Deleted %s %s/%s.", info.Kind, ns, in.Name)), nil, nil
}

// ---- kubedb_halt_database ----

type HaltDatabaseInput struct {
	Context   string `json:"context,omitempty" jsonschema:"Kubeconfig context to target. Omit to use the current context or in-cluster config."`
	Kind      string `json:"kind" jsonschema:"Database kind, e.g. MongoDB, Postgres."`
	Name      string `json:"name" jsonschema:"Database object name."`
	Namespace string `json:"namespace,omitempty" jsonschema:"Database namespace. Defaults to default."`
	Halted    bool   `json:"halted" jsonschema:"true pauses the database (deletes pods, keeps PVCs and secrets). false resumes it."`
}

func (t *Toolset) HaltDatabase(ctx context.Context, req *mcp.CallToolRequest, in HaltDatabaseInput) (*mcp.CallToolResult, any, error) {
	if in.Halted {
		if err := t.guardDestructive(); err != nil {
			return nil, nil, err
		}
	} else if err := t.guardWrite(); err != nil {
		return nil, nil, err
	}
	c, err := t.clients(in.Context)
	if err != nil {
		return nil, nil, err
	}
	info, err := c.FindKind(in.Kind)
	if err != nil {
		return nil, nil, err
	}
	if info.Group != k8s.GroupKubeDB {
		return nil, nil, fmt.Errorf("kind %s is not a database kind", info.Kind)
	}
	ns := namespaceFor(info, in.Namespace)
	patch := fmt.Sprintf(`{"spec":{"halted":%t}}`, in.Halted)
	if _, err := c.Dynamic.Resource(info.GVR()).Namespace(ns).Patch(ctx, in.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		return nil, nil, fmt.Errorf("updating %s %s/%s: %w", info.Kind, ns, in.Name, err)
	}
	state := "halted (pods removed, storage retained)"
	if !in.Halted {
		state = "resuming"
	}
	return textResult(fmt.Sprintf("%s %s/%s is now %s.", info.Kind, ns, in.Name, state)), nil, nil
}
