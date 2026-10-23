package hookinstall

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"dyna-agent/internal/fsutil"
	"dyna-agent/internal/processguard"
)

type driver struct {
	service Service
	config  Config
	output  io.Writer
}

func (d driver) run(ctx context.Context, dir string, args ...string) (string, error) {
	fmt.Fprintln(d.output, "$ "+strings.Join(args, " "))
	var output bytes.Buffer
	env := d.environment()
	err := d.service.Exec(ctx, processguard.Process{Dir: dir, Args: args, Env: env, Timeout: 15 * time.Minute, Output: io.MultiWriter(d.output, &output)})
	if err != nil {
		return "", fmt.Errorf("%s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(output.String()), nil
}

func (d driver) environment() []string {
	env := []string{}
	for _, entry := range processguard.MetadataEnv() {
		if !strings.HasPrefix(entry, "PATH=") {
			env = append(env, entry)
		}
	}
	env = append(env, "PATH="+d.config.Path)
	return env
}

// Locked installs under the lock captured by the job.
func (s Service) Locked(ctx context.Context, id, log string, out io.Writer) error {
	j, err := s.read(ctx, id)
	if err != nil {
		return err
	}
	c := j.Config
	if log != "" {
		if err = s.Jobs.Update(id, func(j *Job) error { j.WorkerLog = log; return nil }); err != nil {
			return err
		}
	}
	if skip, err := s.skipSuperseded(j, out); err != nil || skip {
		return err
	}
	state, err := s.Jobs.installState(c.State)
	if err != nil {
		return err
	}
	d := driver{s, c, out}
	git := func(args ...string) (string, error) {
		return d.run(ctx, c.Main, append([]string{"/usr/bin/git", "-C", c.Main}, args...)...)
	}
	if _, err = git("fetch", "-q", "origin", "main"); err != nil {
		return err
	}
	head, err := git("rev-parse", "origin/main")
	if err != nil {
		return err
	}
	if !shaPattern.MatchString(head) {
		return fmt.Errorf("origin/main is not a full commit SHA")
	}
	branch, err := git("branch", "--show-current")
	if err != nil {
		return err
	}
	warning := Warning{}
	if branch != "main" {
		label := branch
		if label == "" {
			label = "detached"
		}
		warning = Warning{Cause: "branch:" + branch, Text: fmt.Sprintf("The main checkout %s is on branch '%s'. Switch it back with: git -C %s switch main", c.Main, label, c.Main)}
	} else if _, err = git("diff", "--quiet"); err != nil {
		warning = Warning{Cause: "dirty", Text: fmt.Sprintf("The main checkout %s has tracked edits. See: git -C %s status", c.Main, c.Main)}
	} else if _, err = git("diff", "--cached", "--quiet"); err != nil {
		warning = Warning{Cause: "dirty", Text: fmt.Sprintf("The main checkout %s has tracked edits. See: git -C %s status", c.Main, c.Main)}
	} else if _, err = git("merge", "-q", "--ff-only", "origin/main"); err != nil {
		warning = Warning{Cause: "diverged", Text: fmt.Sprintf("The main checkout %s has local commits outside origin/main. See: git -C %s log origin/main..main", c.Main, c.Main)}
	}
	if warning.Cause != "" {
		if state.Alerted != warning.Cause {
			state.Alerted = warning.Cause
			if err = s.Jobs.saveInstallState(c.State, state); err != nil {
				return err
			}
			fmt.Fprintln(out, "alert: "+warning.Text)
			if err = s.Jobs.Update(id, func(j *Job) error { j.Warnings = append(j.Warnings, warning); return nil }); err != nil {
				return err
			}
		}
	} else if state.Alerted != "" {
		state.Alerted = ""
		if err = s.Jobs.saveInstallState(c.State, state); err != nil {
			return err
		}
	}
	if state.Installed == head {
		fmt.Fprintln(out, "Already installed "+head)
		return s.Jobs.Update(id, func(j *Job) error { j.Installed = head; return nil })
	}
	restartComputers, err := d.computersChanged(ctx, state.Installed, head)
	if err != nil {
		return err
	}

	fmt.Fprintln(out, "Installing "+head)
	if _, err = os.Stat(c.Build); os.IsNotExist(err) {
		if _, err = git("worktree", "add", "-q", "--detach", c.Build, head); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if _, err = d.run(ctx, c.Build, "/usr/bin/git", "-C", c.Build, "checkout", "-q", "--detach", head); err != nil {
		return err
	}
	temp, err := os.MkdirTemp("", "dyna-install-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	frontend := filepath.Join(c.Build, "web")
	for _, args := range [][]string{{"nice", "-n", "10", "npm", "ci", "--silent"}, {"nice", "-n", "10", "npm", "run", "build"}} {
		if _, err = d.run(ctx, frontend, args...); err != nil {
			return err
		}
	}
	if _, err = d.run(ctx, c.Build, "nice", "-n", "10", "go", "build", "-p", "2", "-ldflags", "-X dyna-agent/internal/cli/version.buildRevision="+head, "-o", filepath.Join(temp, "dyna"), "./cmd/dyna"); err != nil {
		return err
	}
	if _, err = d.run(ctx, filepath.Join(c.Build, "desktop"), "nice", "-n", "10", "./build.sh", filepath.Join(temp, "dyna-desktop")); err != nil {
		return err
	}
	if _, err = d.run(ctx, temp, filepath.Join(temp, "dyna"), "--help"); err != nil {
		return err
	}
	// The desktop intentionally rejects --help; the old timer also ignores it.
	_, _ = d.run(ctx, temp, filepath.Join(temp, "dyna-desktop"), "--help")
	// Merges can queue newer work while this build is running. Do not publish
	// binaries or restart services for a build that has already been replaced.
	if skip, err := s.skipSuperseded(j, out); err != nil || skip {
		return err
	}
	if err = os.MkdirAll(c.Bin, 0755); err != nil {
		return err
	}
	stamp := s.now().Local().Format("20060102-150405")
	for _, name := range []string{"dyna", "dyna-desktop"} {
		target := filepath.Join(c.Bin, name)
		old, err := os.Stat(target)
		if err == nil {
			if err = copyFile(target, target+".bak-"+stamp, old.Mode().Perm()); err != nil {
				return err
			}
			if err = os.Chtimes(target+".bak-"+stamp, old.ModTime(), old.ModTime()); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		staged := filepath.Join(c.Bin, "."+name+".new")
		if err = copyFile(filepath.Join(temp, name), staged, 0755); err != nil {
			return err
		}
		if err = os.Rename(staged, target); err != nil {
			return err
		}
		if err = pruneBackups(c.Bin, name); err != nil {
			return err
		}
	}
	if err = d.restartServer(ctx, filepath.Join(c.Bin, "dyna"), restartComputers); err != nil {
		return err
	}
	state.Installed = head
	if err = s.Jobs.saveInstallState(c.State, state); err != nil {
		return err
	}
	fmt.Fprintln(out, "Installed "+head)
	return s.Jobs.Update(id, func(j *Job) error { j.Installed = head; return nil })
}

//go:generate go test -run TestComputerCodePathsGenerated . -args -update

func (s Service) skipSuperseded(current Job, out io.Writer) (bool, error) {
	jobs, err := s.Jobs.List()
	if err != nil {
		return false, err
	}
	newest := current
	for _, other := range jobs {
		if other.Repo == current.Repo && other.Config.Main == current.Config.Main &&
			other.Config.State == current.Config.State && other.SHA != current.SHA &&
			(other.Status == "queued" || other.Status == "running") && other.StartedAt.After(newest.StartedAt) {
			newest = other
		}
	}
	if newest.ID == current.ID {
		return false, nil
	}
	fmt.Fprintf(out, "Skipping superseded install %s; newer queued commit %s\n", current.SHA, newest.SHA)
	return true, nil
}

func (d driver) computersChanged(ctx context.Context, old, head string) (bool, error) {
	if !shaPattern.MatchString(old) {
		return false, nil
	}
	args := append([]string{"/usr/bin/git", "-C", d.config.Main, "diff", "--name-only", old, head, "--"}, computerCodePaths...)
	changed, err := d.run(ctx, d.config.Main, args...)
	return changed != "", err
}

// TODO: legacy/shim remove later — keeps the dyna-serve fallback for builds before #452; remove after every install runs dyna-engine.
func (d driver) restartServer(ctx context.Context, dyna string, restartComputers bool) error {
	if _, err := d.run(ctx, d.config.Main, dyna, "engine", "run", "--help"); err != nil {
		_, err = d.run(ctx, d.config.Main, "systemctl", "--user", "restart", "dyna-serve")
		return err
	}
	// The worker started before the build and still has the previous unit
	// definitions linked into it. Let the installed binary apply its policy.
	args := []string{dyna, "engine", "install-services", "--unit-dir", d.config.Units}
	if restartComputers {
		args = append(args, "--restart-computers")
	}
	_, err := d.run(ctx, d.config.Main, args...)
	return err
}

func copyFile(from, to string, mode os.FileMode) error {
	b, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	return fsutil.WriteFile(to, b, mode, true)
}
func pruneBackups(dir, name string) error {
	paths, err := filepath.Glob(filepath.Join(dir, name+".bak-*"))
	if err != nil {
		return err
	}
	type backup struct {
		path     string
		modified time.Time
	}
	files := []backup{}
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			return err
		}
		files = append(files, backup{p, st.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].modified.Equal(files[j].modified) {
			return files[i].path > files[j].path
		}
		return files[i].modified.After(files[j].modified)
	})
	for i := 5; i < len(files); i++ {
		if err = os.Remove(files[i].path); err != nil {
			return err
		}
	}
	return nil
}
