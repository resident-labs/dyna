package workflowsvc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"dyna-agent/internal/fsutil"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/scratch"
	"dyna-agent/internal/shells"
	"dyna-agent/internal/workflow"
)

// commandHold is how long a command's exit stays held for its run, so the
// shell's owner is not woken for a result the script already reads. A run
// that dies leaves the hold to lapse, and the owner then hears of the exit.
const commandHold = 48 * time.Hour

// CommandRunnerFor builds the runner of a run's run() commands. Tests replace
// it with a fake.
var CommandRunnerFor = func(run *runstore.Run, resumeFrom string) (workflow.CommandRunner, error) {
	exe, err := fsutil.RunningExecutable()
	if err != nil {
		return nil, err
	}
	owner := run.Meta.ParentSession
	if owner == "" {
		owner = runstore.OwnerFromEnv()
	}
	return &shellRunner{store: shells.DefaultStore(), exe: exe, run: run.Meta.ID, roomID: run.Meta.RoomID, owner: owner, resumeFrom: resumeFrom}, nil
}

// shellRunner runs each command as a managed shell that belongs to the run, so
// a cancel of the run stops it and the shell list shows it.
type shellRunner struct {
	store      *shells.Store
	exe        string
	run        string
	roomID     string
	owner      string
	resumeFrom string
}

func (r *shellRunner) Start(_ context.Context, spec workflow.CommandSpec) (string, error) {
	cmd := spec.Argv
	if spec.StderrPath != "" {
		// $0 is the stderr file and "$@" the command: its standard error
		// leaves the shell log, which then holds standard output only.
		cmd = append([]string{"sh", "-c", `exec "$@" 2>"$0"`, spec.StderrPath}, cmd...)
	}
	runMeta, err := runstore.ReadMeta(r.run)
	if err != nil {
		return "", err
	}
	owner := runMeta.ParentSession
	if owner == "" {
		owner = runMeta.Session
	}
	env, err := r.environment(spec.Env, owner)
	if err != nil {
		return "", err
	}
	if len(env) > 0 {
		cmd = append(append([]string{"env"}, env...), cmd...)
	}
	meta, err := r.store.StartJob(shells.StartOptions{Name: spec.Name, Cmd: cmd, Cwd: spec.Cwd, Owner: r.owner, WorkflowOwner: true, Run: r.run, RoomID: r.roomID},
		time.Now().Add(commandHold), func(m shells.Meta) error { return shells.Spawn(r.exe, m.ID) })
	if err != nil {
		return "", err
	}
	return meta.ID, nil
}

// environment is the extra variables of a command. The shell inherits the
// run's own environment; the temporary folder variables the run does not set
// point at the owner's disk temporary folder, as for session shells.
func (r *shellRunner) environment(extra []string, owner string) ([]string, error) {
	temp := map[string]string{}
	for _, name := range scratch.TempVars {
		if v, ok := os.LookupEnv(name); ok {
			temp[name] = v
		}
	}
	for _, kv := range extra {
		if name, _, ok := strings.Cut(kv, "="); ok {
			delete(temp, name)
		}
	}
	if err := scratch.SetTemp(temp, owner); err != nil {
		return nil, fmt.Errorf("prepare temporary folder: %w", err)
	}
	env := append([]string(nil), extra...)
	for _, name := range scratch.TempVars {
		if v, ok := temp[name]; ok && !hasName(extra, name) {
			env = append(env, name+"="+v)
		}
	}
	return env, nil
}

func hasName(env []string, name string) bool {
	for _, kv := range env {
		if strings.HasPrefix(kv, name+"=") {
			return true
		}
	}
	return false
}

func (r *shellRunner) Attach(_ context.Context, shell string) error {
	if !shells.ValidID(shell) {
		return fmt.Errorf("invalid shell id %q", shell)
	}
	info, err := r.store.Info(shell)
	if err != nil {
		return err
	}
	// Only a shell of the run being resumed is this run's to claim.
	if r.resumeFrom == "" || info.Run != r.resumeFrom {
		return fmt.Errorf("shell %s does not belong to the run being resumed", shell)
	}
	return r.store.HoldExit(shell, time.Now().Add(commandHold))
}

func (r *shellRunner) Wait(ctx context.Context, shell string) (workflow.CommandExit, error) {
	info, _, err := r.store.Wait(ctx, shell, 0, nil)
	if err != nil {
		return workflow.CommandExit{}, err
	}
	// The script reads the exit, so the owner need not be told.
	_, _ = r.store.ClaimExit(shell)
	log, _ := r.store.OutputPath(shell)
	out := workflow.CommandExit{LogPath: log}
	switch {
	case info.Status == "lost" || info.Exit == nil:
		out.Code, out.Error = -1, "the shell was lost before it recorded an exit"
	default:
		out.Code, out.Signal, out.Error = info.Exit.Code, info.Exit.Signal, info.Exit.Error
	}
	return out, nil
}

func (r *shellRunner) Output(shell string, limit int64) (string, error) {
	path, err := r.store.OutputPath(shell)
	if err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit))
	// A shell without a display prints one hint line before the command; it
	// is not the command's output.
	return strings.TrimPrefix(string(b), shells.NoDisplayHint+"\n"), err
}

func (r *shellRunner) Stop(shell string) error {
	_, err := r.store.Stop(shell, "agent")
	return err
}
