package podman

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
)

func (h *Host) PushCache(ctx context.Context, mirror, hostData, repo string, refs []string) error {
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || !filepath.IsAbs(hostData) || strings.ContainsAny(hostData, ":\n\r") {
		return errors.New("invalid cache path or repository")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.Trim(part, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_.-") != "" {
			return errors.New("invalid repository")
		}
	}
	path := filepath.Join(hostData, "git", parts[0], parts[1]+".git")
	_, err := h.run(ctx, "bash", "-c", `if [ ! -d "$1" ]; then git init --bare -- "$1" && git -C "$1" config gc.auto 0; fi`, "dyna", path)
	if err != nil {
		return err
	}
	args := append([]string{"env", "GIT_SSH_COMMAND=ssh -S " + quote(h.socket) + " -o ControlMaster=no", "git", "-C", mirror, "push", "--force", h.destination + ":" + path}, refs...)
	_, err = h.runner.Run(ctx, nil, args...)
	return err
}
