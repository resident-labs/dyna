// Package room registers `dyna room` (create, list, log, read, members,
// watch) and `dyna post`, the one command that sends a post.
package room

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/subagent"
)

func store() *room.Store { return room.DefaultStore() }

// RoomCmd is `dyna room`.
func RoomCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "room",
		Short: "Create and inspect Dyna rooms",
		Args:  cobra.NoArgs,
		RunE:  func(c *cobra.Command, _ []string) error { return c.Help() },
		Long: "A room is one durable feed and namespace for one related body of work. " +
			"Its members are the sessions started in it; their addresses are <room>:<name>. " +
			"Every member and the owner can read every post. Mentions decide who wakes, not who can read. " +
			"Work started without --room goes to the current repo's room; outside a repo, pass --room.",
	}
	cmd.AddCommand(sessionControlsCmd("pause"), sessionControlsCmd("resume"), sessionControlsCmd("clear-dormant"), sessionControlsCmd("session-controls"), createCmd(), editCmd(), setCmd(), repoCmd(), listCmd(), deleteCmd(), logCmd(), searchCmd(), readCmd(), membersCmd(), watchCmd(), pinCmd(), unpinCmd(), pinsCmd())
	return cmd
}

func createCmd() *cobra.Command {
	var jsonOut bool
	var repo, githubAccess string
	var rootProfile string
	cmd := &cobra.Command{
		Use:   "create [<name>] [--repo <path>] [--github read-only|read-write]",
		Short: "Create or reuse a room and print its name",
		Long: "A repo names its room. --repo <path> derives the name from the git repo at path, using " +
			"the main worktree so every linked worktree maps to the same room. <name> sets an explicit " +
			"name. Without --repo, the current directory supplies the repo when it is inside Git. " +
			"Outside Git, an explicit name creates a room without a repo.\n\n" +
			"Create is idempotent: the same repo returns its existing room, regardless of the requested name, " +
			"and revives it if archived. Without a matching repo, a name held by a different repo is an error. " +
			"New rooms default to read-only GitHub access. Use --github read-write to allow writes; " +
			"reusing a room keeps its saved policy.",
		Example: "  room=$(dyna room create --repo \"$PWD\")\n  dyna room create design-notes",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			in, err := createInput(args, repo, cmd.Flags().Changed("repo"))
			if err != nil {
				return err
			}
			in.GitHubAccess = room.GitHubAccess(githubAccess)
			if err := room.ValidateGitHubAccess(in.GitHubAccess); err != nil {
				return err
			}
			// The engine keeps this only for the owner; it names a session or
			// worker from the caller.
			in.CreatedBy = runstore.OwnerFromEnv()
			in.RootProfile = rootProfile
			var rec room.Record
			if err := engineClient().Call(cmd.Context(), "CreateRoomRecord", []any{in}, &rec); err != nil {
				return fmt.Errorf("the room was not created: %w", err)
			}
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), rec)
			}
			fmt.Fprintln(cmd.OutOrStdout(), rec.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&githubAccess, "github", string(room.GitHubReadOnly), "GitHub access for a new room: read-only or read-write (existing rooms keep their policy)")
	cmd.Flags().StringVar(&repo, "repo", "", "derive the room from the git repo at this path")
	cmd.Flags().StringVar(&rootProfile, "root-profile", "", "profile for a new room's internal root (empty uses the global default)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the full room record as JSON")
	return cmd
}

// upstreamOrOff shows an empty upstream as off, the value that clears it.
func upstreamOrOff(upstream string) string {
	if upstream == "" {
		return "off"
	}
	return upstream
}

// setCmd is `dyna room set`: one room setting, shown without a value.
func setCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set <room> <setting> [<value>]",
		Short: "Change a room setting",
		Long: "Change a room setting, or show it without a value. Only the owner and the room's root may change one.\n" +
			"checkInInterval is how often Dyna wakes a parent to check its children in this room: a duration such as 30m,\n" +
			"off to stop check-ins in this room, or default to use the parent check-ins hook's global interval.\n" +
			"limits.memoryHigh, limits.memoryMax and limits.tasksMax are the room's resource limits: a size such as 2G for\n" +
			"memory, a count such as 512 for tasks, off to remove one limit, or default to use the machine default.\n" +
			"upstream is the parent repository of this room's fork, such as github.com/owner/repo, so that `pr track`\n" +
			"accepts a PR opened on that parent; off clears it.",
		Args: cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[1] != "checkInInterval" && args[1] != "upstream" && !strings.HasPrefix(args[1], "limits.") {
				return fmt.Errorf("unknown room setting %q; checkInInterval, upstream and limits.memoryHigh, limits.memoryMax and limits.tasksMax are the settings this command changes", args[1])
			}
			rec, err := store().Resolve(args[0])
			if err != nil {
				return err
			}
			if args[1] == "upstream" {
				if len(args) == 2 {
					fmt.Fprintf(cmd.OutOrStdout(), "%s\n", upstreamOrOff(rec.Upstream))
					return nil
				}
				upstream, err := room.ParseUpstream(args[2])
				if err != nil {
					return err
				}
				if err := engineClient().Call(cmd.Context(), "SetRoomUpstream", []any{rec.Name, args[2]}, nil); err != nil {
					return fmt.Errorf("the room was not updated: %w", err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s upstream: %s\n", rec.Name, upstreamOrOff(upstream))
				return nil
			}
			if args[1] == "checkInInterval" {
				if len(args) == 2 {
					fmt.Fprintf(cmd.OutOrStdout(), "%s\n", rec.CheckIn.String())
					return nil
				}
				setting, err := room.ParseCheckIn(args[2])
				if err != nil {
					return err
				}
				if err := engineClient().Call(cmd.Context(), "SetRoomCheckIn", []any{rec.Name, args[2]}, nil); err != nil {
					return fmt.Errorf("the room was not updated: %w", err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s parent check-ins: %s\n", rec.Name, setting.String())
				return nil
			}
			field, err := room.ParseLimitField(strings.TrimPrefix(args[1], "limits."))
			if err != nil {
				return err
			}
			if len(args) == 2 {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\n", field.Format(rec.Limits.LimitOf(field)))
				return nil
			}
			limit, err := field.Parse(args[2])
			if err != nil {
				return err
			}
			var result roomsvc.SetRoomLimitResult
			if err := engineClient().Call(cmd.Context(), "SetRoomLimit", []any{rec.Name, string(field), args[2]}, &result); err != nil {
				return fmt.Errorf("the room was not updated: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s: %s\n", rec.Name, field.Label(), field.Format(limit))
			if result.ApplyWarning != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "warning: %s\n", result.ApplyWarning)
			}
			return nil
		},
	}
	return cmd
}

func editCmd() *cobra.Command {
	var rootProfile string
	cmd := &cobra.Command{
		Use:   "edit <room> --root-profile <name>",
		Short: "Choose the profile for the room's next internal root",
		Long:  "Choose the profile for the room's next internal root. Pass an empty name to inherit root.profile. Existing root sessions keep their current profile.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// The engine client requires a heterogeneous positional argument slice.
			if err := engineClient().Call(cmd.Context(), "SetRoomRootProfile", []any{args[0], rootProfile}, nil); err != nil {
				return fmt.Errorf("the room was not updated: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&rootProfile, "root-profile", "", "root profile name (empty uses the global default)")
	_ = cmd.MarkFlagRequired("root-profile")
	return cmd
}

// repoCmd is `dyna room repo`: the repositories a room accepts for local changes.
func repoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repo",
		Short: "Choose the repositories a room accepts for local changes",
		Long: "A room accepts local changes from checkouts of its own repository. " +
			"Register another repository, such as a private sidecar, so dyna git change open accepts its checkouts. " +
			"Only the owner and the room's root may add or remove one. A registered repository that left the disk is listed as missing.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error { return c.Help() },
	}
	var upstream string
	add := &cobra.Command{
		Use:   "add <room> <path>",
		Short: "Accept checkouts of another repository",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := filepath.Abs(args[1])
			if err != nil {
				return err
			}
			if err := engineClient().Call(cmd.Context(), "AddRoomRepo", []any{args[0], path, upstream}, nil); err != nil {
				return fmt.Errorf("the room was not updated: %w", err)
			}
			return nil
		},
	}
	add.Flags().StringVar(&upstream, "upstream", "", "parent repository of this checkout's fork, as host/owner/repo; empty keeps none")
	remove := &cobra.Command{
		Use:   "remove <room> <path>",
		Short: "Stop accepting a registered repository",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := filepath.Abs(args[1])
			if err != nil {
				return err
			}
			if err := engineClient().Call(cmd.Context(), "RemoveRoomRepo", []any{args[0], path}, nil); err != nil {
				return fmt.Errorf("the room was not updated: %w", err)
			}
			return nil
		},
	}
	list := &cobra.Command{
		Use:   "list <room>",
		Short: "List the repositories a room accepts",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rec, err := store().Resolve(args[0])
			if err != nil {
				return err
			}
			return printJSON(cmd.OutOrStdout(), rec.Repos())
		},
	}
	cmd.AddCommand(add, remove, list)
	return cmd
}

// createInput resolves the requested name and canonical repo for create.
func createInput(args []string, repo string, repoSet bool) (room.CreateInput, error) {
	var in room.CreateInput
	if len(args) == 1 {
		in.Name = strings.TrimSpace(args[0])
	}
	if !repoSet {
		repo = "."
	}
	if strings.TrimSpace(repo) == "" {
		return in, errors.New("--repo needs a path")
	}
	root, err := room.RepoRoot(repo)
	if err != nil {
		if errors.Is(err, room.ErrNotRepo) && !repoSet && in.Name != "" {
			return in, nil
		}
		if errors.Is(err, room.ErrNotRepo) && !repoSet {
			return in, errors.New("the current directory is not inside a git repo; " +
				"pass --repo <path> for a repo or an explicit name: dyna room create <name>")
		}
		return in, err
	}
	in.Repo = root
	return in, nil
}

func listCmd() *cobra.Command {
	var all, jsonOut bool
	cmd := &cobra.Command{
		Use:   "list [--all]",
		Short: "List active rooms",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			live := notify.Default().ActiveRoomIDs()
			records, err := store().ListWithActivity(all, live)
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), records)
			}
			now := time.Now()
			for _, rec := range records {
				state := "active"
				if rec.Archived(now, live[rec.ID]) {
					state = "archived"
				} else if rec.SessionPause.Paused {
					state = "paused"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\tlast %s\n", rec.Name, rec.ID, state, rec.LastActivity.Format(time.RFC3339))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "include archived rooms")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print records as JSON")
	return cmd
}

func deleteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <room>",
		Short: "Delete a room and its log for good",
		Long: "Delete removes the room record and its whole post log. This cannot be undone. " +
			"<room> is a name or an ID.\n\n" +
			"Delete refuses while the room has an open session (running, queued or idle) or a " +
			"running run or shell. It lists what blocks. Close or stop those first with " +
			"`dyna subagent close`, `dyna runs cancel` or `dyna shell stop`. Closed sessions " +
			"do not block, and their records stay.",
		Example: "  dyna room delete old-experiment",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rec, err := store().Resolve(args[0])
			if err != nil {
				return err
			}
			if err := roomsvc.New(nil).DeleteRoom(cmd.Context(), rec.ID); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "deleted room %s (%s)\n", rec.Name, rec.ID)
			return nil
		},
	}
	return cmd
}

func logCmd() *cobra.Command {
	var all, jsonOut bool
	var tail int
	var since string
	var kinds []string
	cmd := &cobra.Command{
		Use:   "log <room> [flags]",
		Short: "Show the last 30 agent posts to the owner",
		Long: "Show posts in append order. Defaults to the last 30 matching ordinary messages and " +
			"session turn reports addressed to the owner, plus the human owner's posts with a mention. " +
			"--all shows every kind and recipient without a tail. Explicit --tail and --kind " +
			"filters also apply with --all. --since selects posts after a stored post ID.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rec, err := store().Resolve(args[0])
			if err != nil {
				return err
			}
			opts := room.LogOptions{All: all, Since: since, Kinds: kinds}
			if cmd.Flags().Changed("tail") {
				opts.Tail = &tail
			}
			out, hidden, err := store().Log(rec, opts)
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), map[string]any{"room": rec, "posts": out, "hidden": hidden})
			}
			if hidden > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "%d older matching posts hidden; use --tail 0 to see them.\n", hidden)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s (%s)\ncreated %s, last activity %s\n\n", rec.Name, rec.ID, rec.CreatedAt.Format(time.RFC3339), rec.LastActivity.Format(time.RFC3339))
			for _, msg := range out {
				fmt.Fprintf(cmd.OutOrStdout(), "%s  %s %s -> %s [%s] %s\n", time.UnixMilli(msg.TS).Format("2006-01-02 15:04:05"), msg.ID, msg.From, strings.Join(msg.To, ", "), msg.Kind, oneline(msg.Text))
				if line := wokeLine(rec.ID, msg); line != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", line)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "show every kind and recipient with no default tail")
	cmd.Flags().IntVar(&tail, "tail", 30, "show the last N matching posts (0 = no limit)")
	cmd.Flags().StringVar(&since, "since", "", "show posts after this post ID")
	cmd.Flags().StringArrayVar(&kinds, "kind", nil, "select a stored kind, such as message, completed, failed or stopped (repeatable)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the room, filtered posts and hidden count as JSON")
	return cmd
}

func readCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "read <room> <post-id>",
		Short: "Print one stored post in full",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			rec, err := store().Resolve(args[0])
			if err != nil {
				return err
			}
			msg, err := store().Message(rec.ID, args[1])
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), msg)
			}
			reader := ""
			if cur, ok := mode.CurrentSession(); ok {
				if info, err := notify.Default().Sessions.Store.Inspect(cur.ID); err == nil {
					reader = info.Address()
				}
			}
			reply := notify.Default().Sessions.Store.ReplyAddress(msg)
			files, _ := store().FilesDir(rec.ID)
			fmt.Fprintln(cmd.OutOrStdout(), msg.Frame(room.FrameOptions{Room: rec.Name, Reader: reader, Reply: reply, FilesDir: files}))
			if lines := deliveryReport(rec, msg.ID); len(lines) > 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "Deliveries:")
				for _, line := range lines {
					fmt.Fprintln(cmd.OutOrStdout(), "  "+line)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the stored post as JSON")
	return cmd
}

func printJSON(w io.Writer, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

func oneline(s string) string { return strings.Join(strings.Fields(s), " ") }

func membersCmd() *cobra.Command {
	var all, jsonOut bool
	cmd := &cobra.Command{
		Use:   "members <room> [--all]",
		Short: "Show a room's member sessions as a tree, grouped by parent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rec, err := store().Resolve(args[0])
			if err != nil {
				return err
			}
			sessions, err := notify.Default().Sessions.Store.ListSnapshot(true)
			if err != nil {
				return err
			}
			membership := subagent.RoomMembership(sessions, rec.ID, all)
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), membership.Sessions)
			}
			selfID, selfParent := "", ""
			if current, ok := mode.CurrentSession(); ok {
				for _, info := range membership.Sessions {
					if info.ID == current.ID {
						selfID, selfParent = info.ID, info.ParentSession
						break
					}
				}
			}
			fmt.Fprintln(cmd.OutOrStdout(), rec.Name+" (root)")
			var walk func(nodes []*subagent.MemberNode, prefix string)
			walk = func(nodes []*subagent.MemberNode, prefix string) {
				for i, node := range nodes {
					last := i == len(nodes)-1
					branch := "├─ "
					next := prefix + "│  "
					if last {
						branch = "└─ "
						next = prefix + "   "
					}
					marker := ""
					if node.Info.ID == selfID {
						marker = " ← you"
					} else if selfID != "" && node.Info.ParentSession == selfParent {
						marker = " · sibling"
					}
					fmt.Fprintf(cmd.OutOrStdout(), "%s%s%s (%s, %s)%s\n", prefix, branch, node.Info.Address(), node.Info.Status, node.Info.Profile, marker)
					walk(node.Children, next)
				}
			}
			walk(membership.Roots, "")
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "include closed sessions")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print matching sessions as JSON")
	return cmd
}

// PostCmd is `dyna post`, the only command that sends a post.
// engineClient is the engine client that dyna post, pin and unpin use. Tests
// replace it.
var engineClient = func() *engineclient.Client { return engineclient.New("") }

// sendPost asks the engine to store and deliver a post (requirement 2).
// Package tests replace it with the same posting path, run in-process.
var sendPost = callEnginePost

// callEnginePost sends a post to the engine. A post the engine did not take
// exits non-zero with the cause (requirement 26).
func callEnginePost(ctx context.Context, req notify.PostRequest) (notify.Result, error) {
	var result notify.Result
	if err := engineClient().Call(ctx, "Post", []any{req}, &result); err != nil {
		return notify.Result{}, fmt.Errorf("the post was not sent: %w", err)
	}
	return result, nil
}

func PostCmd() *cobra.Command {
	var replyTo string
	var alert, quiet bool
	var jsonOut bool
	var attach []string
	var richPath string
	var update string
	var allowNet bool
	var forward string
	cmd := &cobra.Command{
		Use:   "post <@mention>... [--] <text...>",
		Short: "Post to a room",
		Long: "Store one post in a room and deliver it to every mentioned recipient.\n\n" +
			"Mentions: @<name> (a member of your room), @<room>:<name>, @root (the room owner),\n" +
			"@<room> (that room's owner), @team (the owner's top-level sessions), @parent, @siblings,\n" +
			"@children, and @run:<run-id>/agent:<n>.\n" +
			"Bare mentions resolve in your stamped room. Outside Dyna, qualify them: @<room>:<name>,\n" +
			"@<room>:children. All recipients must be in one room. Every member can read every post.\n\n" +
			"Who wakes: a post wakes each session it mentions. @root (or @<room>:root) wakes the room's root\n" +
			"when a session, the desktop, or an owner-run script posts it; so does @parent when the parent is root.\n" +
			"A script, CI hook or bridge that runs dyna post outside Dyna posts as the owner and must write\n" +
			"@<room>:root. A post that only mentions @<room> or @<room>:<name> of a closed session wakes no one.\n" +
			"--quiet stores the post as context that each mentioned session reads with its next turn.\n" +
			"It wakes no one, so a bridge or log can forward every line and wake only on chosen ones.\n" +
			"Run dyna guide posting for the full table.\n\n" +
			"Leading arguments that start with @ are mentions; use -- before text that starts with @.\n\n" +
			"--attach saves a file with the room and lists it in the post; repeat it for more files.\n" +
			"With --attach the text may be empty. Mentions are read from the text only, never from files.\n" +
			"Cards and files each default to 25 MiB. dyna settings sets content.maxCardBytes and\n" +
			"content.maxFileBytes with sizes such as 25 MiB (MB also means MiB). Changes apply to new uploads and posts\n" +
			"without restart. A post allows eight attachments, including at most one card.\n\n" +
			"--forward <post-id> forwards a post from the room. The recipients get the original text, who wrote it\n" +
			"and when. Its files are referenced, not copied. The text is an optional note.\n\n" +
			"--html <file.html> attaches one HTML file that the Dyna desktop and web apps show as a card in\n" +
			"the feed. Agents and text readers see only the post text and a file line, so the text must state\n" +
			"the key result. Use it for something a person should see: a chart, a table, a diagram, a small\n" +
			"tool, and always when the owner asks for an HTML report or a visual. Write one complete file (default limit 25 MiB; content.maxCardBytes) with inline <style> and <script> and a\n" +
			"dark background (the app is dark). Scripts run in a sandbox with no app access and no storage.\n" +
			"Network is HTTPS only (fetch, images, fonts, styles); plain http: and localhost are blocked.\n" +
			"Forms, popups and dialogs do not work. Links do not open by click; to open one in the owner's\n" +
			"browser after a click, call parent.postMessage({type: 'dyna:open', url: 'https://…'}, '*').\n" +
			"To get the owner's input back, use plain inputs and your own Send button (not a <form> submit), and on that click call\n" +
			"parent.postMessage({type: 'dyna:submit', text: '…', choice: '…'}, '*'). text is up to 16 KB (send JSON\n" +
			"for structured input); choice is optional. It works only right after the owner clicks or types in the card.\n" +
			"The app then shows the text under the card, and the owner must press the app's own Send button to send it.\n" +
			"What is sent is marked as written by a card. If the owner presses Cancel, you get ok: false, error: 'cancelled'.\n" +
			"With dyna request question --html, the text (or the choice, when it is one of the options) answers the question. On any\n" +
			"other card it is an owner reply to you, which wakes you. The card gets back\n" +
			"{type: 'dyna:submitted', ok: true|false, error?: 'answered'|'cancelled'|'replaced'} in a window 'message' event.\n" +
			"To size the card, call parent.postMessage({type: 'dyna:height', height: 480}, '*'), or set\n" +
			"<meta name=\"dyna-height\" content=\"480\"> (120-1200 px, default 360). <title> names the card.\n" +
			"Every room member can read the file, so never put secrets in it.\n\n" +
			"--update <post-id> with --html adds a new version to an existing visual instead of a new one.\n" +
			"Get the current file with dyna attachment get, edit it, and post it with --update.",
		Args:               cobra.MinimumNArgs(1),
		DisableFlagParsing: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			if n := cmd.ArgsLenAtDash(); n >= 0 {
				args = append(append(append([]string{}, args[:n]...), "--"), args[n:]...)
			}
			mentions, rest := room.SplitLeadingMentions(args)
			text := strings.Join(rest, " ")
			if strings.TrimSpace(text) == "" && len(attach) == 0 && richPath == "" && forward == "" {
				return errors.New("a post needs text, --attach or --forward")
			}
			if (update != "" || allowNet) && richPath == "" {
				return errors.New("--update and --allow-net need --html")
			}
			if richPath != "" && strings.TrimSpace(text) == "" {
				return errors.New("--html needs post text that sums up the card for readers outside the desktop")
			}
			count := len(attach)
			if richPath != "" {
				count++
			}
			if count > room.MaxPostAttachments {
				return fmt.Errorf("a post can carry at most %d attachments", room.MaxPostAttachments)
			}
			if quiet && alert {
				return errors.New("--quiet and --alert conflict: an alert asks someone to act")
			}
			limits, err := settings.Content()
			if err != nil {
				return err
			}
			var files []notify.File
			for _, path := range attach {
				data, err := settings.ReadContentFile(path, limits.FileBytes)
				if err != nil {
					return err
				}
				files = append(files, notify.File{Name: filepath.Base(path), Data: data})
			}
			if richPath != "" {
				data, err := settings.ReadContentFile(richPath, limits.CardBytes)
				if err != nil {
					return err
				}
				files = append(files, notify.File{Name: filepath.Base(richPath), Data: data, Render: room.RenderHTML, Visual: update,
					AllowNet: allowNet, Preview: preview(cmd.Context(), data, allowNet)})
			}
			svc := notify.Default()
			snd, err := svc.SelfSender()
			if err != nil {
				return err
			}
			kind := ""
			if alert {
				kind = notify.ServiceAlertKind
			}
			result, err := sendPost(cmd.Context(), notify.PostRequest{Kind: kind, Quiet: quiet, Mentions: mentions, Text: text, ReplyTo: replyTo, ForwardOf: forward, Files: files, Claim: notify.ClaimOf(snd)})
			if result.Message.ID == "" {
				return err
			}
			// The post is stored. Report a failed delivery without asking for
			// the same text again.
			if err == nil {
				err = notify.DeliveryError(result)
			}
			if postNeedsCallHint(cmd.Context(), result) {
				fmt.Fprintln(cmd.ErrOrStderr(), "No call is live; use `dyna call --reason` to ring the owner.")
			}
			if jsonOut {
				_ = printJSON(cmd.OutOrStdout(), result)
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "post %s in %s\n", result.Message.ID, roomNameOf(svc, result.Message.RoomID))
			for _, a := range result.Message.Attachments {
				kind := a.MIME
				if a.Rich() {
					kind = fmt.Sprintf("HTML card %q, %d px", a.RichLabel(), a.CardHeight())
					if a.Net {
						kind += ", network allowed"
					}
				}
				fmt.Fprintf(cmd.OutOrStdout(), "  attached %s %s (%s, %s)\n", a.ID, a.Name, kind, room.HumanSize(a.Size))
				if a.Rich() {
					printVisual(cmd.OutOrStdout(), svc.Rooms, result.Message, a)
				}
			}
			for _, d := range result.Deliveries {
				line := fmt.Sprintf("  %s: %s", d.To, d.Delivery)
				if d.Pointer {
					line += " (pointer to the stored post)"
				}
				if d.Error != "" {
					line += " — " + d.Error
				}
				fmt.Fprintln(cmd.OutOrStdout(), line)
			}
			for _, sk := range result.Skipped {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s: skipped (%s)\n", sk.To, sk.Reason)
			}
			return err
		},
	}
	cmd.Flags().StringVar(&replyTo, "reply-to", "", "post id this replies to")
	cmd.Flags().BoolVar(&alert, "alert", false, "report an actionable service problem to the owner or actual parent")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "store the post as context for the recipients' next turn; wake no one")
	cmd.Flags().StringVar(&forward, "forward", "", "id of a post in the room to forward; the text becomes your note")
	cmd.Flags().StringArrayVar(&attach, "attach", nil, "attach a file (repeatable)")
	cmd.Flags().StringVar(&richPath, "html", "", "attach one .html file as a desktop card (needs post text)")
	cmd.Flags().StringVar(&update, "update", "", "with --html: add a new version to the visual this post ID created")
	cmd.Flags().BoolVar(&allowNet, "allow-net", false, "with --html: let the card load images, fonts, scripts and data over HTTPS")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the stored post and each delivery as JSON")
	return cmd
}

// AttachmentCmd is `dyna attachment`: files saved with room posts.
func AttachmentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "attachment",
		Short: "Get files attached to room posts",
		Args:  cobra.NoArgs,
		RunE:  func(c *cobra.Command, _ []string) error { return c.Help() },
	}
	var out string
	get := &cobra.Command{
		Use:   "get <room> <attachment-id> [-o <path>|-o -]",
		Short: "Print where an attachment is saved, or copy its bytes",
		Long: "Without -o, print the saved file's path on this host; open it with any file tool.\n" +
			"-o <path> copies the bytes to path. -o - writes the bytes to stdout.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			rec, err := store().Resolve(args[0])
			if err != nil {
				return err
			}
			path, err := store().AttachmentPath(rec.ID, args[1])
			if err != nil {
				return err
			}
			switch out {
			case "":
				fmt.Fprintln(cmd.OutOrStdout(), path)
				return nil
			case "-":
				f, err := os.Open(path)
				if err != nil {
					return err
				}
				defer f.Close()
				_, err = io.Copy(cmd.OutOrStdout(), f)
				return err
			}
			data, err := settings.ReadStoredFile(path)
			if err != nil {
				return err
			}
			return os.WriteFile(out, data, 0o600)
		},
	}
	get.Flags().StringVarP(&out, "out", "o", "", "copy the bytes to this path (- for stdout)")
	cmd.AddCommand(get)
	return cmd
}

func roomNameOf(svc *notify.Service, id string) string {
	if rec, err := svc.Rooms.Resolve(id); err == nil {
		return rec.Name
	}
	return id
}

func watchCmd() *cobra.Command {
	var since string
	var fromStart, all, lines bool
	cmd := &cobra.Command{
		Use:   "watch <room> [--all|--lines]",
		Short: "Stream a room's posts to the owner, one delivery per waking post",
		Long: "Stream root input as JSON deliveries. Quiet root-addressed posts stay held until " +
			"an alert, action request, owner post or failure wakes root; if no wake comes for " +
			"15 minutes, the engine posts one timed-flush notice that delivers them the same way. " +
			"One delivery contains " +
			"the ordered posts array and the last waking post ID as id. An external root's own " +
			"owner posts are not echoed back, and the owner's posts to agents stay quiet " +
			"context, like every other root-addressed post that does not ask root to act. " +
			"--lines prints the same deliveries as short text lines, one per post, and ends " +
			"each delivery with `-- since <post-id>`: ! marks a post that asks root to act, > another " +
			"waking post and · quiet context. --all streams every raw post without batching. " +
			"--since <post-id> replays posts after that post, including held context.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if lines && all {
				return fmt.Errorf("--lines prints the batched root feed as text; --all is a raw JSON stream: choose one")
			}
			if err := authority.Check("room watch", authority.Current(), nil); err != nil {
				return err
			}
			rec, err := store().Resolve(args[0])
			if err != nil {
				return err
			}
			if !rec.Seat.External {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: the internal root holds the root seat of room %s, so it also answers posts to @root; run `dyna root attach --room %s` to take the seat\n", rec.Name, rec.Name)
			}
			cutoff, skip, err := watchStart(rec, since, fromStart)
			if err != nil {
				return err
			}
			return follow(cmd.Context(), rec, cutoff, skip, all, lines, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&since, "since", "1h", "replay after this post ID, or a window: a duration such as 15m or 1h, or an RFC3339 time")
	cmd.Flags().BoolVar(&fromStart, "from-start", false, "replay from the beginning of the room")
	cmd.Flags().BoolVar(&all, "all", false, "stream every post, not only posts to the owner")
	cmd.Flags().BoolVar(&lines, "lines", false, "print one short text line per post and a since line per delivery, instead of JSON")
	return cmd
}

// watchStart turns --since and --from-start into a replay cutoff and the
// posts to skip. A post ID replays every post after it, as room log does.
func watchStart(rec room.Record, since string, fromStart bool) (time.Time, map[string]bool, error) {
	skip := map[string]bool{}
	if fromStart || !strings.HasPrefix(since, "msg_") {
		cutoff, err := parseSince(since, fromStart)
		return cutoff, skip, err
	}
	msgs, err := store().Messages(rec.ID)
	if err != nil {
		return time.Time{}, nil, err
	}
	after, err := room.After(msgs, since, rec.Name)
	if err != nil {
		return time.Time{}, nil, err
	}
	for _, msg := range msgs[:len(msgs)-len(after)] {
		skip[msg.ID] = true
	}
	return time.Time{}, skip, nil
}

func parseSince(s string, fromStart bool) (time.Time, error) {
	if fromStart {
		return time.Time{}, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid --since %q: use a post ID, a duration or an RFC3339 time", s)
}

// watchDelivery is one monitor notification. ID is the last waking post's
// replay cursor; Posts contains held quiet context followed by that post.
type watchDelivery struct {
	ID    string         `json:"id"`
	Posts []room.Message `json:"posts"`
}

// watchLineText bounds one --lines post text so a notification stays short.
const watchLineText = 200

// watchSeat tells how one post reaches the root seat, from its stored delivery
// rows: the mode of its seat delivery, and false when the post is not the
// seat's context at all.
type watchSeat func(msg room.Message) (room.DeliveryMode, bool, error)

// watchWriter turns posts into monitor output. Both output modes follow the
// root seat's stored delivery rows: quiet posts are held, and a waking post
// delivers them all. The default writes one JSON delivery per waking post;
// --lines writes one short text line per post and a since line per delivery.
// Explicit --all keeps its raw, one-post-per-line JSON output and asks the seat
// about nothing.
func watchWriter(out io.Writer, seat watchSeat, all, lines bool) func(room.Message) error {
	enc := json.NewEncoder(out)
	var held []room.Message
	deliver := func(wake room.Message) error {
		held = append(held, wake)
		delivery := watchDelivery{ID: wake.ID, Posts: held}
		held = nil
		if lines {
			return writeDeliveryLines(out, delivery)
		}
		return enc.Encode(delivery)
	}
	return func(msg room.Message) error {
		if all {
			return enc.Encode(msg)
		}
		mode, ok, err := seat(msg)
		if err != nil || !ok {
			return err
		}
		if mode == room.DeliveryWake {
			return deliver(msg)
		}
		held = append(held, msg)
		return nil
	}
}

// writeDeliveryLines prints one text line per post, then the since line a
// restart passes to --since. The delivery's last post is the waking one.
func writeDeliveryLines(out io.Writer, delivery watchDelivery) error {
	for i, msg := range delivery.Posts {
		marker := "·"
		if i == len(delivery.Posts)-1 {
			marker = ">"
			if msg.NeedsRoot {
				marker = "!"
			}
		}
		kind := msg.Kind
		if kind == "" {
			kind = "message"
		}
		if _, err := fmt.Fprintf(out, "%s %s [%s] %s: %s\n", marker, msg.From, kind, msg.ID, lineText(msg.Text, watchLineText)); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(out, "-- since %s\n", delivery.ID)
	return err
}

// lineText bounds the first text line to at most max runes.
func lineText(s string, max int) string {
	first, _, _ := strings.Cut(s, "\n")
	text := oneline(first)
	if r := []rune(text); len(r) > max {
		return string(r[:max]) + "…"
	}
	return text
}

// follow streams posts newer than cutoff whose IDs are not in seen. Held
// context has no printed cursor, so --since replays it after a restart.
func follow(ctx context.Context, rec room.Record, cutoff time.Time, seen map[string]bool, all, lines bool, out io.Writer) error {
	st := store()
	seat := func(msg room.Message) (room.DeliveryMode, bool, error) {
		d, ok, err := st.SeatDelivery(rec.ID, msg.ID)
		return d.Mode, ok, err
	}
	write := watchWriter(out, seat, all, lines)
	return st.Follow(ctx, rec.ID, func(msg room.Message) (bool, error) {
		if seen[msg.ID] || time.UnixMilli(msg.TS).Before(cutoff) {
			return false, nil
		}
		seen[msg.ID] = true
		return false, write(msg)
	})
}

// postNeedsCallHint checks only posts delivered to the voice call.
func postNeedsCallHint(ctx context.Context, result notify.Result) bool {
	voice := false
	for _, delivery := range result.Deliveries {
		if delivery.VoiceCall {
			voice = true
			break
		}
	}
	if !voice {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	var status struct {
		Live   bool   `json:"live"`
		RoomID string `json:"roomId"`
	}
	err := engineclient.New("").Call(ctx, "VoiceStatus", nil, &status)
	return err != nil || !status.Live || status.RoomID != result.Message.RoomID
}

func wokeLine(roomID string, msg room.Message) string {
	rows, err := store().PostDeliveries(roomID, msg.ID)
	if err != nil {
		return ""
	}
	for _, row := range rows {
		if row.How == room.DeliveryWoke {
			return room.WakeLine(msg.ID, room.WakeSender(msg))
		}
	}
	return ""
}

func deliveryReport(rec room.Record, postID string) []string {
	rows, err := store().PostDeliveries(rec.ID, postID)
	if err != nil || len(rows) == 0 {
		return nil
	}
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		lines = append(lines, row.Summary(room.DeliveryLabel(row.SessionID, rec.Name)))
	}
	return lines
}
