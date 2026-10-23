package lanesync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Progress struct {
	Kind      string    `json:"kind"`
	State     string    `json:"state"`
	StartedAt time.Time `json:"startedAt"`
	Head      string    `json:"head"`
	PRURL     string    `json:"prURL"`
	Error     string    `json:"error"`
	Attempts  int       `json:"attempts"`
}
type Operation struct {
	Progress
	RoomID             string    `json:"roomId"`
	Base               string    `json:"base"`
	OldIntegration     string    `json:"oldIntegration"`
	Branch             string    `json:"branch"`
	ExportBranch       string    `json:"exportBranch"`
	ExportExpected     string    `json:"exportExpected"`
	RetryAt            time.Time `json:"retryAt,omitempty"`
	PublicationPending bool      `json:"publicationPending,omitempty"`
}
type Export struct {
	Commit  string   `json:"commit"`
	Branch  string   `json:"branch"`
	PRURL   string   `json:"prURL"`
	Changes []string `json:"changes"`
}
type Commit struct {
	SHA    string `json:"sha"`
	Title  string `json:"title"`
	Author string `json:"author"`
	At     string `json:"at"`
}
type Integration struct {
	Head       string   `json:"head"`
	LastExport string   `json:"lastExport"`
	Commits    []Commit `json:"commits"`
}
type Repository struct {
	Remotes     map[string]Remote `json:"remotes"`
	Integration Integration       `json:"integration"`
	Operation   *Progress         `json:"operation,omitempty"`
}
type Status map[string]Repository

func Active(state string) bool {
	return state == "building" || state == "testing" || state == "publishing"
}

// Update changes the saved snapshot. fn must only change the snapshot; it
// must not run Git, network calls, or another store write.
func (s *Service) Update(fn func(*Snapshot) error) error {
	return s.update(context.Background(), fn)
}

// Status is the sync view of every repository, for the web page. The Git
// work per repository is kept while its refs and its last export stay the
// same, so a web refresh runs no Git process unless a repository moved.
func (s *Service) Status() (Status, error) {
	state, err := s.Read()
	if err != nil {
		return nil, err
	}
	result := Status{}
	aliases := map[string]string{}
	commons := map[string]string{}
	for repo, remotes := range state.Repositories {
		dir := s.commonDir(repo)
		commons[repo] = dir
		key := dir
		if key == "" {
			// The key only keeps this repository's rows apart when Git did
			// not name its common directory. It is not a resolved directory.
			key = repo
		}
		aliases[key] = repo
		result[repo] = Repository{Remotes: remotes}
	}
	for dir := range state.Operations {
		if aliases[dir] == "" {
			aliases[dir], commons[dir] = dir, dir
		}
	}
	for dir := range state.Exports {
		if aliases[dir] == "" {
			aliases[dir], commons[dir] = dir, dir
		}
	}
	for dir, repo := range aliases {
		row := result[repo]
		if row.Remotes == nil {
			row.Remotes = map[string]Remote{}
		}
		if op, ok := state.Operations[dir]; ok {
			progress := op.Progress
			row.Operation = &progress
		}
		row.Integration.LastExport = state.Exports[dir].Commit
		view := s.repoView(commons[repo], repo, row.Integration.LastExport)
		row.Integration.Head = view.head
		row.Integration.Commits = view.commits
		result[repo] = row
	}
	return result, nil
}

// repoView is one repository's Git view. It holds the refs it was read from
// and the export it compared against, so a later call can tell whether it is
// still current without running Git.
type repoView struct {
	common  string
	refs    string
	export  string
	head    string
	commits []Commit
}

// gitRead runs one Git read. Tests count these calls through it.
func (s *Service) gitRead(ctx context.Context, repo string, args ...string) (string, error) {
	if s.git != nil {
		return s.git(ctx, repo, args...)
	}
	return read(ctx, repo, args...)
}

// commonDir is the repository's git common directory. It is kept from the
// last view of that repository, so only a repository this service has not
// resolved runs Git for it. It returns "" when Git did not name one, and the
// next call asks again.
func (s *Service) commonDir(repo string) string {
	if view := s.keptView(repo); view != nil && view.common != "" {
		return view.common
	}
	common, err := s.gitRead(context.Background(), repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return ""
	}
	return common
}

func (s *Service) keptView(repo string) *repoView {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.views[repo]
}

func (s *Service) keepView(repo string, view *repoView) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.views == nil {
		s.views = map[string]*repoView{}
	}
	s.views[repo] = view
}

// repoView returns the repository's integration head and recent commits. A
// kept view whose refs and export did not move is returned as it is. A view
// is kept only when every Git read it made gave an answer: a ref that is not
// there is an answer, a failure to run Git is not, so a temporary failure is
// asked again on the next call. A repository whose common directory is
// unknown keeps no view.
func (s *Service) repoView(common, repo, export string) *repoView {
	refs := ""
	if common != "" {
		refs = refsFingerprint(common)
	}
	if kept := s.keptView(repo); kept != nil && kept.common == common && kept.refs == refs && kept.export == export {
		return kept
	}
	view := &repoView{common: common, refs: refs, export: export, commits: []Commit{}}
	keep := common != ""
	head, err := s.gitRead(context.Background(), repo, "rev-parse", "--verify", "--quiet", "refs/heads/dyna/integration")
	switch {
	case err == nil:
		view.head = head
	case noSuchRef(err):
		view.head = ""
	default:
		keep = false
	}
	if view.head != "" {
		rangeRef := view.head
		if export != "" {
			rangeRef = export + ".." + view.head
		} else if main, err := s.gitRead(context.Background(), repo, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/main"); err == nil {
			rangeRef = main + ".." + view.head
		} else if !noSuchRef(err) {
			keep = false
		}
		if out, err := s.gitRead(context.Background(), repo, "log", "-50", "--format=%H%x00%s%x00%an%x00%aI", rangeRef, "--"); err == nil {
			for _, line := range strings.Split(out, "\n") {
				fields := strings.Split(line, "\x00")
				if len(fields) == 4 {
					view.commits = append(view.commits, Commit{fields[0], fields[1], fields[2], fields[3]})
				}
			}
		} else {
			keep = false
		}
	}
	if keep {
		s.keepView(repo, view)
	}
	return view
}

// noSuchRef reports whether Git ran and said the ref is not there, which is a
// stable answer. Any other failure may be temporary.
func noSuchRef(err error) bool {
	var exit interface{ ExitCode() int }
	return errors.As(err, &exit) && exit.ExitCode() == 1
}

// refsFingerprint names the refs a repository's view depends on. The two refs
// it reads are compared by their value, so a rewrite of the same length is
// seen; packed-refs and a reftable list are compared by size and change time,
// which a pack or a table write always moves. Reading it costs file metadata
// and two short files, not a Git process.
func refsFingerprint(common string) string {
	var b strings.Builder
	for _, name := range []string{"refs/heads/dyna/integration", "refs/remotes/origin/main"} {
		value, err := os.ReadFile(filepath.Join(common, name))
		if err != nil {
			b.WriteString(name + "-;")
			continue
		}
		fmt.Fprintf(&b, "%s=%s;", name, value)
	}
	for _, name := range []string{"packed-refs", "reftable/tables.list"} {
		st, err := os.Stat(filepath.Join(common, name))
		if err != nil {
			b.WriteString(name + "-;")
			continue
		}
		fmt.Fprintf(&b, "%s%d:%d;", name, st.Size(), st.ModTime().UnixNano())
	}
	return b.String()
}

func (s *Service) Wait(ctx context.Context, repo string) (Progress, error) {
	common, err := read(ctx, repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return Progress{}, err
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		state, err := s.Read()
		if err != nil {
			return Progress{}, err
		}
		op, ok := state.Operations[common]
		if !ok {
			return Progress{}, fmt.Errorf("no saved sync operation")
		}
		if !Active(op.State) {
			if op.State != "done" {
				return op.Progress, fmt.Errorf("%s", op.Error)
			}
			return op.Progress, nil
		}
		select {
		case <-ctx.Done():
			return op.Progress, ctx.Err()
		case <-ticker.C:
		}
	}
}
