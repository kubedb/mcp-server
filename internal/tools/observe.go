package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"kubedb.dev/mcp-server/internal/k8s"
)

// ---- kubedb_list_kinds ----

type ListKindsInput struct {
	Context string `json:"context,omitempty" jsonschema:"Kubeconfig context to target. Omit to use the current context or in-cluster config."`
	Refresh bool   `json:"refresh,omitempty" jsonschema:"Refresh the API discovery cache before listing. Use after installing new KubeDB CRDs."`
}

func (t *Toolset) ListKinds(ctx context.Context, req *mcp.CallToolRequest, in ListKindsInput) (*mcp.CallToolResult, any, error) {
	c, err := t.clients(in.Context)
	if err != nil {
		return nil, nil, err
	}
	resources, err := c.KubeDBResources(in.Refresh)
	if err != nil {
		return nil, nil, err
	}
	grouped := map[string][]string{}
	for _, r := range resources {
		grouped[r.Group+"/"+r.Version] = append(grouped[r.Group+"/"+r.Version], r.Kind)
	}
	res, err := jsonResult(map[string]any{
		"apiGroups": grouped,
		"hint":      "Database kinds live in kubedb.com. Day-2 operations use ops.kubedb.com, versions use catalog.kubedb.com, autoscaling uses autoscaling.kubedb.com.",
	})
	return res, nil, err
}

// ---- kubedb_list_contexts ----

type ListContextsInput struct{}

func (t *Toolset) ListContexts(ctx context.Context, req *mcp.CallToolRequest, in ListContextsInput) (*mcp.CallToolResult, any, error) {
	contexts, err := t.Factory.ListContexts()
	if err != nil {
		return nil, nil, err
	}
	res, err := jsonResult(contexts)
	return res, nil, err
}

// ---- kubedb_list_databases ----

type ListDatabasesInput struct {
	Context       string `json:"context,omitempty" jsonschema:"Kubeconfig context to target. Omit to use the current context or in-cluster config."`
	Kind          string `json:"kind,omitempty" jsonschema:"Database kind to list, e.g. MongoDB, Postgres, MySQL, Redis, Kafka. Omit to list every installed database kind."`
	Namespace     string `json:"namespace,omitempty" jsonschema:"Namespace to search. Omit to search all namespaces."`
	LabelSelector string `json:"labelSelector,omitempty" jsonschema:"Kubernetes label selector to filter results, e.g. team=payments."`
	Phase         string `json:"phase,omitempty" jsonschema:"Filter by status phase: Provisioning, DataRestoring, Ready, Critical, NotReady, Halted, Unknown."`
}

type DatabaseSummary struct {
	Kind           string `json:"kind"`
	Name           string `json:"name"`
	Namespace      string `json:"namespace"`
	Version        string `json:"version,omitempty"`
	Phase          string `json:"phase,omitempty"`
	Replicas       *int64 `json:"replicas,omitempty"`
	Storage        string `json:"storage,omitempty"`
	DeletionPolicy string `json:"deletionPolicy,omitempty"`
	Halted         bool   `json:"halted,omitempty"`
	Age            string `json:"age,omitempty"`
}

func summarize(kind string, u *unstructured.Unstructured) DatabaseSummary {
	policy := nestedString(u, "spec", "deletionPolicy")
	if policy == "" {
		policy = nestedString(u, "spec", "terminationPolicy")
	}
	return DatabaseSummary{
		Kind:           kind,
		Name:           u.GetName(),
		Namespace:      u.GetNamespace(),
		Version:        nestedString(u, "spec", "version"),
		Phase:          nestedString(u, "status", "phase"),
		Replicas:       nestedInt(u, "spec", "replicas"),
		Storage:        nestedString(u, "spec", "storage", "resources", "requests", "storage"),
		DeletionPolicy: policy,
		Halted:         nestedBool(u, "spec", "halted"),
		Age:            age(u),
	}
}

func (t *Toolset) ListDatabases(ctx context.Context, req *mcp.CallToolRequest, in ListDatabasesInput) (*mcp.CallToolResult, any, error) {
	c, err := t.clients(in.Context)
	if err != nil {
		return nil, nil, err
	}
	var kinds []k8s.ResourceInfo
	if in.Kind != "" {
		info, err := c.FindKind(in.Kind)
		if err != nil {
			return nil, nil, err
		}
		if info.Group != k8s.GroupKubeDB {
			return nil, nil, fmt.Errorf("kind %s belongs to group %s, not a database kind. Use kubedb_list_resources for non database kinds", info.Kind, info.Group)
		}
		kinds = []k8s.ResourceInfo{info}
	} else {
		kinds, err = c.DatabaseKinds()
		if err != nil {
			return nil, nil, err
		}
	}

	var rows []DatabaseSummary
	var failures []string
	for _, info := range kinds {
		ri := c.Dynamic.Resource(info.GVR()).Namespace(in.Namespace)
		list, err := ri.List(ctx, listOptions(in.LabelSelector))
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", info.Kind, err))
			continue
		}
		for i := range list.Items {
			row := summarize(info.Kind, &list.Items[i])
			if in.Phase != "" && !strings.EqualFold(row.Phase, in.Phase) {
				continue
			}
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Namespace != rows[j].Namespace {
			return rows[i].Namespace < rows[j].Namespace
		}
		if rows[i].Kind != rows[j].Kind {
			return rows[i].Kind < rows[j].Kind
		}
		return rows[i].Name < rows[j].Name
	})
	out := map[string]any{"count": len(rows), "databases": rows}
	if len(failures) > 0 {
		out["errors"] = failures
	}
	res, err := jsonResult(out)
	return res, nil, err
}

// ---- kubedb_list_resources ----

type ListResourcesInput struct {
	Context       string `json:"context,omitempty" jsonschema:"Kubeconfig context to target. Omit to use the current context or in-cluster config."`
	Kind          string `json:"kind" jsonschema:"Any KubeDB family kind, e.g. MongoDBOpsRequest, PostgresVersion, MySQLAutoscaler, ConnectCluster, Connector, Publisher, Subscriber, MongoDBArchiver, MySQLDatabase (schema manager)."`
	Namespace     string `json:"namespace,omitempty" jsonschema:"Namespace to search. Omit for all namespaces. Ignored for cluster scoped kinds."`
	LabelSelector string `json:"labelSelector,omitempty" jsonschema:"Kubernetes label selector to filter results."`
}

func (t *Toolset) ListResources(ctx context.Context, req *mcp.CallToolRequest, in ListResourcesInput) (*mcp.CallToolResult, any, error) {
	c, err := t.clients(in.Context)
	if err != nil {
		return nil, nil, err
	}
	info, err := c.FindKind(in.Kind)
	if err != nil {
		return nil, nil, err
	}
	ri := c.Dynamic.Resource(info.GVR()).Namespace(namespaceFor(info, in.Namespace))
	list, err := ri.List(ctx, listOptions(in.LabelSelector))
	if err != nil {
		return nil, nil, fmt.Errorf("listing %s: %w", info.Kind, err)
	}
	type row struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace,omitempty"`
		Phase     string `json:"phase,omitempty"`
		Age       string `json:"age,omitempty"`
	}
	rows := make([]row, 0, len(list.Items))
	for i := range list.Items {
		u := &list.Items[i]
		rows = append(rows, row{
			Name:      u.GetName(),
			Namespace: u.GetNamespace(),
			Phase:     nestedString(u, "status", "phase"),
			Age:       age(u),
		})
	}
	res, err := jsonResult(map[string]any{
		"kind":      info.Kind,
		"group":     info.Group,
		"count":     len(rows),
		"resources": rows,
	})
	return res, nil, err
}

// ---- kubedb_get_resource ----

type GetResourceInput struct {
	Context   string `json:"context,omitempty" jsonschema:"Kubeconfig context to target. Omit to use the current context or in-cluster config."`
	Kind      string `json:"kind" jsonschema:"Any KubeDB family kind, e.g. MongoDB, PostgresOpsRequest, KafkaVersion."`
	Name      string `json:"name" jsonschema:"Object name."`
	Namespace string `json:"namespace,omitempty" jsonschema:"Object namespace. Required for namespaced kinds, defaults to default. Ignored for cluster scoped kinds."`
	Format    string `json:"format,omitempty" jsonschema:"Output format: json or yaml. Defaults to yaml."`
}

func (t *Toolset) GetResource(ctx context.Context, req *mcp.CallToolRequest, in GetResourceInput) (*mcp.CallToolResult, any, error) {
	c, err := t.clients(in.Context)
	if err != nil {
		return nil, nil, err
	}
	info, err := c.FindKind(in.Kind)
	if err != nil {
		return nil, nil, err
	}
	u, err := c.Dynamic.Resource(info.GVR()).Namespace(namespaceFor(info, in.Namespace)).Get(ctx, in.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, fmt.Errorf("%s %q not found in namespace %q. Use kubedb_list_resources to see what exists", info.Kind, in.Name, namespaceFor(info, in.Namespace))
		}
		return nil, nil, err
	}
	u.SetManagedFields(nil)
	if strings.EqualFold(in.Format, "json") {
		res, err := jsonResult(u.Object)
		return res, nil, err
	}
	y, err := yaml.Marshal(u.Object)
	if err != nil {
		return nil, nil, err
	}
	return textResult(string(y)), nil, nil
}

// namespaceFor picks the effective namespace for a resource.
func namespaceFor(info k8s.ResourceInfo, ns string) string {
	if !info.Namespaced {
		return ""
	}
	if ns == "" {
		return "default"
	}
	return ns
}

// ---- kubedb_database_health ----

type DatabaseHealthInput struct {
	Context   string `json:"context,omitempty" jsonschema:"Kubeconfig context to target. Omit to use the current context or in-cluster config."`
	Kind      string `json:"kind" jsonschema:"Database kind, e.g. MongoDB, Postgres."`
	Name      string `json:"name" jsonschema:"Database object name."`
	Namespace string `json:"namespace,omitempty" jsonschema:"Database namespace. Defaults to default."`
}

func (t *Toolset) DatabaseHealth(ctx context.Context, req *mcp.CallToolRequest, in DatabaseHealthInput) (*mcp.CallToolResult, any, error) {
	c, err := t.clients(in.Context)
	if err != nil {
		return nil, nil, err
	}
	info, err := c.FindKind(in.Kind)
	if err != nil {
		return nil, nil, err
	}
	ns := namespaceFor(info, in.Namespace)
	u, err := c.Dynamic.Resource(info.GVR()).Namespace(ns).Get(ctx, in.Name, metav1.GetOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("getting %s %s/%s: %w", info.Kind, ns, in.Name, err)
	}

	conditions, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")

	type podRow struct {
		Name     string `json:"name"`
		Phase    string `json:"phase"`
		Ready    string `json:"ready"`
		Restarts int32  `json:"restarts"`
		Node     string `json:"node,omitempty"`
	}
	var pods []podRow
	selector := fmt.Sprintf("app.kubernetes.io/instance=%s,app.kubernetes.io/managed-by=%s", in.Name, k8s.GroupKubeDB)
	podList, podErr := c.Typed.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if podErr == nil {
		for _, p := range podList.Items {
			ready := 0
			var restarts int32
			for _, cs := range p.Status.ContainerStatuses {
				if cs.Ready {
					ready++
				}
				restarts += cs.RestartCount
			}
			pods = append(pods, podRow{
				Name:     p.Name,
				Phase:    string(p.Status.Phase),
				Ready:    fmt.Sprintf("%d/%d", ready, len(p.Spec.Containers)),
				Restarts: restarts,
				Node:     p.Spec.NodeName,
			})
		}
	}

	type eventRow struct {
		Reason  string `json:"reason"`
		Message string `json:"message"`
		Count   int32  `json:"count"`
		Last    string `json:"lastSeen,omitempty"`
	}
	var warnings []eventRow
	evtList, evtErr := c.Typed.CoreV1().Events(ns).List(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("involvedObject.name=%s,type=Warning", in.Name),
		Limit:         10,
	})
	if evtErr == nil {
		for _, e := range evtList.Items {
			last := ""
			if !e.LastTimestamp.IsZero() {
				last = e.LastTimestamp.String()
			}
			warnings = append(warnings, eventRow{Reason: e.Reason, Message: e.Message, Count: e.Count, Last: last})
		}
	}

	out := map[string]any{
		"kind":       info.Kind,
		"name":       in.Name,
		"namespace":  ns,
		"phase":      nestedString(u, "status", "phase"),
		"version":    nestedString(u, "spec", "version"),
		"halted":     nestedBool(u, "spec", "halted"),
		"conditions": conditions,
		"pods":       pods,
	}
	if len(warnings) > 0 {
		out["warningEvents"] = warnings
	}
	if podErr != nil {
		out["podListError"] = podErr.Error()
	}
	res, err := jsonResult(out)
	return res, nil, err
}
