package podman

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
)

func (h *Host) PushCache(ctx context.Context, mirror, cache string, refs []string) error {
	if !filepath.IsAbs(cache) || strings.ContainsAny(cache, ":\n\r") {
		return errors.New("invalid cache path")
	}
	_, err := h.run(ctx, "bash", "-c", `if [ ! -d "$1" ]; then git init --bare -- "$1" && git -C "$1" config gc.auto 0; fi`, "dyna", cache)
	if err != nil {
		return err
	}
	args := append([]string{"env", "GIT_SSH_COMMAND=ssh -S " + quote(h.socket) + " -o ControlMaster=no", "git", "-C", mirror, "push", "--force", h.destination + ":" + cache}, refs...)
	_, err = h.runner.Run(ctx, nil, args...)
	return err
}
