package lanesync

import (
	"context"
	"fmt"
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
func (s *Service) Status() (Status, error) {
	state, err := s.Read()
	if err != nil {
		return nil, err
	}
	result := Status{}
	aliases := map[string]string{}
	for repo, remotes := range state.Repositories {
		common, _ := read(context.Background(), repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if common == "" {
			common = repo
		}
		aliases[common] = repo
		result[repo] = Repository{Remotes: remotes}
	}
	for common := range state.Operations {
		if aliases[common] == "" {
			aliases[common] = common
		}
	}
	for common := range state.Exports {
		if aliases[common] == "" {
			aliases[common] = common
		}
	}
	for common, repo := range aliases {
		row := result[repo]
		if row.Remotes == nil {
			row.Remotes = map[string]Remote{}
		}
		if op, ok := state.Operations[common]; ok {
			progress := op.Progress
			row.Operation = &progress
		}
		row.Integration.LastExport = state.Exports[common].Commit
		row.Integration.Commits = []Commit{}
		row.Integration.Head, _ = read(context.Background(), repo, "rev-parse", "--verify", "refs/heads/dyna/integration")
		if row.Integration.Head != "" {
			rangeRef := row.Integration.Head
			if row.Integration.LastExport != "" {
				rangeRef = row.Integration.LastExport + ".." + rangeRef
			} else if main, err := read(context.Background(), repo, "rev-parse", "--verify", "refs/remotes/origin/main"); err == nil {
				rangeRef = main + ".." + rangeRef
			}
			out, err := read(context.Background(), repo, "log", "-50", "--format=%H%x00%s%x00%an%x00%aI", rangeRef, "--")
			if err == nil {
				for _, line := range strings.Split(out, "\n") {
					fields := strings.Split(line, "\x00")
					if len(fields) == 4 {
						row.Integration.Commits = append(row.Integration.Commits, Commit{fields[0], fields[1], fields[2], fields[3]})
					}
				}
			}
		}
		result[repo] = row
	}
	return result, nil
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
