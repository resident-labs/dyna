package harnessconfig

import (
	"context"
	"encoding/json"
	"os"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/harness/config"
	"dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/room"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/shellmcp"
	"dyna-agent/internal/shells"
	"dyna-agent/internal/subagent"
	"github.com/spf13/cobra"
)

func NewMCPCommand() *cobra.Command {
	c := &cobra.Command{Use: "mcp", Short: "Manage MCP servers across harnesses"}
	AddCatalogList(c, "mcp")
	c.AddCommand(&cobra.Command{Use: "serve <generation-directory> <server>", Hidden: true, Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, a []string) error { return config.RunGeneratedMCP(a[0], a[1]) }})
	c.AddCommand(shellCommand())
	return c
}

// shellCommand serves Dyna's shell tools over stdio for the current session.
func shellCommand() *cobra.Command {
	return &cobra.Command{Use: "shell", Short: "Serve Dyna's shell tools over MCP stdio", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		owner := runstore.OwnerFromEnv()
		if owner == "" {
			// Grok's shared agent process spawns one MCP server per session with
			// GROK_SESSION_ID in its environment; the server maps that harness
			// session to its Dyna identity.
			if grokSession := os.Getenv("GROK_SESSION_ID"); grokSession != "" {
				if caller, ok := sessionResolver(subagent.DefaultStore())(grokSession); ok {
					owner = caller.Owner
				}
			}
		}
		tools := &shellmcp.Tools{Store: shells.DefaultStore(), Exe: exe, Owner: owner, Cwd: sessionCwd(subagent.DefaultStore(), owner, cwd), Resolve: sessionResolver(subagent.DefaultStore()), Search: engineSearch, RoomMCP: engineRoomMCP(), Steer: steerWatch}
		// An update replaces the binary this server started from; when the
		// state it serves moves past this build, restart on that binary so a
		// schema bump never cuts the session's shells off (D-171).
		if restart := restartOutdatedServer(); restart != nil {
			tools.SetRestart(restart)
		}
		// Only a session hears when a background job ends. Elsewhere, for
		// example in a workflow worker, each command runs to its end.
		if !runstore.IsSessionDirID(tools.Owner) {
			tools.Foreground = true
		} else if rec, err := room.DefaultStore().ResolveContext("", false); err == nil {
			tools.RoomID, tools.Room = rec.ID, rec.Name
		}
		return shellmcp.Serve(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), tools)
	}}
}

// steerWatch returns a signal that fires when the engine queues steering
// for a session: it appends a steer_submitted line to the session's event
// journal when it hands a post to the running turn. A blocking shell wait
// that ends on the signal lets the harness read the message at its tool
// boundary (D-349). Anything but a session, or a session with no journal,
// never interrupts a wait.
func steerWatch(session string) (<-chan struct{}, func()) {
	if !runstore.IsSessionDirID(session) {
		return nil, func() {}
	}
	path, err := subagent.DefaultStore().EventsPath(session)
	if err != nil {
		return nil, func() {}
	}
	return shellmcp.WatchSteerJournal(path)
}

// engineSearch runs one web search in the engine, which holds every provider
// credential. The MCP process itself never gets a token or a key.
func engineSearch(ctx context.Context, in callspec.WebSearchRequest) (callspec.WebSearchResponse, error) {
	var answer callspec.WebSearchResponse
	if err := engineClient().Call(ctx, "WebSearch", []any{in}, &answer); err != nil {
		return callspec.WebSearchResponse{}, err
	}
	return answer, nil
}

// engineRoomMCP reaches the room's outside MCP servers through the engine. The
// engine holds each token and reads the caller from the socket.
func engineRoomMCP() *shellmcp.RoomMCP {
	return &shellmcp.RoomMCP{
		Access: func(ctx context.Context) (bool, error) {
			var granted bool
			err := engineClient().Call(ctx, "RoomMCPAccess", nil, &granted)
			return granted, err
		},
		Tools: func(ctx context.Context) (string, error) {
			var text string
			err := engineClient().Call(ctx, "RoomMCPAgentTools", nil, &text)
			return text, err
		},
		Invoke: func(ctx context.Context, in callspec.RoomMCPCallRequest) (string, error) {
			var text string
			err := engineClient().Call(ctx, "RoomMCPCall", []any{in}, &text)
			return text, err
		},
	}
}

// sessionResolver finds the open session whose harness session has the given
// ID. A harness that runs one MCP server for many sessions names the calling
// session in each call. The job gets the session's Dyna identity, as its
// worker would.
func sessionResolver(store *subagent.Store) func(string) (shellmcp.Caller, bool) {
	return func(harnessSession string) (shellmcp.Caller, bool) {
		list, err := store.ListSnapshot(false)
		if err != nil {
			return shellmcp.Caller{}, false
		}
		for _, in := range list {
			if in.HarnessSession == harnessSession {
				return shellmcp.Caller{Owner: in.ID, RoomID: in.RoomID, Room: in.Room, Cwd: in.Dir, Env: subagent.StampEnv(in.Meta)}, true
			}
		}
		return shellmcp.Caller{}, false
	}
}

// sessionCwd is the session's working directory. A harness may start its MCP
// servers elsewhere (one may use a plugin folder), so a session's own
// directory wins over the server's.
func sessionCwd(store *subagent.Store, owner, fallback string) string {
	if meta, err := store.ReadMeta(owner); err == nil && meta.Dir != "" {
		if info, err := os.Stat(meta.Dir); err == nil && info.IsDir() {
			return meta.Dir
		}
	}
	return fallback
}

// AddCatalogList exposes only Dyna's catalog. Personal MCP/skill import is retired.
// The skill catalog is global, so only an MCP list takes --harness.
func AddCatalogList(parent *cobra.Command, kind string) {
	var harness string
	c := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := runtime.New().EnsureCatalog(); err != nil {
			return err
		}
		snap, err := config.NewConfigManager().Catalog(harness)
		if err != nil {
			return err
		}
		var entries any = snap.MCP
		if kind == "skill" {
			entries = snap.Skills
		}
		e := json.NewEncoder(cmd.OutOrStdout())
		e.SetIndent("", "  ")
		return e.Encode(entries)
	}}
	if kind == "mcp" {
		c.Flags().StringVar(&harness, "harness", "", "limit to one harness ID")
	}
	parent.AddCommand(c)
}
