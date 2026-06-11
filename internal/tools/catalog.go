package tools

import (
	"context"
	"fmt"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"kubedb.dev/mcp-server/internal/k8s"
)

// ---- kubedb_list_versions ----

type ListVersionsInput struct {
	Context string `json:"context,omitempty" jsonschema:"Kubeconfig context to target. Omit to use the current context or in-cluster config."`
	Kind    string `json:"kind" jsonschema:"Database kind whose catalog versions to list, e.g. Postgres, MongoDB, MySQL, Kafka."`
}

type VersionRow struct {
	Name              string `json:"name"`
	Version           string `json:"version"`
	Distribution      string `json:"distribution,omitempty"`
	Deprecated        bool   `json:"deprecated,omitempty"`
	UpdateConstraints any    `json:"updateConstraints,omitempty"`
}

func (t *Toolset) ListVersions(ctx context.Context, req *mcp.CallToolRequest, in ListVersionsInput) (*mcp.CallToolResult, any, error) {
	c, err := t.clients(in.Context)
	if err != nil {
		return nil, nil, err
	}
	rows, err := t.versionRows(ctx, c, in.Kind)
	if err != nil {
		return nil, nil, err
	}
	res, err := jsonResult(map[string]any{
		"kind":     k8s.VersionKindFor(in.Kind),
		"count":    len(rows),
		"versions": rows,
		"hint":     "Use a version name (not the raw version number) in spec.version and in UpdateVersion ops requests. Respect updateConstraints when planning upgrades.",
	})
	return res, nil, err
}

func (t *Toolset) versionRows(ctx context.Context, c *k8s.Clients, dbKind string) ([]VersionRow, error) {
	info, err := c.FindKind(k8s.VersionKindFor(dbKind))
	if err != nil {
		return nil, fmt.Errorf("no version catalog found for kind %q: %w", dbKind, err)
	}
	list, err := c.Dynamic.Resource(info.GVR()).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing %s: %w", info.Kind, err)
	}
	rows := make([]VersionRow, 0, len(list.Items))
	for i := range list.Items {
		u := &list.Items[i]
		constraints, _, _ := unstructured.NestedMap(u.Object, "spec", "updateConstraints")
		rows = append(rows, VersionRow{
			Name:              u.GetName(),
			Version:           nestedString(u, "spec", "version"),
			Distribution:      nestedString(u, "spec", "distribution"),
			Deprecated:        nestedBool(u, "spec", "deprecated"),
			UpdateConstraints: constraints,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, nil
}
