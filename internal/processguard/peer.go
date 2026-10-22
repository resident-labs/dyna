package processguard

import (
	"context"
	"errors"
	"net"
)

// Peer pins the process that connected to a private Unix socket. Its PID and
// birth identify that process; only its kernel handle can signal or join it.
type Peer struct {
	pid   int
	birth string
	fd    int
}

func (p *Peer) PID() int      { return p.pid }
func (p *Peer) Birth() string { return p.birth }
func (p *Peer) Pinned() bool  { return p != nil && p.fd >= 0 }

// SocketPeer reads credentials and pins the exact peer where the kernel
// supports it. Callers that need a join must require Pinned before a grant.
func SocketPeer(conn net.Conn) (*Peer, error) { return socketPeer(conn) }

// KillAndWait stops this exact pinned peer and waits for its exit. A missing
// pin or uncertain exit is an error; a reused numeric PID is never signaled.
func (p *Peer) KillAndWait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !p.Pinned() {
		return errors.New("private socket peer has no pinned process identity")
	}
	return p.killAndWait(ctx)
}
