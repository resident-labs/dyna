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

	"github.com/github/github-mcp-server/v2/pkg/github"
	"github.com/github/github-mcp-server/v2/pkg/http/transport"
	"github.com/github/github-mcp-server/v2/pkg/observability"
	"github.com/github/github-mcp-server/v2/pkg/observability/metrics"
	"github.com/github/github-mcp-server/v2/pkg/raw"
	"github.com/github/github-mcp-server/v2/pkg/translations"
	"github.com/github/github-mcp-server/v2/pkg/utils"
	gogithub "github.com/google/go-github/v92/github"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/shurcooL/githubv4"
)

const module = "github.com/github/github-mcp-server/v2"

// features turns on upstream's read tools that wait behind a flag: file blame, issue dependencies and duplicate issues.
var features = []string{github.FeatureFlagFileBlame, github.FeatureFlagIssueDependencies, github.FeatureFlagDuplicateDetection}

// excluded is upstream's helper for MCP Apps forms. Pi shows no such forms, and the other tools read the same data.
var excluded = []string{"ui_get"}

// contentWindow is how many lines of a job log one read keeps, upstream's default.
const contentWindow = 5000

// Handler serves every read-only tool of github-mcp-server over streamable HTTP, built as upstream's own stdio server builds
// it but with --read-only and every toolset. It is stateless, so one server answers every box. Each request to GitHub takes
// the token here and goes out through reads, which refuses anything but a read and spends the engine's GitHub quota.
func Handler(ctx context.Context, token string, reads http.RoundTripper) (http.Handler, error) {
	host, err := utils.NewAPIHost("")
	if err != nil {
		return nil, err
	}
	restURL, _ := host.BaseRESTURL(ctx)
	uploadURL, _ := host.UploadURL(ctx)
	graphQLURL, _ := host.GraphqlURL(ctx)
	rawURL, _ := host.RawURL(ctx)
	version := upstreamVersion()
	// The token goes only to GitHub's own hosts, so a redirect elsewhere does not carry it.
	allowed := []string{restURL.Host, uploadURL.Host, graphQLURL.Host, rawURL.Host}
	signed := func(next http.RoundTripper) *http.Client {
		agent := &transport.UserAgentTransport{Transport: next, Agent: "dyna github-mcp-server/" + version}
		return &http.Client{Transport: &transport.BearerAuthTransport{Transport: agent, Token: token, AllowedHosts: allowed}}
	}
	rest, err := gogithub.NewClient(gogithub.WithHTTPClient(signed(&transport.ETagTransport{Transport: reads})), gogithub.WithEnterpriseURLs(restURL.String(), uploadURL.String()))
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
	graphQL := githubv4.NewEnterpriseClient(graphQLURL.String(), signed(&transport.GraphQLFeaturesTransport{Transport: reads}))

	logger := slog.New(slog.NewTextHandler(log.Writer(), &slog.HandlerOptions{Level: slog.LevelWarn}))
	exporters, err := observability.NewExporters(logger, metrics.NewNoopMetrics())
	if err != nil {
		return nil, err
	}
	enabled := func(_ context.Context, flag string) (bool, error) { return slices.Contains(features, flag), nil }
	t := translations.NullTranslationHelper
	deps := github.NewBaseDeps(rest, graphQL, rawClient, nil, t, github.FeatureFlags{}, contentWindow, enabled, exporters)
	inventory, err := github.NewInventory(t, github.WithHost(utils.HostTypeDotcom)).
		WithDeprecatedAliases(github.DeprecatedToolAliases).
		WithReadOnly(true).
		WithToolsets([]string{"all"}).
		WithExcludeTools(excluded).
		WithServerInstructions().
		WithFeatureChecker(enabled).
		Build()
	if err != nil {
		return nil, fmt.Errorf("github-mcp-server tools: %w", err)
	}
	server, err := github.NewMCPServer(ctx, &github.MCPServerConfig{Version: version, Translator: t, ReadOnly: true, ContentWindowSize: contentWindow, Logger: logger}, deps, inventory)
	if err != nil {
		return nil, fmt.Errorf("github-mcp-server: %w", err)
	}
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true}), nil
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
