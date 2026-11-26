package agent

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/agy"
	"github.com/resident-labs/dyna/harness/pig"
)

// agyContextFiles are the two instruction files PiG loads from its agent folder and from the parent of the work folder. agy reads
// only AGENTS.md in the work folder, which is the repository's own and stays agy's to read. The profile folder is a shared mount,
// so nothing is linked into it.
var agyContextFiles = []string{"/box/pi/AGENTS.md", "/box/work/AGENTS.md"}

// agyProjectInstructions reads each readable, non-empty file once, labelled as PiG labels a project context file in its prompt. The
// harness sends them with the session instructions on the first prompt. A file edited later applies at the next open of a session.
func agyProjectInstructions(paths []string) []string {
	var out []string
	for _, path := range paths {
		if data, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(data)) != "" {
			out = append(out, `<project_instructions path="`+path+`">`+"\n"+string(data)+"\n</project_instructions>")
		}
	}
	return out
}

// openAgy opens the session's agy harness in place of PiG: the dyna tools, and GitHub's when the engine serves them, are its MCP servers.
// The profile directory is the host's own, mounted read-write: the box never reads or copies a file in it.
func openAgy(ctx context.Context, hello contract.BoxHelloResponse, workdir, sessions string, contextFiles []string, notice func(string)) (*agy.Harness, error) {
	if hello.Agy == nil {
		return nil, errors.New("the hello names the agy harness without its files")
	}
	servers := []agy.MCPServer{{Name: "dyna", URL: pig.DynaMCPURL}}
	if hello.GitHub {
		servers = append(servers, agy.MCPServer{Name: "github", URL: pig.GitHubMCPURL})
	}
	return agy.Open(ctx, agy.Config{
		Binary:              hello.Agy.Binary,
		GeminiDir:           hello.Agy.GeminiDir,
		Dir:                 workdir,
		SessionDir:          sessions,
		Model:               hello.Model,
		Instructions:        hello.Instructions,
		ProjectInstructions: agyProjectInstructions(contextFiles),
		ModelInstructions:   hello.ModelInstructions,
		MCPServers:          servers,
		New:                 hello.Fresh,
		Notice:              notice,
	})
}
