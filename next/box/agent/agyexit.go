package agent

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// ListenAgyExit opens the loopback port agy is told to use as its HTTPS proxy, and serves it until ctx ends. Every CONNECT goes to the
// engine's agy exit over the box socket, and nothing else is served: a request that is not a CONNECT is refused, so that agy has no
// path out of the box but the exit. It fails if the port cannot be taken, and the box then does not open agy.
func ListenAgyExit(ctx context.Context, socket string) error {
	listener, err := net.Listen("tcp", contract.AgyExitAddr)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: agyExit(socket)}
	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()
	go func() { _ = server.Serve(listener) }()
	return nil
}

// agyExit forwards a CONNECT to the engine once. It waits for nothing and sends nothing again: an engine that is away, restarting or
// refusing is a failed CONNECT, which fails agy's request with the reason as the status text, and agy's own retry opens a new one.
func agyExit(socket string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "this box's agy proxy takes only a CONNECT", http.StatusMethodNotAllowed)
			return
		}
		failed := func(status int, text string) {
			conn, _, err := http.NewResponseController(w).Hijack()
			if err != nil {
				return
			}
			defer conn.Close()
			_, _ = io.WriteString(conn, "HTTP/1.1 "+itoa(status)+" "+oneLine(text)+"\r\n\r\n")
		}
		dialCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		engine, err := (&net.Dialer{}).DialContext(dialCtx, "unix", socket)
		cancel()
		if err != nil {
			failed(http.StatusBadGateway, "the agy exit is unreachable: the engine did not answer")
			return
		}
		defer engine.Close()
		if _, err := io.WriteString(engine, "POST "+contract.AgyExitPath+r.Host+" HTTP/1.1\r\nHost: dyna\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"); err != nil {
			failed(http.StatusBadGateway, "the agy exit is unreachable: the engine did not answer")
			return
		}
		reader := bufio.NewReader(engine)
		response, err := http.ReadResponse(reader, nil)
		if err != nil {
			failed(http.StatusBadGateway, "the agy exit is unreachable: the engine closed the connection")
			return
		}
		if response.StatusCode != http.StatusOK {
			text := response.Header.Get("X-Dyna-Exit")
			if text == "" {
				text = "the agy exit refused the connection"
			}
			failed(response.StatusCode, text)
			return
		}
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return
		}
		done := make(chan struct{}, 2)
		go func() { _, _ = io.Copy(engine, conn); done <- struct{}{} }()
		go func() { _, _ = io.Copy(conn, response.Body); done <- struct{}{} }()
		<-done
	})
}

func oneLine(text string) string {
	text = strings.NewReplacer("\r", " ", "\n", " ").Replace(text)
	if len(text) > 160 {
		text = text[:160]
	}
	return text
}

func itoa(n int) string { return strconv.Itoa(n) }
