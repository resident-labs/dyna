package callerid

import (
	"dyna-agent/internal/processguard"
	"net"
)

// connPeer keeps socket process operations injectable without real processes.
type connPeer interface {
	PID() int
	Pinned() bool
	Alive() bool
	Close() error
}

var resolveConnPeer = func(conn net.Conn) (connPeer, error) {
	return processguard.SocketPeer(conn)
}

// ResolveConn identifies the socket peer and checks its pinned exit event
// after reading caller identity. Signal 0 cannot detect an unreaped exit.
func (r Resolver) ResolveConn(conn net.Conn) Identity {
	peer, err := resolveConnPeer(conn)
	if err != nil {
		return refused("cannot read the caller's process from its socket: %v", err)
	}
	defer peer.Close()
	id := r.Resolve(peer.PID())
	if peer.Pinned() && !peer.Alive() {
		return refused("the caller exited while the engine identified it, so its PID may name another process")
	}
	return id
}
