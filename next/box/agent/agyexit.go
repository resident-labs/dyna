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
// refusing fails the Google CONNECTs only, with the reason as the status text, and agy's own retry opens a new one. An engine that cannot
// be reached yet (see throughEngine) is waited for first, so a deploy's restart does not fail the turn in flight.
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

// engineWait is how long a Google CONNECT keeps trying to reach the engine before it fails. A deploy restarts the engine, and its own
// readiness check (scripts/deploy-trial.sh) allows the owner socket up to 30 attempts a second apart after the restart, so 30 s covers
// a restart that goes as the deploy expects; a longer outage is a real one and fails as before. engineBackoff is the pause between
// attempts, and each attempt dials with its own timeout of at most engineDial. They are variables so that a test can shorten them.
var (
	engineWait    = 30 * time.Second
	engineBackoff = 250 * time.Millisecond
	engineDial    = 5 * time.Second
)

// dialEngine connects to the engine's socket; a test replaces it to make a write fail.
var dialEngine = func(ctx context.Context, socket string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "unix", socket)
}

// throughEngine sends one CONNECT to the engine's agy exit. It waits for our own engine and for nothing else: while the socket cannot be
// dialled (the engine is down) or the request cannot be written whole (the engine has not read a full request, so nothing can have started
// toward Google), it dials again on a fresh connection and sends the request again, for engineWait at most and no longer than the client
// stays. Once the request is written, nothing is sent again: an engine that closed the connection without answering, or refused, may
// already have started toward Google, and that answer goes to the client as it is. An open tunnel that is cut later stays cut.
func throughEngine(w http.ResponseWriter, r *http.Request, socket, target string) {
	failed := func(status int, text string) {
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.WriteString(conn, "HTTP/1.1 "+strconv.Itoa(status)+" "+oneLine(text)+"\r\n\r\n")
	}
	waitCtx, stop := context.WithTimeout(r.Context(), engineWait)
	defer stop()
	var engine net.Conn
	for {
		dialCtx, cancel := context.WithTimeout(waitCtx, engineDial)
		conn, err := dialEngine(dialCtx, socket)
		cancel()
		if err == nil {
			if _, err = io.WriteString(conn, "POST "+contract.AgyExitPath+target+" HTTP/1.1\r\nHost: dyna\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"); err == nil {
				engine = conn
				break
			}
			conn.Close()
		}
		select {
		case <-waitCtx.Done():
			failed(http.StatusBadGateway, "the agy exit is unreachable: the engine did not answer")
			return
		case <-time.After(engineBackoff):
		}
	}
	defer engine.Close()
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
