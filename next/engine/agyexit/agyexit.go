// Package agyexit is the way out of an agy box for Google's hosts: the box's proxy sends them here, so Google sees one IP however many
// hosts run agy. The box's agent listens for a CONNECT on a loopback port and forwards each one to a Google host to this route over the box
// socket (the proxy is configured, not enforced); the engine dials the Google host itself and the two ends are joined byte for byte. TLS runs from agy to Google, and nothing here reads,
// logs or stores a byte of it.
package agyexit

import (
	"context"
	"log"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/resident-labs/dyna/contract"
)

var count atomic.Int64

// Handler serves /agy-exit/<host>:443, which the box sends as a POST because a CONNECT's target has no path to route on. It refuses
// anything but a host below the four domains on port 443, with the reason as the status text the box passes on to agy.
func Handler(dial func(ctx context.Context, network, address string) (net.Conn, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			refuse(w, http.StatusMethodNotAllowed, "the agy exit takes only a CONNECT")
			return
		}
		target := strings.TrimPrefix(r.URL.Path, contract.AgyExitPath)
		host, port, err := net.SplitHostPort(target)
		if err != nil || port != "443" {
			refuse(w, http.StatusForbidden, "the agy exit takes only port 443")
			return
		}
		if normal, ok := contract.AgyExitHost(host); !ok || normal != host {
			refuse(w, http.StatusForbidden, "the agy exit does not serve "+host)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		out, err := dial(ctx, "tcp", target)
		cancel()
		if err != nil {
			refuse(w, http.StatusBadGateway, "the agy exit could not reach "+target)
			return
		}
		defer out.Close()
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		log.Printf("agy exit: %s (%d)", target, count.Add(1))
		if _, err := conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
			return
		}
		done := make(chan struct{}, 2)
		pipe := func(to, from net.Conn) {
			buffer := make([]byte, 32<<10)
			for {
				n, err := from.Read(buffer)
				if n > 0 {
					if _, werr := to.Write(buffer[:n]); werr != nil {
						break
					}
				}
				if err != nil {
					break
				}
			}
			done <- struct{}{}
		}
		go pipe(out, conn)
		go pipe(conn, out)
		<-done
	})
}

func refuse(w http.ResponseWriter, status int, text string) {
	w.Header().Set("X-Dyna-Exit", text)
	w.WriteHeader(status)
}
