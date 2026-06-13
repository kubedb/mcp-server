package tools

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

	gateways, err := gatewayConnections(ctx, c, info.Kind, in.Name, ns, authSecret)
	if err != nil {
		out["gatewayError"] = err.Error()
	} else if len(gateways) > 0 {
		out["publicEndpoints"] = gateways
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

// GatewayEndpoint is one public connection route for a database, sourced from a
// catalog.appscode.com binding's status.
type GatewayEndpoint struct {
	// Gateway mirrors the binding's status.gateway (ofst.Gateway): host/ip,
	// per-alias service ports, and database UI URLs.
	Gateway   map[string]any `json:"gateway"`
	SecretRef *SecretRef     `json:"secretRef,omitempty"`
	// CACert is the PEM-encoded CA bundle for the gateway's TLS listener.
	CACert string `json:"caCert,omitempty"`
}

type SecretRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// gatewayConnections discovers the catalog binding for a database kind, finds
// the binding whose spec.sourceRef points at this database, and returns the
// public gateway connections from its status. Returns nil (no error) when the
// catalog/gateway stack is not installed or no matching binding exists.
func gatewayConnections(ctx context.Context, c *k8s.Clients, kind, name, ns, authSecret string) ([]GatewayEndpoint, error) {
	bindingRes, ok, err := c.FindResourceInGroup(k8s.GroupAppCatalog, kind+"Binding")
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil // catalog bindings not installed
	}

	list, err := c.Dynamic.Resource(bindingRes.GVR()).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing %s: %w", bindingRes.Resource, err)
	}

	var out []GatewayEndpoint
	for i := range list.Items {
		b := &list.Items[i]
		srcName := nestedString(b, "spec", "sourceRef", "name")
		srcNS := nestedString(b, "spec", "sourceRef", "namespace")
		if srcName != name || srcNS != ns {
			continue
		}
		gw, found, _ := unstructured.NestedMap(b.Object, "status", "gateway")
		if !found || len(gw) == 0 {
			continue
		}
		// Front-end uses the IP as the hostname when hostname is absent.
		if h, _ := gw["hostname"].(string); h == "" {
			if ip, _ := gw["ip"].(string); ip != "" {
				gw["hostname"] = ip
			}
		}

		ep := GatewayEndpoint{Gateway: gw}
		if secName := nestedString(b, "status", "secretRef", "name"); secName != "" {
			ep.SecretRef = &SecretRef{Namespace: b.GetNamespace(), Name: secName}
		} else {
			ep.SecretRef = &SecretRef{Namespace: ns, Name: authSecret}
		}

		gwName, _ := gw["name"].(string)
		gwNS, _ := gw["namespace"].(string)
		if cert, err := gatewayCACert(ctx, c, gwName, gwNS); err != nil {
			return nil, err
		} else if len(cert) > 0 {
			ep.CACert = string(cert)
		}
		out = append(out, ep)
	}
	return out, nil
}

// gatewayCACert returns the CA bundle for a gateway-api Gateway's first TLS
// listener, falling back to the ACME root when the referenced secret has no
// ca.crt key. Returns nil when the Gateway, its TLS config, or the API itself
// is absent.
func gatewayCACert(ctx context.Context, c *k8s.Clients, name, namespace string) ([]byte, error) {
	if name == "" {
		return nil, nil
	}
	gwRes, ok, err := c.FindResourceInGroup(k8s.GroupGatewayAPI, "Gateway")
	if err != nil || !ok {
		return nil, err
	}
	gw, err := c.Dynamic.Resource(gwRes.GVR()).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("getting gateway %s/%s: %w", namespace, name, err)
	}

	listeners, found, _ := unstructured.NestedSlice(gw.Object, "spec", "listeners")
	if !found || len(listeners) == 0 {
		return nil, nil
	}
	listener, _ := listeners[0].(map[string]any)
	tls, _ := listener["tls"].(map[string]any)
	if tls == nil {
		return nil, nil
	}
	refs, _ := tls["certificateRefs"].([]any)
	if len(refs) == 0 {
		return nil, nil
	}
	ref, _ := refs[0].(map[string]any)
	secName, _ := ref["name"].(string)
	if secName == "" {
		return nil, nil
	}
	secNS := namespace
	if v, _ := ref["namespace"].(string); v != "" {
		secNS = v
	}

	sec, err := c.Typed.CoreV1().Secrets(secNS).Get(ctx, secName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("getting gateway tls secret %s/%s: %w", secNS, secName, err)
	}
	if ca, ok := sec.Data["ca.crt"]; ok {
		return ca, nil
	}
	return []byte(acmeCaCrt), nil
}
