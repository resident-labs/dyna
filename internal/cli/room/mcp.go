package room

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"dyna-agent/internal/cli/goal"
	"dyna-agent/internal/roommcp"
)

// readToken reads a token from a terminal prompt or from one line of stdin.
// It never comes from an argument, which a process list and shell history show.
func readToken(c *cobra.Command) (string, error) {
	if f, ok := c.InOrStdin().(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(c.ErrOrStderr(), "Token (input is hidden): ")
		raw, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(c.ErrOrStderr())
		if err != nil {
			return "", errors.New("the token could not be read from the terminal")
		}
		return strings.TrimSpace(string(raw)), nil
	}
	line, err := bufio.NewReader(io.LimitReader(c.InOrStdin(), 8<<10)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", errors.New("the token could not be read from standard input")
	}
	token := strings.TrimRight(line, "\r\n")
	if token == "" || strings.ContainsAny(token, " \t") {
		return "", errors.New("enter the token on one line")
	}
	return token, nil
}

// mcpCmd is `dyna room mcp`. Every command acts on the caller's own room, and
// the engine checks who the caller is.
func mcpCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Attach an outside MCP server to this room",
		Long: "A room can use one or more outside MCP servers over HTTPS. The engine holds the token and makes every call; " +
			"an agent never sees the token. Every answer reaches an agent wrapped as untrusted data, and every call is logged in the room. " +
			"Set up in order: save the token, attach (this pins the server's tool list with every tool off), allow the read tools you want, then grant sessions or a profile. " +
			"Known limit: the token is stored in Dyna's private account store. A process that runs as your Unix user can still read that file, as it can read every credential Dyna saves. " +
			"Use the narrowest read-only token the server offers.",
	}
	cmd.PersistentFlags().BoolVar(&asJSON, "json", false, "print JSON")
	call := func(c *cobra.Command, method string, out any, args ...any) error {
		return engineClient().Call(c.Context(), method, append([]any{goal.MutationRoom("")}, args...), out)
	}
	done := func(c *cobra.Command, format string, args ...any) error {
		fmt.Fprintf(c.OutOrStdout(), format+"\n", args...)
		return nil
	}

	token := &cobra.Command{Use: "token", Short: "Save or remove a server's token", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error { return c.Help() }}
	var serverURL string
	set := &cobra.Command{
		Use:   "set <name> --url <https-url>",
		Short: "Save a server's token (owner only); the value comes from stdin or a hidden prompt",
		Long: "Save the token and URL of a server for this room. Only the owner can run this. The token is read from a hidden terminal prompt or from one line of standard input, never from an argument. " +
			"Run it again to rotate the token. Ask the server for the narrowest scope it offers, such as read only. Saving does not contact the server: run `dyna room mcp attach <name>` next.",
		Example: "  dyna room mcp token set board --url https://mcp.example.com/mcp < token.txt",
		Args:    cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, a []string) error {
			value, err := readToken(c)
			if err != nil {
				return err
			}
			if err := call(c, "RoomMCPSetToken", nil, a[0], serverURL, value); err != nil {
				return err
			}
			return done(c, "saved the token for %s; next: dyna room mcp attach %s", a[0], a[0])
		},
	}
	set.Flags().StringVar(&serverURL, "url", "", "the server's HTTPS URL")
	_ = set.MarkFlagRequired("url")
	token.AddCommand(set, &cobra.Command{Use: "rm <name>", Short: "Delete a server's token and detach it (owner only)", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		if err := call(c, "RoomMCPRemoveToken", nil, a[0]); err != nil {
			return err
		}
		return done(c, "deleted the token for %s", a[0])
	}})

	attach := &cobra.Command{Use: "attach <name>", Short: "Connect and pin the tool list, every tool off (owner only)", Long: "Connect with the saved token and pin the server's tool list. Every tool starts off. Run it again after `detach` to restore the server. It fails, and sends nothing, when no token is saved.", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		if err := call(c, "RoomMCPAttach", nil, a[0]); err != nil {
			return err
		}
		return done(c, "attached %s; every tool is off. Allow read tools with: dyna room mcp allow %s <tool>", a[0], a[0])
	}}
	detach := &cobra.Command{Use: "detach <name>", Short: "Kill switch: stop all calls to a server at once (owner or root)", Long: "Refuse new calls and cancel calls in flight. Run `attach` again to restore the server.", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		if err := call(c, "RoomMCPDetach", nil, a[0]); err != nil {
			return err
		}
		return done(c, "detached %s", a[0])
	}}
	remove := &cobra.Command{Use: "remove <name>", Short: "Delete a server, its grants and its token (owner only)", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		if err := call(c, "RoomMCPRemove", nil, a[0]); err != nil {
			return err
		}
		return done(c, "removed %s", a[0])
	}}

	var refresh bool
	tools := &cobra.Command{Use: "tools <name> [--refresh]", Short: "Show the pinned tools (owner or root)", Long: "Show the pinned tool names and whether each is allowed. --refresh reads the server's live list: a new tool starts off and a tool the server dropped is forgotten. Only the owner can refresh.", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		var list []roommcp.ToolView
		if err := call(c, "RoomMCPTools", &list, a[0], refresh); err != nil {
			return err
		}
		if asJSON {
			return json.NewEncoder(c.OutOrStdout()).Encode(list)
		}
		for _, t := range list {
			state := "off"
			if t.Allowed {
				state = "read"
			}
			fmt.Fprintf(c.OutOrStdout(), "%s  %s\n", state, t.Name)
		}
		return nil
	}}
	tools.Flags().BoolVar(&refresh, "refresh", false, "read the live tool list from the server")

	var off, write bool
	allow := &cobra.Command{Use: "allow <name> <tool>... [--off]", Short: "Allow read calls to pinned tools (owner only)", Long: "Allow read calls to the named pinned tools, or turn them off with --off. Dyna does not trust a server's own read-only hint: you choose each tool. Write tools are not available yet.", Args: cobra.MinimumNArgs(2), RunE: func(c *cobra.Command, a []string) error {
		if write {
			return errors.New("write tools are not available yet")
		}
		if err := call(c, "RoomMCPAllow", nil, a[0], a[1:], off); err != nil {
			return err
		}
		return done(c, "updated %d tool(s) of %s", len(a)-1, a[0])
	}}
	allow.Flags().BoolVar(&off, "off", false, "turn the tools off")
	allow.Flags().BoolVar(&write, "write", false, "allow write calls (not available yet)")

	var session, profile string
	var revoke bool
	grant := &cobra.Command{Use: "grant <name> --session <session> | --profile <profile> [--revoke]", Short: "Let one session, or every session on a profile, call allowed tools (owner or root)", Long: "A grant covers one session or every session that runs on a profile. Children do not inherit it. Root has no access unless it grants itself. Read grants only.", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		if (session == "") == (profile == "") {
			return errors.New("give --session or --profile, not both")
		}
		kind, subject := roommcp.GrantSession, session
		if profile != "" {
			kind, subject = roommcp.GrantProfile, profile
		}
		if err := call(c, "RoomMCPGrant", nil, a[0], kind, subject, revoke); err != nil {
			return err
		}
		return done(c, "updated the %s grant of %s on %s", kind, subject, a[0])
	}}
	grant.Flags().StringVar(&session, "session", "", "the session to grant")
	grant.Flags().StringVar(&profile, "profile", "", "the profile to grant")
	grant.Flags().BoolVar(&revoke, "revoke", false, "remove the grant")

	var removeTerm bool
	blockTerm := &cobra.Command{Use: "block-term <name> <term> [--remove]", Short: "Block a term from every call's arguments (owner only)", Long: "Add a word or phrase, such as your name, that must never appear in the arguments of a call. Dyna stores only a hash, so the list is not a second copy of personal data.", Args: cobra.ExactArgs(2), RunE: func(c *cobra.Command, a []string) error {
		if err := call(c, "RoomMCPBlockTerm", nil, a[0], a[1], removeTerm); err != nil {
			return err
		}
		return done(c, "updated the block terms of %s", a[0])
	}}
	blockTerm.Flags().BoolVar(&removeTerm, "remove", false, "remove the term")
	var removeLogin bool
	allowLogin := &cobra.Command{Use: "allow-login <name> <login> [--remove]", Short: "Let a GitHub login appear in a call's arguments (owner only)", Long: "Dyna blocks every GitHub login it knows from a call's arguments. Allow the logins this server may see, such as a fork account.", Args: cobra.ExactArgs(2), RunE: func(c *cobra.Command, a []string) error {
		if err := call(c, "RoomMCPAllowLogin", nil, a[0], a[1], removeLogin); err != nil {
			return err
		}
		return done(c, "updated the allowed logins of %s", a[0])
	}}
	allowLogin.Flags().BoolVar(&removeLogin, "remove", false, "remove the login")

	list := &cobra.Command{Use: "list", Short: "List this room's MCP servers", Long: "List the servers, whether each is attached, and how many tools are pinned and allowed. It never shows a token.", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		var servers []roommcp.ServerView
		if err := call(c, "RoomMCPList", &servers); err != nil {
			return err
		}
		if asJSON {
			return json.NewEncoder(c.OutOrStdout()).Encode(servers)
		}
		for _, s := range servers {
			state := "detached"
			if s.Attached {
				state = "attached"
			}
			fmt.Fprintf(c.OutOrStdout(), "%s  %s  %s  %d/%d tools allowed  grants: %s\n", s.Name, state, s.URL, s.Allowed, s.Tools, strings.Join(s.Grants, ","))
		}
		return nil
	}}

	cmd.AddCommand(token, attach, tools, allow, grant, detach, remove, blockTerm, allowLogin, list)
	return cmd
}
