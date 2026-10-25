package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/gitexec"
	"dyna-agent/internal/hooks"
	"dyna-agent/internal/lanesync"
	"dyna-agent/internal/locallane"
	"dyna-agent/internal/pr/trial"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/store"
)

var errExportMoved = errors.New("export branch moved outside the operation; refusing to overwrite it")

const maxExportPublicationAttempts = 5

type SyncStarted struct {
	Started bool `json:"started"`
}

func (s *Service) SyncRefresh(ctx context.Context, roomID, repo string) (SyncStarted, error) {
	return s.startSyncOperation(ctx, roomID, repo, "refresh")
}
func (s *Service) GitExport(ctx context.Context, roomID, repo string) (SyncStarted, error) {
	return s.startSyncOperation(ctx, roomID, repo, "export")
}

func (s *Service) startSyncOperation(ctx context.Context, roomID, repo, kind string) (SyncStarted, error) {
	common, err := gitRead(ctx, repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return SyncStarted{}, err
	}
	r := Record{Kind: "local", RoomID: roomID, RepoPath: common}
	if why, err := s.trialRoomHold(ctx, r); err != nil {
		return SyncStarted{}, err
	} else if why != "" {
		return SyncStarted{}, fmt.Errorf("cannot %s: %s", kind, why)
	}
	var n uint64
	if err := s.Store.update(func(st *State) error { st.NextMergeTrial++; n = st.NextMergeTrial; return nil }); err != nil {
		return SyncStarted{}, err
	}
	err = lanesync.New().Update(func(st *lanesync.Snapshot) error {
		if lanesync.Active(st.Operations[common].State) {
			return fmt.Errorf("a sync operation is already running for this repository")
		}
		previous := st.Operations[common]
		if previous.PublicationPending && previous.State == "failed" {
			if kind != "export" {
				return errors.New("retry the unfinished export before refreshing")
			}
			if previous.RoomID != roomID {
				return errors.New("resume the unfinished export from its original room")
			}
			previous.State, previous.Error, previous.Attempts = "publishing", "", 0
			previous.RetryAt = time.Time{}
			st.Operations[common] = previous
			return nil
		}
		st.Operations[common] = lanesync.Operation{Progress: lanesync.Progress{Kind: kind, State: "building", StartedAt: s.now()}, RoomID: roomID, Branch: fmt.Sprintf("dyna/merge-trial/%d", n)}
		return nil
	})
	return SyncStarted{Started: err == nil}, err
}

// SyncTick advances saved operations. Local CI runs separately and never
// blocks this tick. It returns when it is next due: one poll later while an
// operation is active, or zero. Starting an operation wakes it by its sync
// event.
func (s *Service) SyncTick(ctx context.Context) (time.Time, error) {
	retry := s.now().Add(pollEvery)
	st, err := lanesync.New().Read()
	if err != nil {
		return retry, err
	}
	var failures []error
	// The pass is due again while an operation is active.
	var next time.Time
	for repo, op := range st.Operations {
		if !lanesync.Active(op.State) {
			continue
		}
		next = retry
		// An operation lease, not a store lock: one pass advances a repo's
		// operation at a time, TryLock never waits, and no writer takes it, so
		// the git and network work below blocks no store write (item 6).
		path := filepath.Join(s.Store.Root, "sync-"+digest(repo)[:16]+".lock")
		unlock, owned, err := store.TryLock(path)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if !owned {
			continue
		}
		latest, readErr := lanesync.New().Read()
		if readErr != nil {
			unlock()
			failures = append(failures, readErr)
			continue
		}
		op = latest.Operations[repo]
		if lanesync.Active(op.State) {
			err = s.advanceSyncOperation(ctx, repo, op)
		}
		unlock()
		if err != nil {
			failures = append(failures, err)
		}
	}
	if err := errors.Join(failures...); err != nil {
		return retry, err
	}
	return next, nil
}
func saveSyncOperation(repo string, op lanesync.Operation) error {
	return lanesync.New().Update(func(st *lanesync.Snapshot) error {
		if st.Operations[repo].Branch != op.Branch {
			return errors.New("sync operation changed")
		}
		st.Operations[repo] = op
		return nil
	})
}
func syncBatch(repo string, op lanesync.Operation) trial.Batch {
	return trial.Batch{Remote: repo, Base: op.Base, DefaultBranch: locallane.Integration, Branch: op.Branch, Heads: []string{op.OldIntegration}, Created: op.StartedAt}
}
func (s *Service) advanceSyncOperation(ctx context.Context, repo string, op lanesync.Operation) error {
	r := Record{Kind: "local", Key: "integration-" + digest(repo)[:16], RoomID: op.RoomID, RepoPath: repo, Head: op.OldIntegration}
	client := s.trialClient(r)
	finish := func(state, reason string) error {
		op.State, op.Error = state, reason
		if state == "done" {
			op.PublicationPending = false
		}
		if !op.PublicationPending && op.Head != "" && op.Base != "" {
			if err := client.Delete(ctx, syncBatch(repo, op), op.Head); err != nil {
				return err
			}
		}
		return saveSyncOperation(repo, op)
	}
	if why, err := s.trialRoomHold(ctx, r); err != nil {
		return finish("failed", err.Error())
	} else if why != "" {
		return finish("failed", why)
	}
	switch op.State {
	case "building":
		if op.Base == "" {
			fetchCtx := ghclient.WithRoom(ghclient.WithRooms(ctx, s.Notify.Rooms), op.RoomID)
			remoteRecord, err := s.exportRecord(ctx, r)
			if err != nil {
				return finish("failed", err.Error())
			}
			if err := lanesync.FetchOrigin(fetchCtx, repo, s.trialClient(remoteRecord).Run); err != nil {
				return finish("failed", err.Error())
			}
			op.Base, err = gitRead(ctx, repo, "rev-parse", "--verify", "refs/remotes/origin/main^{commit}")
			if err != nil {
				return finish("failed", err.Error())
			}
			op.OldIntegration, err = locallane.Ensure(ctx, repo)
			if err != nil {
				return finish("failed", err.Error())
			}
			r.Head = op.OldIntegration
			if op.Kind == "export" {
				if err := s.prepareLaneExport(ctx, r, &op); err != nil {
					return finish("failed", err.Error())
				}
			}
			if err := saveSyncOperation(repo, op); err != nil {
				return err
			}
		}
		result, err := client.Build(ctx, syncBatch(repo, op), func(result trial.Result) error { op.Head = result.Head; return saveSyncOperation(repo, op) })
		if err != nil {
			return finish("failed", err.Error())
		}
		if len(result.Conflicts) > 0 {
			paths, _ := mergeConflictPaths(ctx, repo, op.Base, op.OldIntegration)
			text := "dyna/integration conflicts with origin/main"
			if len(paths) > 0 {
				text += "\n" + strings.Join(paths, "\n")
			}
			if err := s.postMergeNotice(ctx, r, "integration-conflict-"+op.Base, Notice{Text: text}); err != nil {
				return err
			}
			return finish("conflict", text)
		}
		op.Head, op.State = result.Head, "testing"
		return saveSyncOperation(repo, op)
	case "testing":
		ci, err := s.localSyncCI(ctx, r, op.Head)
		if err != nil {
			return finish("failed", err.Error())
		}
		if ci.Status == VerdictFailed {
			return finish("failed", ci.Logs)
		}
		if ci.Status != VerdictPassed {
			return nil
		}
		op.State = "publishing"
		return saveSyncOperation(repo, op)
	case "publishing":
		if s.now().Before(op.RetryAt) {
			return nil
		}
		// Recheck the complete saved CI evidence after a restart before publication.
		ci, err := s.localSyncCI(ctx, r, op.Head)
		if err != nil {
			return finish("failed", err.Error())
		}
		if ci.Status == VerdictFailed {
			return finish("failed", ci.Logs)
		}
		if ci.Status != VerdictPassed {
			return nil
		}
		if op.Kind == "refresh" {
			publish := syncBatch(repo, op)
			publish.Base = op.OldIntegration
			if err := client.Publish(ctx, publish, op.Head); err != nil {
				return finish("failed", err.Error())
			}
			if err := hooks.New().Emit(ctx, hooks.Event{Event: hooks.PRMerged, Repo: repo, Worktree: repo, Branch: locallane.Integration, SHA: op.Head, Room: op.RoomID}); err != nil {
				return err
			}
		} else {
			op.Attempts++
			op.RetryAt = time.Time{}
			if err := saveSyncOperation(repo, op); err != nil {
				return err
			}
			url, err := s.publishLaneExport(ctx, r, op)
			if err != nil {
				op.PublicationPending = !errors.Is(err, errExportMoved)
				if errors.Is(err, errExportMoved) || op.Attempts >= maxExportPublicationAttempts {
					return finish("failed", err.Error())
				}
				op.Error = err.Error()
				op.RetryAt = s.now().Add(time.Minute)
				return saveSyncOperation(repo, op)
			}
			op.PRURL = url
		}
		return finish("done", "")
	}
	return nil
}

// mergeConflictPaths lists the paths with line-level conflicts when a and b
// merge. merge-tree needs no checkout; its exit status 1 means conflicts.
func mergeConflictPaths(ctx context.Context, repo, a, b string) ([]string, error) {
	_, paths, err := mergeTree(ctx, repo, a, b)
	return paths, err
}

// mergeTree also returns the merged tree, which holds conflict markers
// when paths conflict.
func mergeTree(ctx context.Context, repo, a, b string) (string, []string, error) {
	out, err := gitexec.Command(ctx, repo, "merge-tree", "--write-tree", "--name-only", "-z", a, b).Output()
	var exit *exec.ExitError
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
		return "", nil, fmt.Errorf("git merge-tree: %w", err)
	}
	fields := strings.Split(string(out), "\x00")
	var paths []string
	for _, path := range fields[1:] {
		if path == "" {
			break
		}
		if !slices.Contains(paths, path) {
			paths = append(paths, path)
		}
	}
	return fields[0], paths, nil
}

func (s *Service) exportRecord(ctx context.Context, r Record) (Record, error) {
	url, err := gitRead(ctx, r.RepoPath, "remote", "get-url", "origin")
	if err != nil {
		return Record{}, err
	}
	identity, err := repoid.RemoteIdentity(r.RepoPath, url)
	if err != nil {
		return Record{}, err
	}
	host, name, ok := strings.Cut(identity, "/")
	// File remotes support refresh without a GitHub account.
	if !ok || host == "" || filepath.IsAbs(identity) {
		return r, nil
	}
	r.Kind, r.Host, r.Repo = "github", host, name
	return r, nil
}

type exportPull struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
}

func (s *Service) findExportPR(ctx context.Context, r Record, branch string) (exportPull, error) {
	if branch == "" {
		return exportPull{}, nil
	}
	out, err := s.github(r.RoomID).command(ctx, r.RepoPath, false, "pr", "list", "--repo", r.Host+"/"+r.Repo, "--head", branch, "--state", "open", "--json", "number,url", "--limit", "1")
	if err != nil {
		return exportPull{}, err
	}
	var prs []exportPull
	if err := json.Unmarshal(out, &prs); err != nil {
		return exportPull{}, err
	}
	if len(prs) > 0 {
		return prs[0], nil
	}
	return exportPull{}, nil
}
func (s *Service) prepareLaneExport(ctx context.Context, r Record, op *lanesync.Operation) error {
	remote, err := s.exportRecord(ctx, r)
	if err != nil {
		return err
	}
	if changeKind(remote) != "github" {
		return errors.New("export requires a GitHub origin")
	}
	state, err := lanesync.New().Read()
	if err != nil {
		return err
	}
	previous := state.Exports[r.RepoPath]
	pr, err := s.findExportPR(ctx, remote, previous.Branch)
	if err != nil {
		return err
	}
	op.ExportBranch = strings.Replace(op.Branch, "dyna/merge-trial/", "dyna/export/", 1)
	op.ExportExpected = ""
	if pr.Number > 0 {
		op.ExportBranch, op.ExportExpected = previous.Branch, previous.Commit
	}
	return nil
}

func (s *Service) publishLaneExport(ctx context.Context, r Record, op lanesync.Operation) (string, error) {
	remote, err := s.exportRecord(ctx, r)
	if err != nil {
		return "", err
	}
	if changeKind(remote) != "github" {
		return "", errors.New("export requires a GitHub origin")
	}
	syncState, err := lanesync.New().Read()
	if err != nil {
		return "", err
	}
	previous := syncState.Exports[r.RepoPath]
	g := s.github(r.RoomID)
	if op.ExportBranch == "" {
		return "", errors.New("export destination was not frozen before CI")
	}
	pr, err := s.findExportPR(ctx, remote, op.ExportBranch)
	if err != nil {
		return "", err
	}
	branch := op.ExportBranch
	body, changes, err := s.exportBody(ctx, r.RepoPath, op.OldIntegration, previous, pr.Number > 0)
	if err != nil {
		return "", err
	}
	run := s.trialClient(remote).Run
	url := "https://" + remote.Host + "/" + remote.Repo + ".git"
	readHead := func() (string, error) {
		var out strings.Builder
		probe := ghclient.Command(ctx, "git", r.RepoPath, "ls-remote", "--", url, "refs/heads/"+branch)
		probe.Stdout = &out
		if err := run(ctx, probe); err != nil {
			return "", err
		}
		if fields := strings.Fields(out.String()); len(fields) > 0 {
			return fields[0], nil
		}
		return "", nil
	}
	current, err := readHead()
	if err != nil {
		return "", err
	}
	if current != op.Head {
		if current != op.ExportExpected {
			return "", errExportMoved
		}
		push := ghclient.Command(ctx, "git", r.RepoPath, "push", "--force-with-lease=refs/heads/"+branch+":"+op.ExportExpected, "--", url, op.Head+":refs/heads/"+branch)
		if err := run(ctx, push); err != nil {
			head, readErr := readHead()
			if readErr == nil && head != op.Head && head != op.ExportExpected {
				return "", errExportMoved
			}
			if readErr != nil || head != op.Head {
				return "", err
			}
		}
	}
	dir, err := os.MkdirTemp(filepath.Join(s.Store.Root, "trials"), "export-body-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "body.md")
	if err := os.WriteFile(file, []byte(body), 0600); err != nil {
		return "", err
	}
	prURL := ""
	if pr.Number > 0 {
		if _, err := g.command(ctx, r.RepoPath, false, "pr", "edit", fmt.Sprint(pr.Number), "--repo", remote.Host+"/"+remote.Repo, "--body-file", file); err != nil {
			return "", err
		}
		prURL = pr.URL
	} else {
		out, err := g.command(ctx, r.RepoPath, false, "pr", "create", "--repo", remote.Host+"/"+remote.Repo, "--base", "main", "--head", branch, "--title", "Export local integration changes", "--body-file", file)
		if err != nil {
			return "", err
		}
		prURL = strings.TrimSpace(string(out))
	}
	err = lanesync.New().Update(func(st *lanesync.Snapshot) error {
		st.Exports[r.RepoPath] = lanesync.Export{Commit: op.Head, Branch: branch, PRURL: prURL, Changes: changes}
		return nil
	})
	return prURL, err
}
func (s *Service) exportBody(ctx context.Context, repo, integration string, previous lanesync.Export, open bool) (string, []string, error) {
	st, err := s.Store.Read()
	if err != nil {
		return "", nil, err
	}
	keys := map[string]bool{}
	if open {
		for _, key := range previous.Changes {
			keys[key] = true
		}
	}
	for key, r := range st.Records {
		if changeKind(*r) != "local" || r.RepoPath != repo || r.State != "merged" {
			continue
		}
		if gitexec.Command(ctx, repo, "merge-base", "--is-ancestor", r.Head, integration).Run() != nil {
			continue
		}
		if previous.Commit != "" && gitexec.Command(ctx, repo, "merge-base", "--is-ancestor", r.Head, previous.Commit).Run() == nil {
			continue
		}
		keys[key] = true
	}
	var changes []Record
	if open {
		for _, key := range previous.Changes {
			if r := st.Records[key]; r != nil {
				changes = append(changes, *r)
			}
			delete(keys, key)
		}
	}
	var added []Record
	for key := range keys {
		if r := st.Records[key]; r != nil {
			added = append(added, *r)
		}
	}
	sort.Slice(added, func(i, j int) bool {
		if added[i].Created.Equal(added[j].Created) {
			return added[i].Key < added[j].Key
		}
		return added[i].Created.Before(added[j].Created)
	})
	changes = append(changes, added...)
	body := "Local changes merged into dyna/integration:\n"
	var ids []string
	for _, r := range changes {
		ids = append(ids, r.Key)
		body += fmt.Sprintf("\n## %s\n\nIntent: %s\n\nRisks: %s\n\nTesting: %s\n", r.Title, r.Notes.Intent, r.Notes.Risks, r.Notes.Testing)
	}
	return body, ids, nil
}
