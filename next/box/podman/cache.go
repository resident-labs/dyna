package podman

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/resident-labs/dyna/box"
)

const prepareGitCache = `old=
if [ -d "$1" ] && [ "$(cat "$1/$3" 2>/dev/null)" != "$2" ]; then
 old="$1.old.$$"
 mv -- "$1" "$old" || exit
fi
if [ ! -d "$1" ]; then
 git init --bare -- "$1" && git -C "$1" config gc.auto 0 || exit
fi
if [ -n "$old" ]; then rm -rf -- "$old" || exit; fi`

func (h *Host) PushCache(ctx context.Context, mirror, cache string, refs []string) error {
	if !filepath.IsAbs(cache) || strings.ContainsAny(cache, ":\n\r") {
		return errors.New("invalid cache path")
	}
	marker, err := os.ReadFile(filepath.Join(mirror, box.GitIdentityMarker))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_, err = h.run(ctx, "bash", "-c", prepareGitCache, "dyna", cache, string(marker), box.GitIdentityMarker)
	if err != nil {
		return err
	}
	args := append([]string{"env", "GIT_SSH_COMMAND=ssh -S " + quote(h.socket) + " -o ControlMaster=no", "git", "-C", mirror, "push", "--force", h.destination + ":" + cache}, refs...)
	_, err = h.runner.Run(ctx, nil, args...)
	if err != nil {
		return err
	}
	_, err = h.run(ctx, "bash", "-c", `if [ -n "$2" ]; then printf '%s' "$2" > "$1/$3"; else rm -f -- "$1/$3"; fi`, "dyna", cache, string(marker), box.GitIdentityMarker)
	return err
}
