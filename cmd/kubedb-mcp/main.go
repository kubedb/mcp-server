// Command kubedb-mcp is an MCP server for managing KubeDB databases in
// Kubernetes and OpenShift clusters.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"kubedb.dev/mcp-server/internal/k8s"
	"kubedb.dev/mcp-server/internal/server"
	"kubedb.dev/mcp-server/internal/tools"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	var (
		transport          = flag.String("transport", envStr("KUBEDB_MCP_TRANSPORT", "stdio"), "Transport to serve on: stdio or http")
		listen             = flag.String("listen", envStr("KUBEDB_MCP_LISTEN", ":8080"), "Listen address for the http transport")
		kubeconfig         = flag.String("kubeconfig", os.Getenv("KUBEDB_MCP_KUBECONFIG"), "Path to a kubeconfig file. Defaults to KUBECONFIG, ~/.kube/config, then in-cluster config")
		readOnly           = flag.Bool("read-only", envBool("KUBEDB_MCP_READ_ONLY", false), "Expose only read tools")
		disableDestructive = flag.Bool("disable-destructive", envBool("KUBEDB_MCP_DISABLE_DESTRUCTIVE", false), "Disable delete and other destructive operations")
		allowCredentials   = flag.Bool("allow-credentials", envBool("KUBEDB_MCP_ALLOW_CREDENTIALS", false), "Allow kubedb_get_connection_info to return decoded database credentials")
		showVersion        = flag.Bool("version", false, "Print version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("kubedb-mcp", version)
		return
	}

	ts := &tools.Toolset{
		Factory: k8s.NewFactory(*kubeconfig, "kubedb-mcp/"+version),
		Cfg: tools.Config{
			ReadOnly:           *readOnly,
			DisableDestructive: *disableDestructive,
			AllowCredentials:   *allowCredentials,
		},
	}
	srv := server.New(ts, version)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch *transport {
	case "stdio":
		if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil {
			log.Fatalf("server error: %v", err)
		}
	case "http":
		handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{
			Stateless: true,
		})
		mux := http.NewServeMux()
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		})
		mux.Handle("/mcp", handler)
		mux.Handle("/", handler)
		hs := &http.Server{
			Addr:              *listen,
			Handler:           mux,
			ReadHeaderTimeout: 10 * time.Second,
		}
		go func() {
			<-ctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = hs.Shutdown(shutdownCtx)
		}()
		log.Printf("kubedb-mcp %s listening on %s (streamable HTTP)", version, *listen)
		if err := hs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	default:
		log.Fatalf("unknown transport %q: use stdio or http", *transport)
	}
}
