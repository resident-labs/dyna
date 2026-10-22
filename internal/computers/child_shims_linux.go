//go:build linux

package computers

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"dyna-agent/internal/shellquote"
)

// wlroots may double-fork Xwayland. The shim waits until its parent is the
// compositor or subreaper, then keeps SIGKILL as its parent-death signal.
func (s *supervisor) childShims(config string) (string, error) {
	setpriv, err := exec.LookPath("setpriv")
	if err != nil {
		return "", fmt.Errorf("Xwayland parent-death protection needs setpriv: %w", err)
	}
	xwayland, err := exec.LookPath("Xwayland")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(config, "child-bin")
	if err = os.MkdirAll(bin, 0700); err != nil {
		return "", err
	}
	script := "#!/bin/sh\ncomputer_daemon=" + strconv.Itoa(os.Getpid()) + "\ncomputer_compositor_file=" + shellquote.Quote(filepath.Join(RuntimeDir(s.meta.SessionID), "compositor.pid")) + "\ncomputer_retries=0\nwhile :; do\n read -r computer_compositor < \"$computer_compositor_file\" || exit 1\n read -r computer_pid computer_comm computer_state computer_parent computer_rest < /proc/$$/stat || exit 1\n case \"$computer_parent\" in \"$computer_daemon\"|\"$computer_compositor\") break ;; esac\n computer_retries=$((computer_retries+1))\n [ \"$computer_retries\" -lt 200 ] || exit 1\n sleep 0.001\ndone\nexec " + shellquote.Quote(setpriv) + " --pdeathsig KILL /bin/sh -c " + shellquote.Quote("read -r pid comm state parent rest < /proc/$$/stat || exit 1\n[ \"$parent\" = \"$1\" ] || [ \"$parent\" = \"$2\" ] || exit 1\nshift 2\nexec \"$@\"\n") + " dyna-xwayland \"$computer_daemon\" \"$computer_compositor\" " + shellquote.Quote(xwayland) + " \"$@\"\n"
	if err = os.WriteFile(filepath.Join(bin, "Xwayland"), []byte(script), 0700); err != nil {
		return "", err
	}
	return bin, nil
}
