package model

import (
	"context"
	"github.com/coder/websocket"
	"github.com/resident-labs/dyna/engine/voice"
)

func (p *Proxy) DialRealtime(ctx context.Context, eligibleIDs []string) (*websocket.Conn, error) {
	return voice.DialProxy(ctx, p.Socket, p.clientKey, eligibleIDs)
}
