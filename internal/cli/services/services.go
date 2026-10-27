// Package services reads and changes Dyna's services: the librarian, the PR
// reviewers and the voice session. A service is not a session in the room's
// hierarchy, so it never appears in `dyna room members` or `@children`; this
// command is the way to list, enable, profile, restart and read one.
//
// Every value here comes from an engine call that the Services page also
// makes, so the two front ends cannot disagree about what a service is.
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"dyna-agent/internal/cli/jsonout"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/librarian"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

// ServiceID names one Dyna service. The set is closed, so a mistyped service
// is an error and never a new one.
type ServiceID string

// The services this command manages.
const (
	Library  ServiceID = "library"
	Reviewer ServiceID = "reviewer"
	Voice    ServiceID = "voice"
)

// serviceIDs is every service, in the order `list` shows them.
var serviceIDs = []ServiceID{Library, Reviewer, Voice}

// aliases accept the words people and prompts use for a service.
var aliases = map[string]ServiceID{"librarian": Library, "reviewers": Reviewer, "review": Reviewer, "voice-call": Voice}

// ParseServiceID accepts a service id or a common alias.
func ParseServiceID(text string) (ServiceID, error) {
	id := ServiceID(strings.ToLower(strings.TrimSpace(text)))
	if id == "" {
		return "", fmt.Errorf("name a service: %s", strings.Join(serviceIDNames(), ", "))
	}
	if alias, ok := aliases[string(id)]; ok {
		return alias, nil
	}
	for _, known := range serviceIDs {
		if id == known {
			return id, nil
		}
	}
	return "", fmt.Errorf("unknown service %q; services are %s", text, strings.Join(serviceIDNames(), ", "))
}

func serviceIDNames() []string {
	names := make([]string, len(serviceIDs))
	for i, id := range serviceIDs {
		names[i] = string(id)
	}
	return names
}

// Row is one service as `dyna services list` and `show` show it. Switch is the
// command that turns the service on or off, and is empty when it has none.
type Row struct {
	ID       ServiceID `json:"id"`
	Name     string    `json:"name"`
	Enabled  bool      `json:"enabled"`
	Switch   string    `json:"switch,omitempty"`
	State    string    `json:"state"`
	Detail   string    `json:"detail,omitempty"`
	Profile  string    `json:"profile"`
	Pinned   string    `json:"pinned,omitempty"`
	Source   string    `json:"source,omitempty"`
	LastRun  string    `json:"lastRun,omitempty"`
	NextRun  string    `json:"nextRun,omitempty"`
	Session  string    `json:"session,omitempty"`
	Profiles []string  `json:"profiles,omitempty"`
	Error    string    `json:"error,omitempty"`
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

type reviewerSlot struct {
	Session *struct {
		Address string `json:"address"`
		Status  string `json:"status"`
	} `json:"session"`
	Current *struct {
		PR    int    `json:"pr"`
		Title string `json:"title"`
	} `json:"current"`
}

type reviewerStatus struct {
	Enabled bool `json:"enabled"`
	// Profile is the one the next review uses; it backs the profile column
	// when the service list cannot be read.
	Profile   string         `json:"profile"`
	LastError string         `json:"lastError"`
	Reviewers []reviewerSlot `json:"reviewers"`
	Queue     []struct {
		PR int `json:"pr"`
	} `json:"queue"`
	Recent []struct {
		PR      int       `json:"pr"`
		Verdict string    `json:"verdict"`
		At      time.Time `json:"at"`
	} `json:"recent"`
}

type settingRow struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
	// Display is the value as the app shows it, used when Value has no text.
	Display string `json:"display"`
}

type hookRow struct {
	Name         string `json:"name"`
	Event        string `json:"event"`
	EveryMinutes int    `json:"everyMinutes"`
	Disabled     bool   `json:"disabled"`
}

type hookRunRow struct {
	Result    string    `json:"result"`
	StartedAt time.Time `json:"startedAt"`
}

type hookSummaryRow struct {
	Hook    hookRow     `json:"hook"`
	LastRun *hookRunRow `json:"lastRun"`
}

type voiceStatus struct {
	Available   bool   `json:"available"`
	Unavailable string `json:"unavailable"`
	Live        bool   `json:"live"`
	Room        string `json:"room"`
	Error       string `json:"error"`
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

// facts is everything the reads said. A read that failed keeps its message so
// the row can say why instead of showing a wrong value.
type facts struct {
	refs     refs
	services []serviceRow
	reviewer reviewerStatus
	settings []settingRow
	hooks    []hookSummaryRow
	voice    voiceStatus
	errs     map[string]string
}

func (f facts) err(method string) string {
	if f.errs == nil {
		return ""
	}
	return f.errs[method]
}

// readFacts asks the engine for the values a row shows. It returns no error:
// a service the engine could not describe keeps its row, with the reason.
func readFacts(ctx context.Context, call engineCaller, r refs) facts {
	r = resolve(ctx, call, r)
	out := facts{refs: r, errs: map[string]string{}}
	if out.refs.Room != "" {
		// A service list that cannot be read leaves the profile columns empty;
		// the call that owns each profile reports its own error.
		_ = call.Call(ctx, "ListServices", []any{out.refs.Room}, &out.services)
		if err := call.Call(ctx, "ReviewerStatus", []any{out.refs.Room}, &out.reviewer); err != nil {
			out.errs["ReviewerStatus"] = err.Error()
		}
	} else {
		out.errs["room"] = "no room; pass --room <room>"
	}
	if err := call.Call(ctx, "Settings", nil, &out.settings); err != nil {
		out.errs["Settings"] = err.Error()
	}
	if out.refs.Repo != "" {
		if err := call.Call(ctx, "HookSummary", []any{out.refs.Repo}, &out.hooks); err != nil {
			out.errs["HookSummary"] = err.Error()
		}
	}
	if err := call.Call(ctx, "VoiceStatus", nil, &out.voice); err != nil {
		out.errs["VoiceStatus"] = err.Error()
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

// textSetting reads a settings value that is one string.
func (f facts) textSetting(key string) (string, bool) {
	for _, item := range f.settings {
		if item.Key != key {
			continue
		}
		var text string
		if err := json.Unmarshal(item.Value, &text); err == nil {
			return text, true
		}
		return item.Display, true
	}
	return "", false
}

// listSetting reads a settings value that is a JSON list of strings.
func (f facts) listSetting(key string) ([]string, bool) {
	for _, item := range f.settings {
		if item.Key != key {
			continue
		}
		var list []string
		if err := json.Unmarshal(item.Value, &list); err != nil {
			return nil, false
		}
		return list, true
	}
	return nil, false
}

// service finds one ListServices row.
func (f facts) service(id ServiceID) (serviceRow, bool) {
	role := string(id)
	if id == Library {
		role = subagent.RoleLibrarian
	}
	for _, row := range f.services {
		if row.Role == role {
			return row, true
		}
	}
	return serviceRow{}, false
}

// Rows builds one row per service from the engine's reads.
func Rows(f facts) []Row {
	return []Row{libraryRow(f), reviewerRow(f), voiceRow(f)}
}

func libraryRow(f facts) Row {
	row := Row{ID: Library, Name: "Librarian", Switch: "dyna services enable|disable " + string(Library)}
	if listed, ok := f.service(Library); ok {
		row.Pinned, row.Source, row.Profiles = listed.Pinned, listed.Source, listed.Profiles
	}
	repos, known := f.listSetting("library.enabled")
	if profile, ok := f.textSetting("library.profile"); ok {
		row.Profile, row.Pinned = profile, profile
		row.Source = "pinned"
		if profile == "" {
			row.Source = "auto"
		}
	}
	if !known {
		row.Error = f.errOr("Settings", "librarian settings could not be read")
		row.State = "unknown"
		return row
	}
	if f.refs.Repo == "" {
		row.Error = "no repository; run this inside one or pass --repo <path>"
		row.State = "unknown"
		return row
	}
	for _, path := range repos {
		if filepath.Clean(path) == filepath.Clean(f.refs.Repo) {
			row.Enabled = true
		}
	}
	if f.refs.Room != "" {
		row.Session = f.refs.Room + ":" + subagent.LibrarianName
	}
	batch := f.hookNamed("librarian-batch")
	switch {
	case !row.Enabled:
		row.State, row.Detail = "off", "Off for "+f.refs.Repo
	case batch.LastRun != nil && batch.LastRun.Result == "pending":
		row.State, row.Detail = "running", "Keeps the feature map current"
	default:
		row.State, row.Detail = "idle", "Keeps the feature map current"
	}
	if batch.LastRun != nil {
		row.LastRun = stamp(batch.LastRun.StartedAt)
		if row.Enabled {
			row.NextRun = nextScheduled(batch)
		}
	}
	if listed, ok := f.service(Library); ok && strings.HasPrefix(listed.Reason, "waiting:") {
		row.State, row.Detail = "waiting", listed.Reason
	}
	return row
}

func reviewerRow(f facts) Row {
	row := Row{ID: Reviewer, Name: "PR reviewers", Switch: "dyna services enable|disable " + string(Reviewer)}
	if listed, ok := f.service(Reviewer); ok {
		row.Profile, row.Pinned, row.Source, row.Profiles, row.Error = listed.Profile, listed.Pinned, listed.Source, listed.Profiles, listed.Reason
	}
	st := f.reviewer
	if row.Profile == "" && st.Profile != "" {
		row.Profile, row.Source = st.Profile, "pinned"
	}
	switch {
	case f.errs["room"] != "":
		row.State, row.Error = "unknown", f.errs["room"]
	case f.errs["ReviewerStatus"] != "":
		row.State, row.Error = "unknown", f.errs["ReviewerStatus"]
	case !st.Enabled:
		row.State, row.Detail = "off", "Reviews are off for this room"
	case st.LastError != "":
		row.State, row.Detail = "error", st.LastError
	case len(st.Reviewers) > 0:
		row.State = "running"
		if current := st.Reviewers[0].Current; current != nil {
			row.Detail = fmt.Sprintf("reviewing #%d · %s", current.PR, current.Title)
		}
	default:
		row.State, row.Detail = "waiting", "nothing to review now"
	}
	row.Enabled = st.Enabled && f.errs["ReviewerStatus"] == ""
	for _, slot := range st.Reviewers {
		if slot.Session != nil && slot.Session.Address != "" {
			row.Session = slot.Session.Address
			break
		}
	}
	if len(st.Recent) > 0 {
		row.LastRun = stamp(st.Recent[len(st.Recent)-1].At)
	}
	if len(st.Queue) > 0 {
		row.NextRun = fmt.Sprintf("%d waiting for review", len(st.Queue))
	}
	if listed, ok := f.service(Reviewer); ok && strings.HasPrefix(listed.Reason, "waiting:") {
		row.State, row.Detail = "waiting", listed.Reason
		if listed.Pinned != "" {
			row.Profile, row.Pinned, row.Source = listed.Pinned, listed.Pinned, "pinned"
		}
	}
	return row
}

func voiceRow(f facts) Row {
	row := Row{ID: Voice, Name: "Voice"}
	v := f.voice
	switch {
	case f.errs["VoiceStatus"] != "":
		row.State, row.Error = "unknown", f.errs["VoiceStatus"]
	case !v.Available:
		row.State, row.Detail = "off", strings.TrimSpace(v.Unavailable)
	default:
		row.Enabled = true
		row.State, row.Detail = "idle", "Started and stopped per call"
		if v.Live {
			row.State, row.Detail = "in a call", "In a call in "+v.Room
			row.Session = v.Room + ":" + room.VoiceAgent
		}
		if v.Error != "" {
			row.Detail = v.Error
		}
	}
	return row
}

// errOr returns the recorded read error, or a fallback sentence.
func (f facts) errOr(method, fallback string) string {
	if err := f.err(method); err != "" {
		return err
	}
	return fallback
}

// hookNamed finds one repository hook summary. A hook the repository does not
// have reports as an empty summary.
func (f facts) hookNamed(name string) hookSummaryRow {
	for _, row := range f.hooks {
		if row.Hook.Name == name {
			return row
		}
	}
	return hookSummaryRow{}
}

// nextScheduled is when a scheduled hook runs next: its last start plus its
// interval. A hook with no schedule or no last run has no next time.
func nextScheduled(row hookSummaryRow) string {
	if row.Hook.Event != "schedule" || row.Hook.EveryMinutes <= 0 || row.Hook.Disabled || row.LastRun == nil {
		return ""
	}
	return stamp(row.LastRun.StartedAt.Add(time.Duration(row.Hook.EveryMinutes) * time.Minute))
}

// stamp is a stored time as the local clock reads it. A zero time is empty.
func stamp(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.Local().Format("2006-01-02 15:04")
}

// NewCommand is `dyna services`: the services list, and the commands that
// change or read one service.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "services",
		Short: "List and manage Dyna's services: the librarian, the PR reviewers and Voice",
		Long: "A service is not a session in the room's hierarchy: the librarian, the PR reviewers\n" +
			"and Voice never appear in `dyna room members`, `@children` or a parent check-in.\n" +
			"Read or change one here or on the app's Services page.\n\n" +
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
	room   string
	repo   string
	asJSON bool
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
			rows := Rows(readFacts(c.Context(), client(), resolveRefs(o.room, o.repo)))
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
			row, err := findRow(readFacts(c.Context(), client(), resolveRefs(o.room, o.repo)), id)
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
	}
	return fmt.Errorf("voice has no switch: it runs one call at a time, so `dyna services enable voice` changes nothing")
}

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
			"profiles again. Change a service's profile here only: a bulk move of sessions never\n" +
			"changes a service, so a service keeps the profile you chose for it.",
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
			return setProfile(c.Context(), client(), resolveRefs(o.room, o.repo), id, profile, c.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&o.room, "room", "", "room whose service to change (default: the current room)")
	cmd.Flags().StringVar(&o.repo, "repo", "", "repository path (default: the current one)")
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
	case Reviewer:
		if r.Room == "" {
			return fmt.Errorf("no room; pass --room <room>")
		}
		if err := call.Call(ctx, "SetServiceProfile", []any{r.Room, string(Reviewer), profile}, nil); err != nil {
			return err
		}
	case Library:
		if err := call.Call(ctx, "SetSetting", []any{"library.profile", profile}, nil); err != nil {
			return err
		}
	case Voice:
		return fmt.Errorf("voice has no profile of its own: its calls use the voice provider settings")
	}
	chosen := profile
	if chosen == "" {
		chosen = "auto"
	}
	fmt.Fprintf(out, "%s profile: %s\n", id, chosen)
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
	return fmt.Errorf("voice has nothing to restart: it starts and stops with each call")
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
	row, err := findRow(readFacts(ctx, call, r), id)
	if err != nil {
		return err
	}
	if row.Session == "" {
		return fmt.Errorf("%s has no session now: %s", id, firstNonEmpty(row.Detail, "it has not started"))
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// findRow builds the rows once and returns the requested one.
func findRow(f facts, id ServiceID) (Row, error) {
	for _, row := range Rows(f) {
		if row.ID == id {
			return row, nil
		}
	}
	return Row{}, fmt.Errorf("unknown service %q", id)
}

func printRows(out io.Writer, rows []Row) {
	fmt.Fprintf(out, "%-9s %-4s %-12s %-12s %-16s %-16s %s\n", "SERVICE", "ON", "PROFILE", "STATE", "LAST RUN", "NEXT RUN", "SESSION")
	for _, row := range rows {
		fmt.Fprintf(out, "%-9s %-4s %-12s %-12s %-16s %-16s %s\n", row.ID, yesNo(row.Enabled), profileText(row), row.State, dash(row.LastRun), dash(row.NextRun), dash(row.Session))
	}
}

// profileText is the profile column: the pinned profile, auto for a service
// that ranks one each turn, and a dash for a service with none.
func profileText(row Row) string {
	if row.Profile != "" {
		return row.Profile
	}
	if row.ID == Voice {
		return "-"
	}
	return "auto"
}

func printRow(out io.Writer, row Row) {
	fmt.Fprintf(out, "service: %s\nname:    %s\nenabled: %s\nstate:   %s\nprofile: %s", row.ID, row.Name, yesNo(row.Enabled), row.State, profileText(row))
	if row.Source != "" {
		fmt.Fprintf(out, " (%s)", row.Source)
	}
	fmt.Fprintln(out)
	for _, line := range []struct{ label, value string }{
		{"switch", row.Switch},
		{"detail", row.Detail},
		{"last", row.LastRun},
		{"next", row.NextRun},
		{"session", row.Session},
		{"error", row.Error},
	} {
		if line.value != "" {
			fmt.Fprintf(out, "%-9s%s\n", line.label+":", line.value)
		}
	}
	if len(row.Profiles) > 0 {
		fmt.Fprintf(out, "profiles: %s\n", strings.Join(row.Profiles, ", "))
	}
}

func yesNo(on bool) string {
	if on {
		return "yes"
	}
	return "no"
}

func dash(text string) string {
	if text == "" {
		return "-"
	}
	return text
}
