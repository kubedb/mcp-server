package tools

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"kubedb.dev/mcp-server/internal/k8s"
)

// ---- kubedb_get_connection_info ----

type ConnectionInfoInput struct {
	Context            string `json:"context,omitempty" jsonschema:"Kubeconfig context to target. Omit to use the current context or in-cluster config."`
	Kind               string `json:"kind" jsonschema:"Database kind, e.g. MongoDB, Postgres, MySQL, Redis."`
	Name               string `json:"name" jsonschema:"Database object name."`
	Namespace          string `json:"namespace,omitempty" jsonschema:"Database namespace. Defaults to default."`
	IncludeCredentials bool   `json:"includeCredentials,omitempty" jsonschema:"Decode and include the username and password from the auth secret. Requires the server to run with --allow-credentials."`
}

type ServiceEndpoint struct {
	Service string  `json:"service"`
	DNS     string  `json:"dns"`
	Ports   []int32 `json:"ports"`
	Alias   string  `json:"alias,omitempty"`
}

func (t *Toolset) ConnectionInfo(ctx context.Context, req *mcp.CallToolRequest, in ConnectionInfoInput) (*mcp.CallToolResult, any, error) {
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
	u, err := c.Dynamic.Resource(info.GVR()).Namespace(ns).Get(ctx, in.Name, metav1.GetOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("getting %s %s/%s: %w", info.Kind, ns, in.Name, err)
	}

	authSecret := nestedString(u, "spec", "authSecret", "name")
	if authSecret == "" {
		authSecret = in.Name + "-auth"
	}

	selector := fmt.Sprintf("app.kubernetes.io/instance=%s,app.kubernetes.io/managed-by=%s", in.Name, k8s.GroupKubeDB)
	svcList, err := c.Typed.CoreV1().Services(ns).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, nil, fmt.Errorf("listing services: %w", err)
	}
	endpoints := make([]ServiceEndpoint, 0, len(svcList.Items))
	for _, svc := range svcList.Items {
		ep := ServiceEndpoint{
			Service: svc.Name,
			DNS:     fmt.Sprintf("%s.%s.svc", svc.Name, ns),
			Alias:   svc.Labels["kubedb.com/service-alias"],
		}
		for _, p := range svc.Spec.Ports {
			ep.Ports = append(ep.Ports, p.Port)
		}
		endpoints = append(endpoints, ep)
	}

	out := map[string]any{
		"kind":           info.Kind,
		"name":           in.Name,
		"namespace":      ns,
		"phase":          nestedString(u, "status", "phase"),
		"authSecretName": authSecret,
		"tlsEnabled":     hasTLS(u),
		"endpoints":      endpoints,
	}

	if in.IncludeCredentials {
		if !t.Cfg.AllowCredentials {
			out["credentials"] = "withheld: this server was started without --allow-credentials. Read the auth secret directly with kubectl if permitted: kubectl get secret " + authSecret + " -n " + ns + " -o yaml"
		} else {
			secret, err := c.Typed.CoreV1().Secrets(ns).Get(ctx, authSecret, metav1.GetOptions{})
			if err != nil {
				out["credentialsError"] = err.Error()
			} else {
				creds := map[string]string{}
				for _, key := range []string{"username", "password", "authSSL", "uri"} {
					if v, ok := secret.Data[key]; ok {
						creds[key] = string(v)
					}
				}
				out["credentials"] = creds
				out["credentialsWarning"] = "Handle these credentials carefully. Do not write them to files, logs, or version control."
			}
		}
	}
	res, err := jsonResult(out)
	return res, nil, err
}

func hasTLS(u *unstructured.Unstructured) bool {
	tls, found, _ := unstructured.NestedMap(u.Object, "spec", "tls")
	return found && len(tls) > 0
}
