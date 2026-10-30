package hookinstall

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dyna-agent/internal/processguard"
)

const timeout = 75 * time.Minute

// Start submits a separate user service. No build or restart happens in the
// emitter's cgroup. RemainAfterExit retains the unit result until reconciliation.
func (s Service) Start(ctx context.Context, j Job, out io.Writer) error {
	if !idPattern.MatchString(j.ID) || !shaPattern.MatchString(j.SHA) || j.HookLog == "" {
		return fmt.Errorf("install requires a hook run ID, SHA, and log path")
	}
	j.Unit = "dyna-install-" + j.SHA
	j.Status = "queued"
	j.StartedAt = s.now()
	if err := s.Jobs.Update(j.ID, func(old *Job) error {
		if old.ID != "" {
			return fmt.Errorf("install job already exists")
		}
		*old = j
		return nil
	}); err != nil {
		return err
	}
	args := []string{"systemd-run", "--user", "--unit", j.Unit, "--property=Type=exec", "--property=RemainAfterExit=yes", "--property=RuntimeMaxSec=4500", "--property=Nice=10", "--", j.Config.Executable, "git", "hooks", "_install-worker", j.ID, "--jobs-root", s.Jobs.Root}
	fmt.Fprintln(out, "Install unit: "+j.Unit)
	err := s.Exec(ctx, processguard.Process{Dir: "/", Args: args, Env: processguard.MetadataEnv(), Timeout: 30 * time.Second, Output: out})
	if err != nil {
		_ = s.Jobs.Update(j.ID, func(j *Job) error {
			j.Status = "finished"
			j.Result = "error"
			j.Cause = "unit launch"
			j.FinishedAt = s.now()
			return nil
		})
	}
	return err
}

// Worker runs in the independent systemd unit, including the captured flock wait.
func (s Service) Worker(ctx context.Context, id string) error {
	j, err := s.read(ctx, id)
	if err != nil {
		return err
	}
	if err = s.Jobs.Update(id, func(j *Job) error { j.Status = "running"; return nil }); err != nil {
		return err
	}
	logs := filepath.Join(s.Jobs.Root, "logs")
	if err = os.MkdirAll(logs, 0700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(j.Config.Lock), 0700); err != nil {
		return err
	}
	log, err := os.CreateTemp(logs, "install-"+id+"-*.log")
	if err != nil {
		return err
	}
	args := []string{"flock", "-w", "1800", j.Config.Lock, j.Config.Executable, "git", "hooks", "_install-locked", id, "--jobs-root", s.Jobs.Root}
	runErr := s.Exec(ctx, processguard.Process{Dir: j.Config.Main, Args: args, Env: append(processguard.MetadataEnv(), "DYNA_HOOK_EVENT=install-worker", "DYNA_HOOK_LOG="+log.Name()), Timeout: timeout, Output: log})
	runErr = errors.Join(runErr, log.Close())
	result := "pass"
	if runErr != nil {
		result = "error"
	}
	saveErr := s.Jobs.Update(id, func(j *Job) error {
		j.Status = "finished"
		j.Result = result
		j.WorkerLog = log.Name()
		j.FinishedAt = s.now()
		if runErr != nil {
			j.Cause = runErr.Error()
		}
		return nil
	})
	if runErr != nil {
		return runErr
	}
	return saveErr
}

// InspectUnit recovers results even when the worker died before writing a receipt.
// A live unit stays pending. A successful worker normally supplies its own result.
func (s Service) InspectUnit(ctx context.Context, j Job) (Job, error) {
	if j.Status == "finished" || (j.Status == "queued" && s.now().Sub(j.StartedAt) < time.Minute) {
		return j, nil
	}
	var out bytes.Buffer
	err := s.Exec(ctx, processguard.Process{Dir: "/", Args: []string{"systemctl", "--user", "show", j.Unit, "--property=ActiveState,SubState,Result,ExecMainStatus,LoadState"}, Env: processguard.MetadataEnv(), Timeout: 10 * time.Second, Output: &out})
	if err != nil {
		return j, err
	}
	props := map[string]string{}
	for _, line := range strings.Split(out.String(), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			props[key] = value
		}
	}
	if props["ActiveState"] == "activating" || props["SubState"] == "running" || props["SubState"] == "start" {
		return j, nil
	}
	j.Status = "finished"
	j.FinishedAt = s.now()
	j.Result = "error"
	j.Cause = fmt.Sprintf("unit %s: load=%s active=%s result=%s exit=%s", j.Unit, props["LoadState"], props["ActiveState"], props["Result"], props["ExecMainStatus"])
	if props["Result"] == "success" && props["ExecMainStatus"] == "0" && props["LoadState"] != "not-found" {
		j.Result = "pass"
		j.Cause = ""
	}
	err = s.Jobs.Update(j.ID, func(old *Job) error {
		if old.Status != "finished" {
			old.Status = j.Status
			old.Result = j.Result
			old.Cause = j.Cause
			old.FinishedAt = j.FinishedAt
		}
		j = *old
		return nil
	})
	return j, err
}

// Installing reports whether the install unit for a commit is already active.
// A query that fails reports false, so the launch runs and surfaces the real error.
func (s Service) Installing(ctx context.Context, sha string) bool {
	err := s.Exec(ctx, processguard.Process{Dir: "/", Args: []string{"systemctl", "--user", "is-active", "dyna-install-" + sha}, Env: processguard.MetadataEnv(), Timeout: 10 * time.Second, Output: io.Discard})
	return err == nil
}

func (s Service) CleanupUnit(ctx context.Context, j Job) {
	for _, verb := range []string{"stop", "reset-failed"} {
		_ = s.Exec(ctx, processguard.Process{Dir: "/", Args: []string{"systemctl", "--user", verb, j.Unit}, Env: processguard.MetadataEnv(), Timeout: 10 * time.Second, Output: io.Discard})
	}
}

// Log appends the worker output once. A marker makes retries safe after a crash.
func Log(j Job) error {
	marker := "\n[install " + j.ID + " unit " + j.Unit + " result " + j.Result + "]\n"
	if err := os.MkdirAll(filepath.Dir(j.HookLog), 0700); err != nil {
		return err
	}
	old, err := os.ReadFile(j.HookLog)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if bytes.Contains(old, []byte(marker)) {
		return nil
	}
	var output []byte
	if j.WorkerLog != "" {
		output, err = os.ReadFile(j.WorkerLog)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	f, err := os.OpenFile(j.HookLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.Write(output); err != nil {
		return err
	}
	if j.Cause != "" {
		if _, err = fmt.Fprintln(f, j.Cause); err != nil {
			return err
		}
	}
	_, err = io.WriteString(f, marker)
	return err
}
