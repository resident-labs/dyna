package agent

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// ListenAgyExit opens the loopback port that agy and every command it runs are told to use as their HTTP and HTTPS proxy, and serves it
// until ctx ends. Traffic to Google's own hosts leaves through the engine's agy exit, so that Google sees one IP however many hosts run
// agy; everything else goes out directly from the box, as it would with no proxy at all. The proxy is configured, not enforced: a
// program that ignores its environment connects without it. It fails if the port cannot be taken, and the box then does not open agy.
func ListenAgyExit(ctx context.Context, socket string) error {
	listener, err := net.Listen("tcp", contract.AgyExitAddr)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: agyProxy(socket)}
	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()
	go func() { _ = server.Serve(listener) }()
	return nil
}

// directDial bounds a direct connection as net/http's default transport does.
const directDial = 30 * time.Second

// agyProxy serves the proxy: a CONNECT to a Google host goes to the engine, a CONNECT to any other host is dialled from here, and a
// plain-HTTP request, which a tool sends to its proxy whole, is forwarded to its origin from here. An engine that is away, restarting or
// refusing fails the Google CONNECTs only, each once with the reason as the status text, and agy's own retry opens a new one.
func agyProxy(socket string) http.Handler {
	direct := &http.Transport{DialContext: (&net.Dialer{Timeout: directDial}).DialContext}
	forward := &httputil.ReverseProxy{
		Transport: direct,
		// The request is absolute-form, so its URL is the origin. ReverseProxy removes the hop-by-hop headers and adds no
		// X-Forwarded-For here.
		Rewrite:       func(r *httputil.ProxyRequest) { r.Out.URL.Scheme, r.Out.URL.Host = r.In.URL.Scheme, r.In.URL.Host },
		FlushInterval: -time.Nanosecond,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			host, port, err := net.SplitHostPort(r.Host)
			if err != nil {
				http.Error(w, "a CONNECT names host:port", http.StatusBadRequest)
				return
			}
			if normal, google := contract.AgyExitHost(host); google {
				throughEngine(w, r, socket, net.JoinHostPort(normal, port))
			} else {
				dialDirect(w, r)
			}
			return
		}
		if r.URL.Host == "" {
			http.Error(w, "this is a proxy: send an absolute URL or a CONNECT", http.StatusBadRequest)
			return
		}
		// Google's traffic leaves only as TLS through the exit, and a request to this port would only loop.
		if _, google := contract.AgyExitHost(r.URL.Hostname()); google || r.URL.Host == contract.AgyExitAddr {
			http.Error(w, "the agy proxy does not forward plain HTTP to "+r.URL.Hostname(), http.StatusForbidden)
			return
		}
		forward.ServeHTTP(w, r)
	})
}

// dialDirect joins the client to the target from this box, with no help from the engine.
func dialDirect(w http.ResponseWriter, r *http.Request) {
	out, err := (&net.Dialer{Timeout: directDial}).DialContext(r.Context(), "tcp", r.Host)
	if err != nil {
		http.Error(w, "could not reach "+r.Host, http.StatusBadGateway)
		return
	}
	defer out.Close()
	conn, _, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	join(conn, out)
}

// throughEngine sends one CONNECT to the engine's agy exit once. It waits for nothing and sends nothing again.
func throughEngine(w http.ResponseWriter, r *http.Request, socket, target string) {
	failed := func(status int, text string) {
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.WriteString(conn, "HTTP/1.1 "+strconv.Itoa(status)+" "+oneLine(text)+"\r\n\r\n")
	}
	dialCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	engine, err := (&net.Dialer{}).DialContext(dialCtx, "unix", socket)
	cancel()
	if err != nil {
		failed(http.StatusBadGateway, "the agy exit is unreachable: the engine did not answer")
		return
	}
	defer engine.Close()
	if _, err := io.WriteString(engine, "POST "+contract.AgyExitPath+target+" HTTP/1.1\r\nHost: dyna\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"); err != nil {
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
	join(conn, struct {
		io.Reader
		io.Writer
	}{response.Body, engine})
}

// join copies both ways until either side ends.
func join(a io.ReadWriter, b io.ReadWriter) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(b, a); done <- struct{}{} }()
	go func() { _, _ = io.Copy(a, b); done <- struct{}{} }()
	<-done
}

func oneLine(text string) string {
	text = strings.NewReplacer("\r", " ", "\n", " ").Replace(text)
	if len(text) > 160 {
		text = text[:160]
	}
	return text
}
