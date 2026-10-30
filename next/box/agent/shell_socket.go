package agent

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
)

// ServeShell accepts local MCP bridges that share the turn's process guard.
func ServeShell(ctx context.Context, socket string, executor ShellExecutor) error {
	if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(socket)
	go func() { <-ctx.Done(); listener.Close() }()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		go func() {
			defer conn.Close()
			_ = Shell(ctx, executor, conn, conn)
		}()
	}
}

// ShellBridge connects Pi's stdio transport to the agent-owned MCP server.
func ShellBridge(socket string, stdin io.Reader, stdout io.Writer) error {
	conn, err := net.Dial("unix", socket)
	if err != nil {
		return err
	}
	defer conn.Close()
	var once sync.Once
	done := make(chan error, 2)
	copyStream := func(dst io.Writer, src io.Reader) {
		_, err := io.Copy(dst, src)
		once.Do(func() { conn.Close() })
		done <- err
	}
	go copyStream(conn, stdin)
	go copyStream(stdout, conn)
	return <-done
}
