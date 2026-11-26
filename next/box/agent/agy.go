package agent

import (
	"context"
	"errors"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/agy"
	"github.com/resident-labs/dyna/harness/pig"
)

// openAgy opens the session's agy harness in place of PiG: the dyna tools, and GitHub's when the engine serves them, are its MCP servers.
// The profile directory is the host's own, mounted read-write: the box never reads or copies a file in it.
func openAgy(ctx context.Context, hello contract.BoxHelloResponse, workdir, sessions string, notice func(string)) (*agy.Harness, error) {
	if hello.Agy == nil {
		return nil, errors.New("the hello names the agy harness without its files")
	}
	servers := []agy.MCPServer{{Name: "dyna", URL: pig.DynaMCPURL}}
	if hello.GitHub {
		servers = append(servers, agy.MCPServer{Name: "github", URL: pig.GitHubMCPURL})
	}
	return agy.Open(ctx, agy.Config{
		Binary:            hello.Agy.Binary,
		GeminiDir:         hello.Agy.GeminiDir,
		Dir:               workdir,
		SessionDir:        sessions,
		Model:             hello.Model,
		Instructions:      hello.Instructions,
		ModelInstructions: hello.ModelInstructions,
		MCPServers:        servers,
		New:               hello.Fresh,
		Notice:            notice,
	})
}
