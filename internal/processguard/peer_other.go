//go:build !linux

package processguard

import (
	"context"
	"errors"
	"net"
)

func socketPeer(net.Conn) (*Peer, error) {
	return nil, errors.New("private socket process pins require Linux")
}
func (*Peer) Alive() bool  { return false }
func (*Peer) Close() error { return nil }
func (*Peer) killAndWait(context.Context) error {
	return errors.New("private socket process joins require Linux")
}
