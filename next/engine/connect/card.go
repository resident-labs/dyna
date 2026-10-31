// Package connect implements the machine side of the Connect relay protocol.
package connect

import (
	"net"
	"net/http"
)

// CardListener reserves a separate loopback origin. Visual cards are not
// exposed through Connect until their remote access design is implemented.
func CardListener() (net.Listener, *http.Server, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, err
	}
	return listener, &http.Server{Handler: http.NotFoundHandler()}, nil
}
