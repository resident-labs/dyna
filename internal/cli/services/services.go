// Package services reads and changes Dyna's services: the work Dyna does by
// itself, such as the PR reviewers, the librarian, Voice and the machine's
// background passes. A service is not a session in the room's hierarchy, so it
// never appears in `dyna room members` or `@children`.
//
// The list comes from one engine call, ServiceRegistry, which the Services page
// also reads, so the two front ends cannot disagree about what a service is.
package services

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"dyna-agent/internal/cli/jsonout"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/hooks"
	"dyna-agent/internal/librarian"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/serviceregistry"
)

// ServiceID names one Dyna service. The set is closed, so a mistyped service
// is an error and never a new one.
type ServiceID = serviceregistry.ID

// The services that run an agent or hold a session.
const (
	Library  = serviceregistry.IDLibrary
	Reviewer = serviceregistry.IDReviews
	Voice    = serviceregistry.IDVoice
)

// aliases accept the words people and prompts use for a service.
var aliases = map[string]ServiceID{
	"librarian": Library, "reviewers": Reviewer, "review": Reviewer, "reviews": Reviewer,
	"voice-call": Voice, "merge": serviceregistry.IDMergeQueue, "checkins": serviceregistry.IDParentCheckIns,
	"update": serviceregistry.IDUpdates, "usage": serviceregistry.IDAccountUsage,
}

// ParseServiceID accepts a service id or a common alias.
func ParseServiceID(text string) (ServiceID, error) {
	id := ServiceID(strings.ToLower(strings.TrimSpace(text)))
	if id == "" {
		return "", fmt.Errorf("name a service: %s", strings.Join(serviceIDNames(), ", "))
	}
	if alias, ok := aliases[string(id)]; ok {
		return alias, nil
	}
	for _, known := range serviceregistry.IDs() {
		if id == known {
			return id, nil
		}
	}
	return "", fmt.Errorf("unknown service %q; services are %s", text, strings.Join(serviceIDNames(), ", "))
}

func serviceIDNames() []string {
	ids := serviceregistry.IDs()
	names := make([]string, len(ids))
	for i, id := range ids {
		names[i] = string(id)
	}
	return names
}

// engineCaller is the engine client a command uses. Its shape mirrors
// engineclient.Client.Call, whose args and result are JSON: each call has its
// own result type, so no single Go type fits them all.
type engineCaller interface {
	Call(ctx context.Context, method string, args []any, out any) error
}

// Engine results this command decodes. Each keeps only the fields it shows, so
// a change on the engine side cannot silently reshape a row.
type serviceRow struct {
	Role     string   `json:"role"`
	Profile  string   `json:"profile"`
	Pinned   string   `json:"pinned"`
	Source   string   `json:"source"`
	Reason   string   `json:"reason"`
	Profiles []string `json:"profiles"`
}

type roomRow struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Repo string `json:"repo"`
}

// refs are the room and repository a command works in. Either may be empty
// when the caller is outside a room or a repository; a command that needs one
// says so.
type refs struct {
	Room string
	Repo string
	// RepoExplicit marks a --repo path the caller chose, which wins over the
	// room's own repository.
	RepoExplicit bool
}

// resolveRefs reads the room and repository from the flag, the session's stamp
// and the current repository. It never fails: `list` still shows what it can.
func resolveRefs(roomFlag, repoFlag string) refs {
	var out refs
	if ref := strings.TrimSpace(roomFlag); ref != "" {
		out.Room = ref
	} else if stamp, ok := mode.StampFromEnv(); ok {
		out.Room = stamp.Room
		if out.Room == "" {
			out.Room = stamp.RoomID
		}
	}
	dir := repoFlag
	out.RepoExplicit = strings.TrimSpace(repoFlag) != ""
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return out
		}
		dir = cwd
	}
	if r, err := repoid.Resolve(context.Background(), dir); err == nil {
		out.Repo = r.Root
	} else if dir != "" {
		out.Repo = dir
	}
	return out
}

// resolve names the room and the repository a command works in. The engine
// names the room, so a flag or stamp that carries an ID still gives the name a
// session address needs, and the room's own repository applies unless --repo
// chose one. Every read and every write goes through it, so a list and a switch
// always describe the same service.
func resolve(ctx context.Context, call engineCaller, r refs) refs {
	rec, ok := resolveRoom(ctx, call, r)
	if !ok {
		return r
	}
	r.Room = rec.Name
	if !r.RepoExplicit && rec.Repo != "" {
		r.Repo = rec.Repo
	}
	return r
}

// resolveRoom finds the room a command works in: the named one, or else the
// room of this repository. It reads the room list and never creates a room.
func resolveRoom(ctx context.Context, call engineCaller, r refs) (roomRow, bool) {
	var rooms []roomRow
	if err := call.Call(ctx, "Rooms", nil, &rooms); err != nil {
		return roomRow{}, false
	}
	for _, rec := range rooms {
		if r.Room != "" && (rec.Name == r.Room || rec.ID == r.Room) {
			return rec, true
		}
	}
	if r.Room != "" || r.Repo == "" {
		return roomRow{}, false
	}
	for _, rec := range rooms {
		if rec.Repo != "" && filepath.Clean(rec.Repo) == filepath.Clean(r.Repo) {
			return rec, true
		}
	}
	return roomRow{}, false
}

// NewCommand is `dyna services`: the services list, and the commands that
// change or read one service.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "services",
		Short: "List and manage what Dyna runs by itself: reviews, the librarian, Voice and background passes",
		Long: "A service is work Dyna does by itself. A hook is a command you add. Services are not\n" +
			"sessions in the room's hierarchy, so they never appear in `dyna room members`,\n" +
			"`@children` or a parent check-in. `list` shows all of them in the groups the app's\n" +
			"Services page uses; both read the same engine list.\n\n" +
			"Change a service's profile here or there, never as part of a bulk move of sessions.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error { return c.Help() },
	}
	cmd.AddCommand(listCmd(), showCmd(), enableCmd(), disableCmd(), profileCmd(), restartCmd(), logCmd())
	return cmd
}

// client is the engine client every command uses; tests replace it.
var client = func() engineCaller { return engineclient.New("") }

// options are the flags a command shares.
type options struct {
	room     string
	repo     string
	asJSON   bool
	allRooms bool
}

func (o *options) bind(c *cobra.Command) {
	c.Flags().StringVar(&o.room, "room", "", "room whose services to read or change (default: the current room)")
	c.Flags().StringVar(&o.repo, "repo", "", "repository path (default: the current one)")
	c.Flags().BoolVar(&o.asJSON, "json", false, "print JSON")
}

func listCmd() *cobra.Command {
	var o options
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Show each service, its switch, profile, state and next run",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			rows, err := readRegistry(c.Context(), client(), resolveRefs(o.room, o.repo))
			if err != nil {
				return err
			}
			if o.asJSON {
				return jsonout.Write(c.OutOrStdout(), rows)
			}
			printRows(c.OutOrStdout(), rows)
			return nil
		},
	}
	o.bind(cmd)
	return cmd
}

func showCmd() *cobra.Command {
	var o options
	cmd := &cobra.Command{
		Use:   "show <service>",
		Short: "Show one service in full",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			id, err := ParseServiceID(args[0])
			if err != nil {
				return err
			}
			row, err := findRow(c.Context(), client(), resolveRefs(o.room, o.repo), id)
			if err != nil {
				return err
			}
			if o.asJSON {
				return jsonout.Write(c.OutOrStdout(), row)
			}
			printRow(c.OutOrStdout(), row)
			return nil
		},
	}
	o.bind(cmd)
	return cmd
}

func enableCmd() *cobra.Command  { return switchCmd(true) }
func disableCmd() *cobra.Command { return switchCmd(false) }

// switchCmd turns one service on or off through the call that owns its switch.
func switchCmd(on bool) *cobra.Command {
	var o options
	use, short := "enable <service>", "Turn a service on"
	if !on {
		use, short = "disable <service>", "Turn a service off"
	}
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			id, err := ParseServiceID(args[0])
			if err != nil {
				return err
			}
			return setEnabled(c.Context(), client(), resolveRefs(o.room, o.repo), id, on, c.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&o.room, "room", "", "room whose service to change (default: the current room)")
	cmd.Flags().StringVar(&o.repo, "repo", "", "repository path (default: the current one)")
	return cmd
}

func setEnabled(ctx context.Context, call engineCaller, r refs, id ServiceID, on bool, out io.Writer) error {
	r = resolve(ctx, call, r)
	switch id {
	case Reviewer:
		if r.Room == "" {
			return fmt.Errorf("no room; pass --room <room>")
		}
		if err := call.Call(ctx, "SetReviewEnabled", []any{r.Room, on}, nil); err != nil {
			return err
		}
		fmt.Fprintf(out, "PR reviewers are %s for room %s\n", stateWord(on), r.Room)
		return nil
	case Library:
		if r.Repo == "" {
			return fmt.Errorf("no repository; run this inside one or pass --repo <path>")
		}
		if !on {
			var disabled repoid.Repo
			if err := call.Call(ctx, "DisableLibrarian", []any{r.Repo}, &disabled); err != nil {
				return err
			}
			fmt.Fprintf(out, "the librarian is off for %s\n", r.Repo)
			return nil
		}
		if r.Room == "" {
			return fmt.Errorf("no room; pass --room <room>")
		}
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		var enabled librarian.Enabled
		if err := call.Call(ctx, "EnableLibrarian", []any{r.Repo, r.Room, cwd}, &enabled); err != nil {
			return err
		}
		fmt.Fprintf(out, "the librarian is on for %s in room %s\n", enabled.Repo.Main, enabled.Room)
		return nil
	case serviceregistry.IDMergeQueue:
		if r.Room == "" {
			return fmt.Errorf("no room; pass --room <room>")
		}
		if err := call.Call(ctx, "SetMergePaused", []any{r.Room, !on}, nil); err != nil {
			return err
		}
		fmt.Fprintf(out, "the merge queue is %s for room %s\n", stateWord(on), r.Room)
		return nil
	case serviceregistry.IDUpdates:
		if r.Repo == "" {
			return fmt.Errorf("no repository; run this inside one or pass --repo <path>")
		}
		off := !on
		if err := call.Call(ctx, "EditHook", []any{r.Repo, updateHook, hooks.Patch{Disabled: &off}}, nil); err != nil {
			return err
		}
		fmt.Fprintf(out, "updates are %s for %s\n", stateWord(on), r.Repo)
		return nil
	case serviceregistry.IDParentCheckIns, serviceregistry.IDHarnessVersions, serviceregistry.IDAccountUsage, serviceregistry.IDLoadDoctor, serviceregistry.IDMaintenance:
		off := !on
		if err := call.Call(ctx, "EditGlobalHook", []any{string(id), hooks.Patch{Disabled: &off}}, nil); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s is %s\n", id, stateWord(on))
		return nil
	}
	return fmt.Errorf("%s has no switch: it always runs", id)
}

// updateHook is the repository hook that installs a new Dyna build; the
// Updates service reads and switches it.
const updateHook = "self-update"

func stateWord(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

func profileCmd() *cobra.Command {
	var o options
	cmd := &cobra.Command{
		Use:   "profile <service> <profile|auto>",
		Short: "Pin a service to a profile; auto ranks the enabled service profiles",
		Long: "Pin one service to an enabled profile, or pass auto to rank the enabled service\n" +
			"profiles again. The owner may change any room, or every room with --all-rooms.\n" +
			"A room's root may change only its own room. A root may not change another room.\n" +
			"Change a service's profile here only: a bulk move of sessions never changes a service.",
		Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			id, err := ParseServiceID(args[0])
			if err != nil {
				return err
			}
			profile := strings.TrimSpace(args[1])
			if strings.EqualFold(profile, "auto") || profile == "-" {
				profile = ""
			}
			if o.allRooms {
				if o.room != "" {
					return fmt.Errorf("pass --room or --all-rooms, not both")
				}
				return setProfileAll(c.Context(), client(), id, profile, c.OutOrStdout())
			}
			return setProfile(c.Context(), client(), resolveRefs(o.room, o.repo), id, profile, c.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&o.room, "room", "", "room whose service to change (default: the current room)")
	cmd.Flags().StringVar(&o.repo, "repo", "", "repository path (default: the current one)")
	cmd.Flags().BoolVar(&o.allRooms, "all-rooms", false, "set this profile in every room (owner only)")
	return cmd
}

// setProfile writes the profile of one service through the call that owns it.
func setProfile(ctx context.Context, call engineCaller, r refs, id ServiceID, profile string, out io.Writer) error {
	r = resolve(ctx, call, r)
	if profile != "" {
		known, err := serviceProfiles(ctx, call, r)
		if err != nil {
			return err
		}
		found := false
		for _, name := range known {
			if name == profile {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("profile %q cannot run a service; choose one of: %s", profile, strings.Join(known, ", "))
		}
	}
	switch id {
	case Reviewer, Library:
		if r.Room == "" {
			return fmt.Errorf("no room; pass --room <room>")
		}
		if err := call.Call(ctx, "SetServiceProfile", []any{r.Room, string(id), profile}, nil); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%s runs no agent of its own, so it has no profile", id)
	}
	chosen := profile
	if chosen == "" {
		chosen = "auto"
	}
	fmt.Fprintf(out, "%s profile: %s\n", id, chosen)
	return nil
}

// setProfileAll writes one service profile in every room. The engine refuses
// a caller that is not the owner before it writes.
func setProfileAll(ctx context.Context, call engineCaller, id ServiceID, profile string, out io.Writer) error {
	if id != Reviewer && id != Library {
		return fmt.Errorf("%s runs no agent of its own, so it has no profile", id)
	}
	if err := call.Call(ctx, "SetServiceProfileAll", []any{string(id), profile}, nil); err != nil {
		return err
	}
	chosen := profile
	if chosen == "" {
		chosen = "auto"
	}
	fmt.Fprintf(out, "%s profile: %s for all rooms\n", id, chosen)
	return nil
}

// serviceProfiles lists the enabled profiles a service may run, as the
// Services page offers them.
func serviceProfiles(ctx context.Context, call engineCaller, r refs) ([]string, error) {
	r = resolve(ctx, call, r)
	if r.Room == "" {
		return nil, fmt.Errorf("no room; pass --room <room>")
	}
	var rows []serviceRow
	if err := call.Call(ctx, "ListServices", []any{r.Room}, &rows); err != nil {
		return nil, err
	}
	for _, row := range rows {
		if len(row.Profiles) > 0 {
			return row.Profiles, nil
		}
	}
	return nil, fmt.Errorf("no profile can run a service; run `dyna profiles set <profile> --use services`")
}

func restartCmd() *cobra.Command {
	var o options
	cmd := &cobra.Command{
		Use:   "restart <service>",
		Short: "Give a service a turn now: wake the librarian, restart the reviewers",
		Long: "Give the service a turn now. The librarian wakes on its repository; the reviewers\n" +
			"restart on the room's open changes. Voice runs per call and has nothing to restart.",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			id, err := ParseServiceID(args[0])
			if err != nil {
				return err
			}
			return restart(c.Context(), client(), resolveRefs(o.room, o.repo), id, c.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&o.room, "room", "", "room whose service to restart (default: the current room)")
	cmd.Flags().StringVar(&o.repo, "repo", "", "repository path (default: the current one)")
	return cmd
}

func restart(ctx context.Context, call engineCaller, r refs, id ServiceID, out io.Writer) error {
	r = resolve(ctx, call, r)
	switch id {
	case Reviewer:
		if r.Room == "" {
			return fmt.Errorf("no room; pass --room <room>")
		}
		if err := call.Call(ctx, "RestartReviewer", []any{r.Room}, nil); err != nil {
			return err
		}
		fmt.Fprintf(out, "restarted the reviewers of room %s\n", r.Room)
		return nil
	case Library:
		if r.Repo == "" {
			return fmt.Errorf("no repository; run this inside one or pass --repo <path>")
		}
		var result librarian.Result
		if err := call.Call(ctx, "WakeLibrarian", []any{r.Repo}, &result); err != nil {
			return err
		}
		fmt.Fprintf(out, "librarian %s %s\n", result.Action, result.Reason)
		return nil
	}
	return fmt.Errorf("%s has nothing to restart; only the reviewers and the librarian take a turn on request", id)
}

func logCmd() *cobra.Command {
	var o options
	var turn int
	cmd := &cobra.Command{
		Use:   "log <service>",
		Short: "Show the newest turn of a service session",
		Long: "Show the newest turn of the session a service runs in: the librarian's, or the\n" +
			"reviewer working on the room's current change. Voice keeps no session.",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			id, err := ParseServiceID(args[0])
			if err != nil {
				return err
			}
			return showLog(c.Context(), client(), resolveRefs(o.room, o.repo), id, turn, c.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&o.room, "room", "", "room whose service to read (default: the current room)")
	cmd.Flags().StringVar(&o.repo, "repo", "", "repository path (default: the current one)")
	cmd.Flags().IntVar(&turn, "turn", 0, "turn number (default: the newest)")
	return cmd
}

func showLog(ctx context.Context, call engineCaller, r refs, id ServiceID, turn int, out io.Writer) error {
	if id == Voice {
		fmt.Fprintln(out, "Voice keeps no session: each call ends with the call. Its posts are in `dyna room log <room>`.")
		return nil
	}
	row, err := findRow(ctx, call, r, id)
	if err != nil {
		return err
	}
	if row.Session == "" {
		return fmt.Errorf("%s has no session now; only services that run an agent have one", id)
	}
	var session struct {
		Turns []struct {
			N      int    `json:"n"`
			Status string `json:"status"`
			Items  []struct {
				Kind string `json:"kind"`
				Text string `json:"text"`
			} `json:"items"`
		} `json:"turns"`
	}
	if turn > 0 {
		err = call.Call(ctx, "TranscriptTurn", []any{row.Session, turn}, &session)
	} else {
		err = call.Call(ctx, "Transcript", []any{row.Session}, &session)
	}
	if err != nil {
		return err
	}
	if len(session.Turns) == 0 {
		return fmt.Errorf("%s has no recorded turn yet", row.Session)
	}
	last := session.Turns[len(session.Turns)-1]
	fmt.Fprintf(out, "%s turn %d: %s\n", row.Session, last.N, last.Status)
	for _, item := range last.Items {
		if item.Text == "" {
			continue
		}
		fmt.Fprintf(out, "\n%s\n%s\n", item.Kind, item.Text)
	}
	return nil
}

// readRegistry reads the service list the Services page reads.
func readRegistry(ctx context.Context, call engineCaller, r refs) ([]serviceregistry.Service, error) {
	r = resolve(ctx, call, r)
	if r.Room == "" {
		return nil, fmt.Errorf("no room; pass --room <room>")
	}
	var rows []serviceregistry.Service
	if err := call.Call(ctx, "ServiceRegistry", []any{r.Room, r.Repo}, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func findRow(ctx context.Context, call engineCaller, r refs, id ServiceID) (serviceregistry.Service, error) {
	rows, err := readRegistry(ctx, call, r)
	if err != nil {
		return serviceregistry.Service{}, err
	}
	for _, row := range rows {
		if row.ID == id {
			return row, nil
		}
	}
	return serviceregistry.Service{}, fmt.Errorf("unknown service %q", id)
}

var groupTitles = map[serviceregistry.Group]string{
	serviceregistry.GroupPullRequests: "Pull requests",
	serviceregistry.GroupAgents:       "Agents",
	serviceregistry.GroupThisComputer: "This computer",
}

// printRows prints the rows in the groups the Services page uses.
func printRows(out io.Writer, rows []serviceregistry.Service) {
	var group serviceregistry.Group
	for _, row := range rows {
		if row.Group != group {
			group = row.Group
			fmt.Fprintf(out, "\n%s\n", groupTitles[group])
			fmt.Fprintf(out, "  %-18s %-7s %-11s %-13s %-17s %-17s %s\n", "SERVICE", "SWITCH", "STATE", "PROFILE", "LAST RUN", "NEXT RUN", "NOTE")
		}
		fmt.Fprintf(out, "  %-18s %-7s %-11s %-13s %-17s %-17s %s\n", row.ID, switchText(row), row.State, dash(row.Profile), stamp(row.LastRun), stamp(row.NextRun), detailText(row))
	}
}

func printRow(out io.Writer, row serviceregistry.Service) {
	fmt.Fprintf(out, "service:     %s\nname:        %s\ngroup:       %s\nabout:       %s\nstate:       %s\n", row.ID, row.Label, groupTitles[row.Group], row.Description, row.State)
	for _, line := range []struct{ label, value string }{
		{"note", detailText(row)},
		{"switch", switchText(row)},
		{"profile", row.Profile},
		{"session", row.Session},
		{"last run", stamp(row.LastRun)},
		{"next run", stamp(row.NextRun)},
		{"settings", strings.Join(row.SettingsKeys, ", ")},
	} {
		if line.value != "" && line.value != "-" {
			fmt.Fprintf(out, "%-13s%s\n", line.label+":", line.value)
		}
	}
}

// switchText is the service's switch as the list shows it: on, off, or a dash
// for a service the owner cannot turn off.
func switchText(row serviceregistry.Service) string {
	switch {
	case row.Switch == nil:
		return "-"
	case row.Switch.On:
		return "on"
	}
	return "off"
}

// detailText words a service's typed detail.
func detailText(row serviceregistry.Service) string {
	switch row.Detail {
	case serviceregistry.DetailLastRunFailed:
		return "last run failed"
	case serviceregistry.DetailNoRunYet:
		return "has not run yet"
	case serviceregistry.DetailProfileWaiting:
		return "waiting for its profile"
	case serviceregistry.DetailWorking:
		if row.Count > 0 {
			return fmt.Sprintf("working, %d waiting", row.Count)
		}
		return "working"
	case serviceregistry.DetailQueued:
		return fmt.Sprintf("%d waiting", row.Count)
	case serviceregistry.DetailPaused:
		return "paused"
	case serviceregistry.DetailInCall:
		return "in a call"
	case serviceregistry.DetailUnavailable:
		return "not available on this computer"
	case serviceregistry.DetailUnreadable:
		return "could not be read"
	}
	return ""
}

// stamp is a stored time as the local clock reads it; a missing time is a dash.
func stamp(at *time.Time) string {
	if at == nil || at.IsZero() {
		return "-"
	}
	return at.Local().Format("2006-01-02 15:04")
}

func dash(text string) string {
	if text == "" {
		return "-"
	}
	return text
}
