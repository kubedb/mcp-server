// Package k8s provides Kubernetes client plumbing for the KubeDB MCP server.
package k8s

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// ResourceInfo describes one discovered KubeDB API resource.
type ResourceInfo struct {
	Group      string `json:"group"`
	Version    string `json:"version"`
	Kind       string `json:"kind"`
	Resource   string `json:"resource"`
	Namespaced bool   `json:"namespaced"`
}

// GVR returns the GroupVersionResource for this resource.
func (r ResourceInfo) GVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: r.Group, Version: r.Version, Resource: r.Resource}
}

// APIVersion returns the group/version string for manifests.
func (r ResourceInfo) APIVersion() string {
	return schema.GroupVersion{Group: r.Group, Version: r.Version}.String()
}

// Clients bundles the client set for one cluster context.
type Clients struct {
	Dynamic   dynamic.Interface
	Typed     kubernetes.Interface
	Discovery discovery.CachedDiscoveryInterface

	mu    sync.Mutex
	index []ResourceInfo
}

// IsKubeDBGroup reports whether an API group belongs to the KubeDB family.
func IsKubeDBGroup(group string) bool {
	return group == GroupKubeDB || strings.HasSuffix(group, "."+GroupKubeDB)
}

// KubeDBResources returns all discovered API resources in the KubeDB family,
// using the preferred version of each group.
func (c *Clients) KubeDBResources(refresh bool) ([]ResourceInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if refresh {
		c.Discovery.Invalidate()
		c.index = nil
	}
	if c.index != nil {
		return c.index, nil
	}
	lists, err := c.Discovery.ServerPreferredResources()
	if err != nil && len(lists) == 0 {
		return nil, fmt.Errorf("API discovery failed: %w. Verify cluster connectivity and credentials", err)
	}
	var out []ResourceInfo
	for _, l := range lists {
		gv, gvErr := schema.ParseGroupVersion(l.GroupVersion)
		if gvErr != nil || !IsKubeDBGroup(gv.Group) {
			continue
		}
		for _, r := range l.APIResources {
			if strings.Contains(r.Name, "/") {
				continue // skip subresources
			}
			out = append(out, ResourceInfo{
				Group:      gv.Group,
				Version:    gv.Version,
				Kind:       r.Kind,
				Resource:   r.Name,
				Namespaced: r.Namespaced,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].Kind < out[j].Kind
	})
	c.index = out
	return out, nil
}

// FindKind resolves a kind name (case-insensitive) to a KubeDB API resource.
// It retries once with a discovery cache refresh before giving up.
func (c *Clients) FindKind(kind string) (ResourceInfo, error) {
	for _, refresh := range []bool{false, true} {
		resources, err := c.KubeDBResources(refresh)
		if err != nil {
			return ResourceInfo{}, err
		}
		for _, r := range resources {
			if strings.EqualFold(r.Kind, kind) {
				return r, nil
			}
		}
	}
	resources, _ := c.KubeDBResources(false)
	known := make([]string, 0, len(resources))
	for _, r := range resources {
		if r.Group == GroupKubeDB {
			known = append(known, r.Kind)
		}
	}
	if len(known) == 0 {
		return ResourceInfo{}, fmt.Errorf("no KubeDB APIs found in this cluster. Install KubeDB first (https://kubedb.com/docs/latest/setup/) or check that you are connected to the right cluster")
	}
	return ResourceInfo{}, fmt.Errorf("kind %q not found in any KubeDB API group. Installed database kinds: %s. Use kubedb_list_kinds to see every KubeDB resource", kind, strings.Join(known, ", "))
}

// FindResourceInGroup resolves a kind (case-insensitive) within a specific API
// group to its preferred-version resource. It is used for non-KubeDB groups
// such as catalog.appscode.com and gateway.networking.k8s.io. Returns ok=false
// when the group or kind is not served by the cluster.
func (c *Clients) FindResourceInGroup(group, kind string) (ResourceInfo, bool, error) {
	lists, err := c.Discovery.ServerPreferredResources()
	if err != nil && len(lists) == 0 {
		return ResourceInfo{}, false, fmt.Errorf("API discovery failed: %w", err)
	}
	for _, l := range lists {
		gv, gvErr := schema.ParseGroupVersion(l.GroupVersion)
		if gvErr != nil || gv.Group != group {
			continue
		}
		for _, r := range l.APIResources {
			if strings.Contains(r.Name, "/") {
				continue // skip subresources
			}
			if strings.EqualFold(r.Kind, kind) {
				return ResourceInfo{
					Group:      gv.Group,
					Version:    gv.Version,
					Kind:       r.Kind,
					Resource:   r.Name,
					Namespaced: r.Namespaced,
				}, true, nil
			}
		}
	}
	return ResourceInfo{}, false, nil
}

// DatabaseKinds returns the kinds in the core kubedb.com group.
func (c *Clients) DatabaseKinds() ([]ResourceInfo, error) {
	resources, err := c.KubeDBResources(false)
	if err != nil {
		return nil, err
	}
	var out []ResourceInfo
	for _, r := range resources {
		if r.Group == GroupKubeDB && !strings.EqualFold(r.Kind, "DoubleOptIn") {
			out = append(out, r)
		}
	}
	return out, nil
}

// GroupKinds returns the kinds in the given KubeDB API group.
func (c *Clients) GroupKinds(group string) ([]ResourceInfo, error) {
	resources, err := c.KubeDBResources(false)
	if err != nil {
		return nil, err
	}
	var out []ResourceInfo
	for _, r := range resources {
		if r.Group == group {
			out = append(out, r)
		}
	}
	return out, nil
}

// ContextInfo describes one kubeconfig context.
type ContextInfo struct {
	Name      string `json:"name"`
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace,omitempty"`
	Current   bool   `json:"current"`
}

// Factory creates and caches per-context client sets.
type Factory struct {
	kubeconfig string
	userAgent  string

	mu    sync.Mutex
	cache map[string]*Clients
}

// NewFactory returns a Factory. kubeconfigPath may be empty, in which case
// the standard loading rules (KUBECONFIG env var, ~/.kube/config) apply,
// with in-cluster config as the fallback when running inside a pod.
func NewFactory(kubeconfigPath, userAgent string) *Factory {
	return &Factory{
		kubeconfig: kubeconfigPath,
		userAgent:  userAgent,
		cache:      map[string]*Clients{},
	}
}

func (f *Factory) restConfig(contextName string) (*rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if f.kubeconfig != "" {
		rules.ExplicitPath = f.kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: contextName}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	if err == nil {
		return cfg, nil
	}
	// Fall back to in-cluster config when no kubeconfig is available.
	if contextName == "" {
		if icc, iccErr := rest.InClusterConfig(); iccErr == nil {
			return icc, nil
		}
	}
	return nil, fmt.Errorf("could not load cluster credentials: %w. Provide --kubeconfig, set KUBECONFIG, or run in a pod with a service account", err)
}

// Clients returns the cached client set for the named kubeconfig context.
// An empty name selects the current context or the in-cluster config.
func (f *Factory) Clients(contextName string) (*Clients, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.cache[contextName]; ok {
		return c, nil
	}
	cfg, err := f.restConfig(contextName)
	if err != nil {
		return nil, err
	}
	cfg.QPS = 50
	cfg.Burst = 100
	cfg.UserAgent = f.userAgent

	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("building dynamic client: %w", err)
	}
	typed, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("building typed client: %w", err)
	}
	disco, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("building discovery client: %w", err)
	}
	c := &Clients{
		Dynamic:   dyn,
		Typed:     typed,
		Discovery: memory.NewMemCacheClient(disco),
	}
	f.cache[contextName] = c
	return c, nil
}

// ListContexts lists the contexts available in the kubeconfig.
func (f *Factory) ListContexts() ([]ContextInfo, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if f.kubeconfig != "" {
		rules.ExplicitPath = f.kubeconfig
	}
	raw, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).RawConfig()
	if err != nil {
		return nil, fmt.Errorf("could not read kubeconfig: %w. When running in-cluster there is a single implicit context", err)
	}
	out := make([]ContextInfo, 0, len(raw.Contexts))
	for name, ctx := range raw.Contexts {
		out = append(out, ContextInfo{
			Name:      name,
			Cluster:   ctx.Cluster,
			Namespace: ctx.Namespace,
			Current:   name == raw.CurrentContext,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
