// Package githubmcp serves GitHub's own MCP server, github-mcp-server, to boxes: every read-only tool it has, with the engine's
// token, which never leaves this process.
package githubmcp

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"runtime/debug"
	"slices"
	"strings"
	"sync"

	"github.com/github/github-mcp-server/v2/pkg/github"
	"github.com/github/github-mcp-server/v2/pkg/http/transport"
	"github.com/github/github-mcp-server/v2/pkg/inventory"
	"github.com/github/github-mcp-server/v2/pkg/observability"
	"github.com/github/github-mcp-server/v2/pkg/observability/metrics"
	"github.com/github/github-mcp-server/v2/pkg/raw"
	"github.com/github/github-mcp-server/v2/pkg/translations"
	"github.com/github/github-mcp-server/v2/pkg/utils"
	gogithub "github.com/google/go-github/v92/github"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/githubwrites"
	"github.com/shurcooL/githubv4"
)

const module = "github.com/github/github-mcp-server/v2"

// features turns on upstream's read tools that wait behind a flag: file blame, issue dependencies and duplicate issues.
var features = []string{github.FeatureFlagFileBlame, github.FeatureFlagIssueDependencies, github.FeatureFlagDuplicateDetection}

// excluded is upstream's helper for MCP Apps forms. Pi shows no such forms, and the other tools read the same data.
var excluded = []string{"ui_get"}

// contentWindow is how many lines of a job log one read keeps, upstream's default.
const contentWindow = 5000

// Handler serves github-mcp-server over streamable HTTP, built as upstream's own stdio server builds it with every toolset. A
// room's boxes see every read tool and, besides, the write tools of the groups its githubWrites setting turns on, which the
// request's context carries (githubwrites.WithAllowed); with none on, the server is upstream's --read-only one. There is one
// server for each set of groups, made when a room first needs it, and each is stateless, so it answers every box of any room with
// that set. Each request to GitHub takes the token here and goes through the githubwrites guard, which lets a read through to reads
// (it spends the engine's GitHub quota) and a write only if the room's setting allows exactly that write.
func Handler(ctx context.Context, token string, reads http.RoundTripper, owns func(context.Context, contract.RepoName, int) (bool, error), canPush func(context.Context, contract.RepoName, string) (bool, error)) (http.Handler, error) {
	host, err := utils.NewAPIHost("")
	if err != nil {
		return nil, err
	}
	restURL, _ := host.BaseRESTURL(ctx)
	uploadURL, _ := host.UploadURL(ctx)
	graphQLURL, _ := host.GraphqlURL(ctx)
	rawURL, _ := host.RawURL(ctx)
	version := upstreamVersion()
	guard := &githubwrites.Guard{Base: reads, API: restURL, Owns: owns, CanPush: canPush}
	// The token goes only to GitHub's own hosts, so a redirect elsewhere does not carry it.
	allowed := []string{restURL.Host, uploadURL.Host, graphQLURL.Host, rawURL.Host}
	signed := func(next http.RoundTripper) *http.Client {
		agent := &transport.UserAgentTransport{Transport: next, Agent: "dyna github-mcp-server/" + version}
		return &http.Client{Transport: &transport.BearerAuthTransport{Transport: agent, Token: token, AllowedHosts: allowed}}
	}
	rest, err := gogithub.NewClient(gogithub.WithHTTPClient(signed(&transport.ETagTransport{Transport: guard})), gogithub.WithEnterpriseURLs(restURL.String(), uploadURL.String()))
	if err != nil {
		return nil, err
	}
	// Raw file bodies skip the ETag cache, as upstream's do, so large files are not kept in memory.
	rawREST, err := gogithub.NewClient(gogithub.WithHTTPClient(signed(reads)), gogithub.WithEnterpriseURLs(restURL.String(), uploadURL.String()))
	if err != nil {
		return nil, err
	}
	rawClient, err := raw.NewClient(rawREST, rawURL)
	if err != nil {
		return nil, err
	}
	graphQL := githubv4.NewEnterpriseClient(graphQLURL.String(), signed(&transport.GraphQLFeaturesTransport{Transport: guard}))

	logger := slog.New(slog.NewTextHandler(log.Writer(), &slog.HandlerOptions{Level: slog.LevelWarn}))
	exporters, err := observability.NewExporters(logger, metrics.NewNoopMetrics())
	if err != nil {
		return nil, err
	}
	enabled := func(_ context.Context, flag string) (bool, error) { return slices.Contains(features, flag), nil }
	t := translations.NullTranslationHelper
	deps := github.NewBaseDeps(rest, graphQL, rawClient, nil, t, github.FeatureFlags{}, contentWindow, enabled, exporters)

	var mu sync.Mutex
	servers := map[string]*mcp.Server{}
	serverFor := func(grant contract.GitHubWrites) (*mcp.Server, error) {
		mu.Lock()
		defer mu.Unlock()
		names := githubwrites.ToolNames(grant)
		key := strings.Join(names, " ")
		if servers[key] != nil {
			return servers[key], nil
		}
		tools := github.AllTools(t, github.WithHost(utils.HostTypeDotcom))
		for i := range tools {
			if tool := &tools[i].Tool; slices.Contains(names, tool.Name) {
				githubwrites.Describe(grant, tool)
			}
		}
		inventory, err := inventory.NewBuilder().SetTools(tools).SetResources(github.AllResources(t)).SetPrompts(github.AllPrompts(t)).
			WithDeprecatedAliases(github.DeprecatedToolAliases).
			WithReadOnly(len(names) == 0).
			WithToolsets([]string{"all"}).
			WithExcludeTools(excluded).
			WithFilter(func(_ context.Context, tool *inventory.ServerTool) (bool, error) {
				return tool.IsReadOnly() || slices.Contains(names, tool.Tool.Name), nil
			}).
			WithServerInstructions().
			WithFeatureChecker(enabled).
			Build()
		if err != nil {
			return nil, fmt.Errorf("github-mcp-server tools: %w", err)
		}
		server, err := github.NewMCPServer(ctx, &github.MCPServerConfig{Version: version, Translator: t, ReadOnly: len(names) == 0, ContentWindowSize: contentWindow, Logger: logger}, deps, inventory)
		if err != nil {
			return nil, fmt.Errorf("github-mcp-server: %w", err)
		}
		servers[key] = server
		return server, nil
	}
	// Building the read-only server now reports a broken build at start-up, not at a box's first call.
	if _, err := serverFor(contract.GitHubWrites{}); err != nil {
		return nil, err
	}
	return mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		server, err := serverFor(githubwrites.AllowedFrom(r.Context()))
		if err != nil {
			log.Printf("GitHub tools for a room: %v", err)
			return nil
		}
		return server
	}, &mcp.StreamableHTTPOptions{Stateless: true}), nil
}

// upstreamVersion is the github-mcp-server version this binary was built with.
func upstreamVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range info.Deps {
			if dep.Path == module {
				return dep.Version
			}
		}
	}
	return "unknown"
}
