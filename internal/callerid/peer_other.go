//go:build !linux

package callerid

import "net"

// ResolveConn refuses every caller: identity needs Linux /proc and cgroups.
func (r Resolver) ResolveConn(net.Conn) Identity {
	return refused("the engine identifies callers only on Linux")
}
