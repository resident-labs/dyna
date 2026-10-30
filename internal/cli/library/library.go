// Package library implements `dyna library`, the only way to read and change
// a repository's feature map.
package library

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/librarian"
	"dyna-agent/internal/library"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/room"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/store"
	"github.com/spf13/cobra"
)

// Options lets tests use an isolated data folder and settings.
type Options struct {
	DataDir  string
	Settings func() (settings.Values, error)
}

func NewCommand() *cobra.Command { return Command(Options{}) }

func Command(o Options) *cobra.Command {
	var repoDir string
	var asJSON bool
	root := &cobra.Command{Use: "library", Short: "Read and change the repository feature map", Args: cobra.NoArgs}
	root.PersistentFlags().StringVar(&repoDir, "repo", ".", "repository path")
	root.PersistentFlags().BoolVar(&asJSON, "json", false, "print JSON")
	open := func(c *cobra.Command) (library.Store, repoid.Repo, error) {
		dir := o.DataDir
		if dir == "" {
			dir = store.DataDir()
		}
		r, err := repoid.Resolve(c.Context(), repoDir)
		return library.Store{DataDir: dir}, r, err
	}
	loadSettings := func() (settings.Values, error) {
		if o.Settings != nil {
			return o.Settings()
		}
		return settings.Load(settings.DefaultPath())
	}
	printEntry := func(c *cobra.Command, s library.Store, r repoid.Repo, e library.Entry) error {
		if asJSON {
			return json.NewEncoder(c.OutOrStdout()).Encode(e)
		}
		rows, err := s.List(r.ID, false)
		if err != nil {
			return err
		}
		// Clones of one remote share the map, so a pin can name a commit this
		// clone lacks. The entry still prints, with the pin's state unknown.
		pin := "unknown"
		if stale, err := library.Stale(c.Context(), r.Root, []library.Entry{e}); err == nil {
			pin = map[bool]string{true: "stale", false: "current"}[stale[e.ID]]
		}
		writeEntry(c.OutOrStdout(), e, rows, pin)
		return nil
	}

	var tree, staleOnly, all bool
	list := &cobra.Command{Use: "list", Short: "List feature entries", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		s, r, err := open(c)
		if err != nil {
			return err
		}
		rows, err := s.List(r.ID, all)
		if err != nil {
			return err
		}
		if staleOnly {
			stale, err := library.Stale(c.Context(), r.Root, rows)
			if err != nil {
				return err
			}
			kept := rows[:0]
			for _, e := range rows {
				if stale[e.ID] {
					kept = append(kept, e)
				}
			}
			rows = kept
		}
		if asJSON {
			if rows == nil {
				rows = []library.Entry{}
			}
			return json.NewEncoder(c.OutOrStdout()).Encode(rows)
		}
		if tree {
			writeTree(c.OutOrStdout(), rows)
			return nil
		}
		for _, e := range rows {
			fmt.Fprintf(c.OutOrStdout(), "%s  %s  %s\n", e.ID, e.Kind, e.Summary)
		}
		return nil
	}}
	list.Flags().BoolVar(&tree, "tree", false, "indent children under their parent")
	list.Flags().BoolVar(&staleOnly, "stale", false, "only entries whose owned files changed since they were verified")
	list.Flags().BoolVar(&all, "all", false, "include retired entries")

	show := &cobra.Command{Use: "show <id>", Short: "Show one entry", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		s, r, err := open(c)
		if err != nil {
			return err
		}
		e, err := s.Get(r.ID, a[0])
		if err != nil {
			return err
		}
		return printEntry(c, s, r, e)
	}}

	var f fields
	add := &cobra.Command{Use: "add <id>", Short: "Add an entry", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		s, r, err := open(c)
		if err != nil {
			return err
		}
		e := library.Entry{ID: a[0], Kind: "feature"}
		if err := f.apply(c, r.Root, &e); err != nil {
			return err
		}
		err = engineclient.New("").Call(c.Context(), "AddLibraryEntry", []any{r.ID, e}, &e)
		if err != nil {
			return err
		}
		return printEntry(c, s, r, e)
	}}
	f.bind(add)
	add.MarkFlagRequired("name")
	add.MarkFlagRequired("summary")
	add.MarkFlagRequired("owns")

	var u fields
	update := &cobra.Command{Use: "update <id>", Short: "Change the supplied fields of an entry", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		s, r, err := open(c)
		if err != nil {
			return err
		}
		patch, err := u.patch(c, r.Root)
		if err != nil {
			return err
		}
		var e library.Entry
		err = engineclient.New("").Call(c.Context(), "UpdateLibraryEntry", []any{r.ID, a[0], patch}, &e)
		if err != nil {
			return err
		}
		return printEntry(c, s, r, e)
	}}
	u.bind(update)

	var addLinks, removeLinks library.Links
	link := &cobra.Command{Use: "link <id>", Short: "Add or remove docs and tests links on an entry", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		if len(addLinks.Docs)+len(addLinks.Tests)+len(removeLinks.Docs)+len(removeLinks.Tests) == 0 {
			return fmt.Errorf("supply --docs, --tests, --remove-docs or --remove-tests")
		}
		s, r, err := open(c)
		if err != nil {
			return err
		}
		var e library.Entry
		err = engineclient.New("").Call(c.Context(), "LinkLibraryEntry", []any{r.ID, a[0], addLinks, removeLinks}, &e)
		if err != nil {
			return err
		}
		return printEntry(c, s, r, e)
	}}
	link.Flags().StringSliceVar(&addLinks.Docs, "docs", nil, "documentation path to link")
	link.Flags().StringSliceVar(&addLinks.Tests, "tests", nil, "test file glob to link")
	link.Flags().StringSliceVar(&removeLinks.Docs, "remove-docs", nil, "documentation path to unlink")
	link.Flags().StringSliceVar(&removeLinks.Tests, "remove-tests", nil, "test file glob to unlink")

	var successor string
	retire := &cobra.Command{Use: "retire <id>", Short: "Retire an entry; children move to the successor or the parent", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		s, r, err := open(c)
		if err != nil {
			return err
		}
		var e library.Entry
		err = engineclient.New("").Call(c.Context(), "RetireLibraryEntry", []any{r.ID, a[0], successor}, &e)
		if err != nil {
			return err
		}
		return printEntry(c, s, r, e)
	}}
	retire.Flags().StringVar(&successor, "successor", "", "entry that replaces this one")

	var since string
	changed := &cobra.Command{Use: "changed", Short: "List changed files with their owners; unmapped files have none", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		s, r, err := open(c)
		if err != nil {
			return err
		}
		rows, err := s.List(r.ID, false)
		if err != nil {
			return err
		}
		changes, err := library.Changed(c.Context(), r.Root, since, "HEAD", rows)
		if err != nil {
			return err
		}
		if asJSON {
			if changes == nil {
				changes = []library.Change{}
			}
			return json.NewEncoder(c.OutOrStdout()).Encode(changes)
		}
		for _, ch := range changes {
			owners := strings.Join(ch.Owners, ",")
			if owners == "" {
				owners = "(unmapped)"
			}
			fmt.Fprintf(c.OutOrStdout(), "%s  %s\n", ch.File, owners)
		}
		return nil
	}}
	changed.Flags().StringVar(&since, "since", "HEAD~1", "base commit")

	lint := &cobra.Command{Use: "lint", Short: "Check the map against the repository; reports warnings only", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		s, r, err := open(c)
		if err != nil {
			return err
		}
		rows, err := s.List(r.ID, false)
		if err != nil {
			return err
		}
		v, err := loadSettings()
		if err != nil {
			return err
		}
		findings, err := library.Lint(c.Context(), r.Root, rows, v.LibraryReadmeMaxLines)
		if err != nil {
			return err
		}
		if asJSON {
			if findings == nil {
				findings = []library.Finding{}
			}
			return json.NewEncoder(c.OutOrStdout()).Encode(findings)
		}
		for _, f := range findings {
			where := strings.TrimSpace(f.Entry + " " + f.File)
			fmt.Fprintf(c.OutOrStdout(), "rule %d  %s  %s\n", f.Rule, where, f.Message)
		}
		return nil
	}}

	var roomRef string
	enable := &cobra.Command{Use: "enable", Short: "Turn the librarian on for this repository (owner or root)", Long: "List the repository in library.enabled, keep its librarian in a room, and add the librarian-main hook. The hook runs dyna library wake on each main update. The engine defers a wake that must wait for the librarian's interval.", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		r, err := repoid.Resolve(c.Context(), repoDir)
		if err != nil {
			return err
		}
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		var enabled librarian.Enabled
		if err := engineclient.New("").Call(c.Context(), "EnableLibrarian", []any{r.Root, roomRef, cwd}, &enabled); err != nil {
			return err
		}
		fmt.Fprintf(c.OutOrStdout(), "librarian on for %s in room %s; hook librarian-main runs dyna library wake\n", enabled.Repo.Main, enabled.Room)
		return nil
	}}
	enable.Flags().StringVar(&roomRef, "room", "", "room that holds the librarian session (default: the current room)")
	disable := &cobra.Command{Use: "disable", Short: "Turn the librarian off; the map stays (owner or root)", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		r, err := repoid.Resolve(c.Context(), repoDir)
		if err != nil {
			return err
		}
		err = engineclient.New("").Call(c.Context(), "DisableLibrarian", []any{r.Root}, &r)
		if err != nil {
			return err
		}
		fmt.Fprintf(c.OutOrStdout(), "librarian off for %s\n", r.Main)
		return nil
	}}
	var wakeSmells, wakeNow bool
	var wakeRoom string
	wake := &cobra.Command{Use: "wake", Short: "Give the librarian a turn if the gate passes; the librarian hooks run this", Long: "Give the librarian a turn if the gate passes. With --smells --now, start the code-reading smell pass at once and skip its 6-hour wait. The busy check, the lock and the daily smell cap still apply, and only the owner or a room's root may use --now. --room picks the repository of a room.", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		if wakeNow && !wakeSmells {
			return errors.New("--now needs --smells")
		}
		if wakeSmells && !wakeNow {
			return errors.New("--smells needs --now; the hooks already run the smell pass on its schedule")
		}
		if wakeRoom != "" && !wakeNow {
			return errors.New("--room needs --smells --now")
		}
		var repoRoot string
		if wakeRoom == "" {
			r, err := repoid.Resolve(c.Context(), repoDir)
			if err != nil {
				return err
			}
			repoRoot = r.Root
		}
		var res librarian.Result
		var err error
		if wakeNow {
			err = engineclient.New("").Call(c.Context(), "WakeLibrarianSmellsNow", []any{repoRoot, wakeRoom}, &res)
		} else {
			err = engineclient.New("").Call(c.Context(), "WakeLibrarian", []any{repoRoot}, &res)
		}
		if err != nil {
			return err
		}
		if asJSON {
			return json.NewEncoder(c.OutOrStdout()).Encode(res)
		}
		fmt.Fprintf(c.OutOrStdout(), "librarian %s %s\n", res.Action, res.Reason)
		return nil
	}}

	wake.Flags().BoolVar(&wakeSmells, "smells", false, "run the code-reading smell pass (needs --now)")
	wake.Flags().BoolVar(&wakeNow, "now", false, "skip the smell interval; owner or root only (needs --smells)")
	wake.Flags().StringVar(&wakeRoom, "room", "", "use the repository of this room instead of --repo")

	var sm librarian.Smell
	smell := &cobra.Command{Use: "smell <title>", Short: "File an architectural smell on the room issue board", Long: "File a smell as a room issue. It needs file references that exist, the symbols involved, the maintenance cost and a direction. Dyna refuses a smell that matches an open issue or passes the daily cap.", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		r, err := repoid.Resolve(c.Context(), repoDir)
		if err != nil {
			return err
		}
		sm.Title = a[0]
		var i room.Issue
		err = engineclient.New("").Call(c.Context(), "FileLibrarySmell", []any{r.Root, sm}, &i)
		if err != nil {
			return err
		}
		if asJSON {
			return json.NewEncoder(c.OutOrStdout()).Encode(i)
		}
		fmt.Fprintf(c.OutOrStdout(), "filed %s : %s\n", i.Label(), i.Title)
		return nil
	}}
	smell.Flags().StringSliceVar(&sm.At, "at", nil, "file or file:line; repeat for each file")
	smell.Flags().StringSliceVar(&sm.Symbols, "symbols", nil, "functions, types or packages involved")
	smell.Flags().StringVar(&sm.Cost, "cost", "", "what the problem costs in maintenance")
	smell.Flags().StringVar(&sm.Direction, "direction", "", "a direction for the fix, not a patch")

	checkReadme := &cobra.Command{Use: "check-readme", Short: "Fail when README.md is longer than library.readmeMaxLines", Long: "Count README.md lines in the repository checkout and fail when there are more than library.readmeMaxLines. The librarian runs this in its docs worktree and does not open a pull request while it fails.", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		r, err := repoid.Resolve(c.Context(), repoDir)
		if err != nil {
			return err
		}
		v, err := loadSettings()
		if err != nil {
			return err
		}
		f, over, err := library.Readme(r.Root, v.LibraryReadmeMaxLines)
		if err != nil {
			return err
		}
		if over {
			return fmt.Errorf("%s; shorten it before you open the pull request", f.Message)
		}
		fmt.Fprintln(c.OutOrStdout(), "README.md is within library.readmeMaxLines")
		return nil
	}}

	root.AddCommand(list, show, add, update, link, retire, changed, lint, enable, disable, wake, smell, checkReadme)
	return root
}

// fields holds the entry flags shared by add and update. Only flags the
// caller supplied change the entry.
type fields struct {
	name, kind, parent, summary, differs, verified string
	owns, relates                                  []string
}

func (f *fields) bind(c *cobra.Command) {
	c.Flags().StringVar(&f.name, "name", "", "what a reader calls the feature")
	c.Flags().StringVar(&f.kind, "kind", "", "feature, adapter, integration or service")
	c.Flags().StringVar(&f.parent, "parent", "", "parent entry id; empty for a top-level entry")
	c.Flags().StringVar(&f.summary, "summary", "", "at most two short sentences")
	c.Flags().StringVar(&f.differs, "differs", "", "how it differs from siblings of the same kind")
	c.Flags().StringSliceVar(&f.owns, "owns", nil, "owned file globs; replaces the list")
	c.Flags().StringSliceVar(&f.relates, "relates", nil, "entries with intentional overlap; replaces the list")
	c.Flags().StringVar(&f.verified, "verified", "", "commit the summary was checked against, such as HEAD")
}

func (f fields) apply(c *cobra.Command, dir string, e *library.Entry) error {
	patch, err := f.patch(c, dir)
	if err != nil {
		return err
	}
	return patch.Apply(e)
}

func (f fields) patch(c *cobra.Command, dir string) (library.Patch, error) {
	var p library.Patch
	for _, field := range []struct {
		flag  string
		value *string
		to    **string
	}{
		{"name", &f.name, &p.Name}, {"kind", &f.kind, &p.Kind},
		{"parent", &f.parent, &p.Parent}, {"summary", &f.summary, &p.Summary},
		{"differs", &f.differs, &p.Differs},
	} {
		if c.Flags().Changed(field.flag) {
			*field.to = field.value
		}
	}
	if c.Flags().Changed("owns") {
		p.Owns = &f.owns
	}
	if c.Flags().Changed("relates") {
		p.Relates = &f.relates
	}
	if c.Flags().Changed("verified") {
		sha, err := repoid.Git(c.Context(), dir, "rev-parse", "--verify", f.verified+"^{commit}")
		if err != nil {
			return p, err
		}
		p.VerifiedAt = &sha
	}
	return p, nil
}

// writeEntry prints an entry for a reader: what it is, how it differs, where
// it sits in the map, and the files, docs and tests to open. rows are the
// live entries, for the parent's summary and the children. pin is current,
// stale or unknown.
func writeEntry(w io.Writer, e library.Entry, rows []library.Entry, pin string) {
	byID := map[string]library.Entry{}
	var children []string
	for _, row := range rows {
		byID[row.ID] = row
		if row.Parent == e.ID {
			children = append(children, row.ID)
		}
	}
	fmt.Fprintf(w, "%s · %s (%s)\n%s\n", e.ID, e.Name, e.Kind, e.Summary)
	if e.Differs != "" {
		fmt.Fprintf(w, "Differs: %s\n", e.Differs)
	}
	if e.Retired {
		if e.Successor != "" {
			fmt.Fprintf(w, "Retired; replaced by %s.\n", e.Successor)
		} else {
			fmt.Fprintln(w, "Retired.")
		}
	}
	fmt.Fprintln(w)
	if e.Parent != "" {
		fmt.Fprintf(w, "Parent:   %s — %s\n", e.Parent, byID[e.Parent].Summary)
	}
	if len(children) > 0 {
		fmt.Fprintf(w, "Children: %s\n", strings.Join(children, ", "))
	}
	if len(e.Relates) > 0 {
		fmt.Fprintf(w, "Related:  %s\n", strings.Join(e.Relates, ", "))
	}
	for _, group := range []struct {
		title string
		paths []string
	}{{"Files", e.Owns}, {"Docs", e.Docs}, {"Tests", e.Tests}} {
		if len(group.paths) == 0 {
			continue
		}
		fmt.Fprintf(w, "%s:\n", group.title)
		for _, p := range group.paths {
			fmt.Fprintf(w, "  %s\n", p)
		}
	}
	switch {
	case e.VerifiedAt == "":
		fmt.Fprintln(w, "Not verified against a commit yet.")
	case pin == "stale":
		fmt.Fprintf(w, "Verified at %.12s; owned files changed since then.\n", e.VerifiedAt)
	case pin == "unknown":
		fmt.Fprintf(w, "Verified at %.12s; this checkout cannot check that commit.\n", e.VerifiedAt)
	default:
		fmt.Fprintf(w, "Verified at %.12s.\n", e.VerifiedAt)
	}
}

// writeTree prints roots first and children indented under their parent.
// An entry whose parent is not in rows prints as a root.
func writeTree(w io.Writer, rows []library.Entry) {
	ids := map[string]bool{}
	children := map[string][]library.Entry{}
	for _, e := range rows {
		ids[e.ID] = true
	}
	for _, e := range rows {
		p := e.Parent
		if !ids[p] {
			p = ""
		}
		children[p] = append(children[p], e)
	}
	var walk func(string, int)
	walk = func(parent string, depth int) {
		for _, e := range children[parent] {
			fmt.Fprintf(w, "%s%s  %s\n", strings.Repeat("  ", depth), e.ID, e.Summary)
			walk(e.ID, depth+1)
		}
	}
	walk("", 0)
}
