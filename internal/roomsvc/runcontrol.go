package roomsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/fsutil"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/room"
	"dyna-agent/internal/runsvc"
	"dyna-agent/internal/workflowsvc"
	"dyna-agent/internal/workflowtemplate"
)

type RunSource struct {
	Template string `json:"template,omitempty"`
	Script   string `json:"script,omitempty"`
}
type StartedRun struct {
	ID string `json:"id"`
}

func (s *Service) ListTemplates() ([]workflowtemplate.Info, error) {
	return workflowtemplate.New("").List()
}

func (s *Service) StartRun(ctx context.Context, ref string, source RunSource, input any) (StartedRun, error) {
	if err := authority.Check("run", authority.Of(ctx), nil); err != nil {
		return StartedRun{}, err
	}
	rec, err := s.rooms().Resolve(ref)
	if err != nil {
		return StartedRun{}, err
	}
	actor, err := authority.ActorIn(ctx, rec.ID)
	if err != nil {
		return StartedRun{}, err
	}
	dir, err := roomDir(rec)
	if err != nil {
		return StartedRun{}, err
	}
	if (source.Template == "") == (source.Script == "") {
		return StartedRun{}, fmt.Errorf("choose exactly one template or script")
	}
	script := source.Script
	if source.Template != "" {
		info, _, err := workflowtemplate.New("").ResolveName(source.Template)
		if err != nil {
			return StartedRun{}, err
		}
		script = info.Path
	}
	if !filepath.IsAbs(script) {
		script = filepath.Join(dir, script)
	}
	b, err := json.Marshal(input)
	if err != nil {
		return StartedRun{}, err
	}
	if len(b) > 256*1024 {
		return StartedRun{}, fmt.Errorf("workflow input exceeds 256 KiB")
	}
	src, err := os.ReadFile(script)
	if err != nil {
		return StartedRun{}, err
	}
	env := runEnv(actor, rec)
	if s.Notify.Sessions.Executable == "" {
		return StartedRun{}, fmt.Errorf("dyna executable is unavailable")
	}
	// The configured path is /proc/<engine pid>/exe, which stops resolving while
	// the engine restarts for an update. A copy of the running image does not.
	executable := s.Notify.Sessions.Executable
	if stable, err := fsutil.StableImage(executable); err == nil {
		executable = stable
	}
	id, err := workflowsvc.Launch(ctx, workflowsvc.Options{ScriptSrc: string(src), ScriptPath: script, Args: json.RawMessage(b), WorkDir: dir}, workflowsvc.LaunchOptions{Executable: executable, Env: env})
	if err != nil {
		return StartedRun{}, err
	}
	return StartedRun{ID: id}, nil
}

// runEnv is the child environment for a run started by actor: this process's
// environment without Dyna stamps, plus the room, plus the caller's session
// when a session started the run, so the run stays that session's work. A
// session caller gets subagent mode, as its own shell would pass it, so the
// runner keeps the role that may start work.
func runEnv(actor authority.Actor, rec room.Record) []string {
	env := make([]string, 0, len(os.Environ())+4)
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "DYNA_") {
			env = append(env, item)
		}
	}
	env = append(env, mode.EnvRoomID+"="+rec.ID, mode.EnvRoom+"="+rec.Name)
	if actor.Role == authority.Session && actor.Session != "" {
		env = append(env, mode.EnvMode+"="+mode.Subagent, mode.EnvSession+"="+actor.Session)
	}
	return env
}

func (s *Service) CancelRun(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return runsvc.Cancel(ctx, id)
}
