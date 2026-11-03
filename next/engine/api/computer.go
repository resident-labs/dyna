package api

import (
	"context"
	"net/http"
	"os/exec"
	"time"

	"github.com/coder/websocket"
)

// computerStream carries a desktop's VNC stream between the owner's view and argv, the box's `dyna computer stream`
// through the host. Binary frames go to its stdin as they are, and its stdout comes back in binary frames; RFB has its
// own framing, so frame edges mean nothing. The viewer asks for each picture, so the stream holds no queue of its own.
func computerStream(w http.ResponseWriter, r *http.Request, argv []string) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.WaitDelay = 2 * time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	if err := cmd.Start(); err != nil {
		_ = conn.Close(websocket.StatusInternalError, "stream unavailable")
		return
	}
	go func() {
		defer cancel()
		defer stdin.Close()
		for {
			kind, data, err := conn.Read(ctx)
			if err != nil || kind != websocket.MessageBinary {
				return
			}
			if _, err := stdin.Write(data); err != nil {
				return
			}
		}
	}()
	buf := make([]byte, 64*1024)
	for {
		n, err := stdout.Read(buf)
		if n > 0 && conn.Write(ctx, websocket.MessageBinary, buf[:n]) != nil {
			cancel()
			break
		}
		if err != nil {
			break
		}
	}
	_ = cmd.Wait()
	_ = conn.Close(websocket.StatusNormalClosure, "")
}
