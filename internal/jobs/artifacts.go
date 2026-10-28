package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// maxArtifactBytes bounds what one job keeps, so a job that names a whole
// build tree cannot fill the engine's disk.
const maxArtifactBytes = 4 << 30

// artifactCopyTimeout bounds the copy of a job's artifacts from its node.
const artifactCopyTimeout = 15 * time.Minute

// ArtifactDir is the engine folder that keeps a job's artifacts after the job
// ends, next to its log.
func ArtifactDir(logPath string) string {
	return filepath.Join(filepath.Dir(logPath), "artifacts")
}

// checkArtifact accepts a path relative to the job's work folder that stays
// inside it.
func checkArtifact(p string) error {
	clean := path.Clean(p)
	if p == "" || path.IsAbs(p) || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("artifact %q must be a path inside the job's work folder, such as target/release/app", p)
	}
	return nil
}

// artifactScript runs on the node in the job's work folder. It writes a tar
// stream of the named paths that exist and names each missing one on stderr.
const artifactScript = `cd "$1" || exit 3; shift; keep=(); ` +
	`for p in "$@"; do if [ -e "$p" ]; then keep+=("$p"); else printf '%s\n' "$p" >&2; fi; done; ` +
	`[ ${#keep[@]} -eq 0 ] || tar -cf - -- "${keep[@]}"`

// copyArtifacts copies the job's named outputs from its work folder on the
// node to dest on this machine. A missing path does not fail the copy; the
// returned note names it.
func copyArtifacts(ctx context.Context, x Executor, job Job, dest string) (string, error) {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", err
	}
	archive, err := os.CreateTemp(filepath.Dir(dest), "artifacts-*.tar")
	if err != nil {
		return "", err
	}
	defer os.Remove(archive.Name())
	defer archive.Close()
	ctx, cancel := context.WithTimeout(ctx, artifactCopyTimeout)
	defer cancel()
	var missing bytes.Buffer
	out := &cappedWriter{w: archive, left: maxArtifactBytes}
	args := append([]string{path.Join(job.Dir, "work")}, job.Artifacts...)
	if err := x.NodeExec(ctx, nil, out, &missing, artifactScript, args...); err != nil {
		if out.full {
			return "", fmt.Errorf("artifacts exceed %d GiB", maxArtifactBytes>>30)
		}
		return "", fmt.Errorf("copy artifacts from %s: %w: %s", job.Node, err, strings.TrimSpace(missing.String()))
	}
	note := ""
	if names := strings.Fields(missing.String()); len(names) > 0 {
		note = "artifacts not found: " + strings.Join(names, ", ")
	}
	if out.written == 0 {
		return note, nil
	}
	if output, err := exec.CommandContext(ctx, "tar", "-xf", archive.Name(), "-C", dest).CombinedOutput(); err != nil {
		return note, fmt.Errorf("unpack artifacts: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return note, nil
}

// cappedWriter writes until left bytes are used, then fails.
type cappedWriter struct {
	w       *os.File
	left    int64
	written int64
	full    bool
}

func (c *cappedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > c.left {
		c.full = true
		return 0, errors.New("artifact size limit reached")
	}
	n, err := c.w.Write(p)
	c.left -= int64(n)
	c.written += int64(n)
	return n, err
}
