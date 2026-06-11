// Package tools implements the MCP tools exposed by the KubeDB MCP server.
package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/duration"

	"kubedb.dev/mcp-server/internal/k8s"
)

// Config carries the safety switches for the toolset.
type Config struct {
	// ReadOnly disables every tool that mutates cluster state.
	ReadOnly bool
	// DisableDestructive disables delete and disruptive operations while
	// still allowing additive writes.
	DisableDestructive bool
	// AllowCredentials permits kubedb_get_connection_info to decode and
	// return database credentials from Kubernetes secrets.
	AllowCredentials bool
}

// Toolset holds shared dependencies for all tool handlers.
type Toolset struct {
	Factory *k8s.Factory
	Cfg     Config
}

func (t *Toolset) clients(contextName string) (*k8s.Clients, error) {
	return t.Factory.Clients(contextName)
}

func (t *Toolset) guardWrite() error {
	if t.Cfg.ReadOnly {
		return fmt.Errorf("this server is running in read-only mode; write operations are disabled")
	}
	return nil
}

func (t *Toolset) guardDestructive() error {
	if err := t.guardWrite(); err != nil {
		return err
	}
	if t.Cfg.DisableDestructive {
		return fmt.Errorf("destructive operations are disabled on this server (--disable-destructive)")
	}
	return nil
}

func textResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func jsonResult(v any) (*mcp.CallToolResult, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding result: %w", err)
	}
	return textResult(string(b)), nil
}

// parseJSONObject parses a JSON object provided as a tool argument string.
func parseJSONObject(field, raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, fmt.Errorf("%s must be a JSON object: %w", field, err)
	}
	return m, nil
}

// deepMerge merges src into dst, recursing into nested maps.
func deepMerge(dst, src map[string]any) map[string]any {
	if dst == nil {
		return src
	}
	for k, v := range src {
		if sv, ok := v.(map[string]any); ok {
			if dv, ok := dst[k].(map[string]any); ok {
				dst[k] = deepMerge(dv, sv)
				continue
			}
		}
		dst[k] = v
	}
	return dst
}

func age(u *unstructured.Unstructured) string {
	ts := u.GetCreationTimestamp()
	if ts.IsZero() {
		return ""
	}
	return duration.HumanDuration(time.Since(ts.Time))
}

func nestedString(u *unstructured.Unstructured, fields ...string) string {
	s, _, _ := unstructured.NestedString(u.Object, fields...)
	return s
}

func nestedInt(u *unstructured.Unstructured, fields ...string) *int64 {
	v, found, err := unstructured.NestedInt64(u.Object, fields...)
	if !found || err != nil {
		return nil
	}
	return &v
}

func nestedBool(u *unstructured.Unstructured, fields ...string) bool {
	v, _, _ := unstructured.NestedBool(u.Object, fields...)
	return v
}

func listOptions(labelSelector string) metav1.ListOptions {
	return metav1.ListOptions{LabelSelector: labelSelector}
}

func dryRunOpts(dryRun bool) []string {
	if dryRun {
		return []string{metav1.DryRunAll}
	}
	return nil
}
