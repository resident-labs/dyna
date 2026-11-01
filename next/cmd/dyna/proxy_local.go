package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/rpc"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/resident-labs/dyna/contract"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/spf13/cobra"
)

// defaultProxyPort sits next to the web app's 7790.
const defaultProxyPort = 7791

// addProxyClient adds the owner's commands for pointing local harnesses at the model route.
func addProxyClient(proxy *cobra.Command, get func() *socketClient) {
	var tool string
	var port int
	env := &cobra.Command{Use: "env", Short: "Print the lines that point a harness at Dyna's model proxy", Long: "Print shell lines for Claude Code, for Codex and other OpenAI-style tools, or with no --tool for both. They hold the owner's proxy key, so write them only where the owner keeps secrets. Run dyna proxy serve on the machine that runs the harness.", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if tool != "" && tool != "claude" && tool != "codex" {
			return fmt.Errorf("unknown tool %q: use claude or codex", tool)
		}
		result, err := call[contract.ProxyKeyRequest, contract.ProxyKeyResponse](cmd.Context(), get(), contract.MethodProxyKey, contract.ProxyKeyRequest{})
		if err != nil {
			return err
		}
		_, err = fmt.Fprint(cmd.OutOrStdout(), proxyEnv(tool, port, result.Key))
		return err
	}}
	env.Flags().StringVar(&tool, "tool", "", "claude or codex")
	env.Flags().IntVar(&port, "port", defaultProxyPort, "Port of dyna proxy serve")

	serve := &cobra.Command{Use: "serve", Short: "Forward a loopback port to the model route over the owner socket", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			return err
		}
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		fmt.Fprintf(cmd.OutOrStdout(), "Serving the model proxy on http://%s\n", listener.Addr())
		return serveModelRoute(ctx, listener, get().http.Transport)
	}}
	serve.Flags().IntVar(&port, "port", defaultProxyPort, "Loopback port")

	reset := &cobra.Command{Use: "reset", Short: "Replace the proxy key", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if _, err := call[contract.ProxyKeyRequest, contract.ProxyKeyResponse](cmd.Context(), get(), contract.MethodProxyKey, contract.ProxyKeyRequest{Reset: true}); err != nil {
			return err
		}
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "The old proxy key no longer works. Run dyna proxy env and replace the lines written from it.")
		return err
	}}

	usage := &cobra.Command{Use: "usage", Short: "Show what local harnesses used, by model", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		result, err := call[contract.ProxyUsageRequest, contract.ProxyUsageResponse](cmd.Context(), get(), contract.MethodProxyUsage, contract.ProxyUsageRequest{})
		if err != nil {
			return err
		}
		table := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintf(table, "CLIENT\tMODEL\tREQUESTS\tINPUT\tOUTPUT\tCACHE READ\tCACHE WRITE\tLAST USED\n")
		for _, u := range result.Models {
			fmt.Fprintf(table, "%s\t%s\t%d\t%d\t%d\t%d\t%d\t%s\n", contract.LocalClient, u.Model, u.Requests, u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens, humanize.Time(u.LastAt))
		}
		return table.Flush()
	}}
	proxy.AddCommand(env, serve, reset, usage)
}

// proxyEnv is what a harness needs, safe to source. Codex takes no base URL from its
// environment and its built-in provider opens a WebSocket first, so it gets its own provider.
func proxyEnv(tool string, port int, key string) string {
	base := fmt.Sprintf("http://127.0.0.1:%d/model", port)
	var out strings.Builder
	out.WriteString("# dyna proxy serve must be running on this machine.\n")
	if tool != "codex" {
		fmt.Fprintf(&out, "export ANTHROPIC_BASE_URL=%s\nexport ANTHROPIC_AUTH_TOKEN=%s\n", base, key)
	}
	if tool != "claude" {
		fmt.Fprintf(&out, "export OPENAI_BASE_URL=%s/v1\nexport OPENAI_API_KEY=%s\n", base, key)
		fmt.Fprintf(&out, `codex() { command codex -c 'model_provider="dyna"' -c 'model_providers.dyna.name="Dyna"' -c 'model_providers.dyna.base_url="%s/v1"' -c 'model_providers.dyna.env_key="OPENAI_API_KEY"' -c 'model_providers.dyna.wire_api="responses"' "$@"; }`+"\n", base)
	}
	return out.String()
}

// serveModelRoute forwards the model route and nothing else: the rest of the owner
// socket would give anything that can reach this port full owner power. The engine checks the key.
func serveModelRoute(ctx context.Context, listener net.Listener, transport http.RoundTripper) error {
	forward := &httputil.ReverseProxy{
		Rewrite:       func(r *httputil.ProxyRequest) { r.Out.URL.Scheme, r.Out.URL.Host = "http", "dyna" },
		Transport:     transport,
		FlushInterval: -1,
	}
	mux := http.NewServeMux()
	mux.Handle("/model/", forward)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	stop := context.AfterFunc(ctx, func() { _ = server.Close() })
	defer stop()
	if err := server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// localUsage tells the engine what each request of the local client used.
type localUsage struct{ client *rpc.Client }

// fromLocalClient keeps the local client's finished requests: not a box's, and not an
// attempt that failed over to another account. The proxy names a session by the header
// the route sets, and adds :agent: and an ID for a Claude Code subagent.
func fromLocalClient(record coreusage.Record) bool {
	const session = "claude:" + contract.LocalClient
	return !record.Failed && (record.SessionID == session || strings.HasPrefix(record.SessionID, session+":"))
}

func (u localUsage) HandleUsage(_ context.Context, record coreusage.Record) {
	if !fromLocalClient(record) {
		return
	}
	_ = u.client.Call("Auth.LocalUsage", contract.ProxyUsage{
		Model: record.Model, Requests: 1, LastAt: time.Now(),
		InputTokens: record.Detail.InputTokens, OutputTokens: record.Detail.OutputTokens,
		CacheReadTokens: record.Detail.CacheReadTokens, CacheWriteTokens: record.Detail.CacheCreationTokens,
	}, &struct{}{})
}
