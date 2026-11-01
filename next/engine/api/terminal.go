package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty"
	"github.com/resident-labs/dyna/contract"
)

func terminal(w http.ResponseWriter, r *http.Request, argv []string, cols, rows uint16) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGHUP) }
	cmd.WaitDelay = 2 * time.Second
	// The engine runs under systemd with no TERM of its own, and ssh hands this one to a host's shell.
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	file, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, "terminal unavailable")
		return
	}
	defer file.Close()
	go func() {
		defer cancel()
		for {
			kind, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			switch kind {
			case websocket.MessageBinary:
				if _, err := file.Write(data); err != nil {
					return
				}
			case websocket.MessageText:
				var resize contract.TerminalResize
				if json.Unmarshal(data, &resize) != nil || resize.Cols < 1 || resize.Cols > 1000 || resize.Rows < 1 || resize.Rows > 1000 {
					return
				}
				if pty.Setsize(file, &pty.Winsize{Cols: uint16(resize.Cols), Rows: uint16(resize.Rows)}) != nil {
					return
				}
			}
		}
	}()
	buf := make([]byte, 32*1024)
	for {
		n, err := file.Read(buf)
		if n > 0 && conn.Write(ctx, websocket.MessageBinary, buf[:n]) != nil {
			cancel()
			break
		}
		if err != nil {
			break
		}
	}
	err = cmd.Wait()
	code := 0
	if err != nil {
		code = cmd.ProcessState.ExitCode()
	}
	data, _ := json.Marshal(contract.TerminalExit{ExitCode: code})
	_ = conn.Write(ctx, websocket.MessageText, data)
	_ = conn.Close(websocket.StatusNormalClosure, "")
}
