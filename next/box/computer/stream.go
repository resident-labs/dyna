package computer

import (
	"context"
	"fmt"
	"io"
	"net"
)

// Stream carries one VNC connection between the display's socket and in and out, which are the owner's view through the
// engine and podman exec. The bytes pass as they are. It ends when either side closes.
func Stream(ctx context.Context, socket string, in io.Reader, out io.Writer) error {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return fmt.Errorf("the desktop is not running: %w", err)
	}
	defer conn.Close()
	ended := make(chan error, 2)
	go func() { _, err := io.Copy(conn, in); ended <- err }()
	go func() { _, err := io.Copy(out, conn); ended <- err }()
	select {
	case err = <-ended:
		return err
	case <-ctx.Done():
		return nil
	}
}
