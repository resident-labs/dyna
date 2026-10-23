package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"sync"
	"time"

	"dyna-agent/internal/processguard"
	"dyna-agent/internal/runstore"
)

const hookSocket = "receipt.sock"

type hookOffer struct {
	id, session, context, state string
	messages                    []runstore.SteeringMessage
	expectedBytes               int
	peer                        *processguard.Peer
}

type hookRequest struct {
	Session, Offer   string
	Written          int
	Accepted, Failed bool
}

type hookReply struct{ Offer, Context, State string }

// hookReceiptTransport is inactive until the run explicitly starts it. A grant
// reserves an offer until its writer proves success, failure, or process exit.
// This transport holds only transient IPC state; it writes no receipt files.
type hookReceiptTransport struct {
	mu          sync.Mutex
	listener    net.Listener
	connections map[net.Conn]bool
	handlers    sync.WaitGroup
	current     *hookOffer
	serial      uint64
	stopped     bool
	// killPeer retires a granted peer. Tests replace it.
	killPeer func(context.Context, *processguard.Peer) error
}

func startHookReceiptTransport(dir string) (*hookReceiptTransport, error) {
	listener, err := net.Listen("unix", filepath.Join(dir, hookSocket))
	if err != nil {
		return nil, err
	}
	t := &hookReceiptTransport{listener: listener, connections: map[net.Conn]bool{}}
	t.handlers.Add(1)
	go func() {
		defer t.handlers.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			t.mu.Lock()
			if t.stopped {
				t.mu.Unlock()
				conn.Close()
				return
			}
			t.connections[conn] = true
			t.handlers.Add(1)
			t.mu.Unlock()
			go func() {
				defer t.handlers.Done()
				defer func() { conn.Close(); t.mu.Lock(); delete(t.connections, conn); t.mu.Unlock() }()
				peer, err := processguard.SocketPeer(conn)
				if err != nil {
					return
				}
				if !peer.Pinned() {
					peer.Close()
					return
				}
				_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
				t.serveReceipt(conn, peer)
			}()
		}
	}()
	return t, nil
}

// serveReceipt receives only pinned peers from the socket listener. Tests use
// an in-process connection and no process handle.
func (t *hookReceiptTransport) serveReceipt(conn io.ReadWriter, peer *processguard.Peer) {
	owned := false
	defer func() {
		if !owned && peer != nil {
			peer.Close()
		}
	}()
	decoder := json.NewDecoder(io.LimitReader(conn, 1<<20))
	encoder := json.NewEncoder(conn)
	var request hookRequest
	if err := decoder.Decode(&request); err != nil {
		return
	}
	t.mu.Lock()
	offer := t.current
	if t.stopped || offer == nil || offer.state != "offered" || request.Offer != "" ||
		(offer.session != "" && request.Session != "" && offer.session != request.Session) {
		t.mu.Unlock()
		_ = encoder.Encode(hookReply{})
		return
	}
	offer.state, offer.peer, owned = "granted", peer, true
	reply := hookReply{Offer: offer.id, Context: offer.context, State: "granted"}
	t.mu.Unlock()
	if err := encoder.Encode(reply); err != nil {
		t.markUnresolved(offer)
		return
	}
	if err := decoder.Decode(&request); err != nil {
		t.markUnresolved(offer)
		return
	}
	t.mu.Lock()
	if request.Offer != offer.id || offer.state != "granted" {
		t.mu.Unlock()
		t.markUnresolved(offer)
		return
	}
	switch {
	case request.Accepted && !request.Failed && request.Written == offer.expectedBytes && request.Written > 0:
		offer.state = "accepted"
	case request.Failed && !request.Accepted && request.Written == 0:
		offer.state = "failed"
	default:
		offer.state = "unresolved"
	}
	reply = hookReply{Offer: offer.id, State: offer.state}
	t.mu.Unlock()
	_ = encoder.Encode(reply) // A lost reply cannot undo the recorded outcome.
}

func (t *hookReceiptTransport) markUnresolved(offer *hookOffer) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if offer.state == "granted" {
		offer.state = "unresolved"
	}
}

func (t *hookReceiptTransport) offer(session string, messages []runstore.SteeringMessage) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped {
		return errors.New("hook transport has retired")
	}
	if t.current != nil {
		return errors.New("hook offer still awaits its write outcome")
	}
	text := steeringPrompt(messages)
	payload, err := hookContext(text, "PostToolUse")
	if err != nil {
		return err
	}
	t.serial++
	t.current = &hookOffer{id: fmt.Sprint(t.serial), session: session, context: text,
		state: "offered", expectedBytes: len(payload), messages: append([]runstore.SteeringMessage(nil), messages...)}
	return nil
}

func (t *hookReceiptTransport) taken() []runstore.SteeringMessage {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.current == nil || t.current.state != "accepted" {
		return nil
	}
	messages := t.current.messages
	t.release()
	return messages
}

// withdraw returns only proved failed or ungranted input for replay. A partial
// write or a lost receipt stays reserved until its pinned peer has retired.
func (t *hookReceiptTransport) withdraw() (back, delivered []runstore.SteeringMessage) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.current == nil {
		return nil, nil
	}
	switch t.current.state {
	case "offered", "failed":
		back = t.current.messages
	case "accepted":
		delivered = t.current.messages
	default:
		return nil, nil
	}
	t.release()
	return back, delivered
}

// release requires mu. The terminal receipt proves the peer finished its write.
func (t *hookReceiptTransport) release() {
	if t.current.peer != nil {
		t.current.peer.Close()
	}
	t.current = nil
}

func (t *hookReceiptTransport) reserved() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.current != nil && (t.current.state == "granted" || t.current.state == "unresolved")
}

// retire must run after the harness group stops. A failed peer retirement keeps
// the offer reserved, so the caller cannot replay potentially delivered input.
func (t *hookReceiptTransport) retire(ctx context.Context) (back, delivered []runstore.SteeringMessage, err error) {
	t.mu.Lock()
	t.stopped = true
	listener := t.listener
	offer := t.current
	var peer *processguard.Peer
	if offer != nil && (offer.state == "granted" || offer.state == "unresolved") {
		peer = offer.peer
	}
	kill := t.killPeer
	t.mu.Unlock()
	if kill == nil {
		kill = func(ctx context.Context, p *processguard.Peer) error { return p.KillAndWait(ctx) }
	}
	if listener != nil {
		listener.Close()
	}
	if peer != nil {
		err = kill(ctx, peer)
	}
	t.mu.Lock()
	for conn := range t.connections {
		conn.Close()
	}
	t.mu.Unlock()
	joined := make(chan struct{})
	go func() { t.handlers.Wait(); close(joined) }()
	select {
	case <-joined:
	case <-ctx.Done():
		return nil, nil, errors.Join(err, ctx.Err())
	}
	if err != nil {
		return nil, nil, err
	}
	t.mu.Lock()
	if offer != nil && (offer.state == "granted" || offer.state == "unresolved") {
		if peer == nil {
			t.mu.Unlock()
			return nil, nil, errors.New("granted hook peer has no pinned identity")
		}
		offer.state = "failed"
	}
	t.mu.Unlock()
	back, delivered = t.withdraw()
	return back, delivered, nil
}

// runHookReceipt writes output only after a grant. RunSteerHook calls it.
func runHookReceipt(dir, session string, out io.Writer, event string) error {
	conn, err := net.DialTimeout("unix", filepath.Join(dir, hookSocket), 30*time.Second)
	if err != nil {
		return nil
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	return writeHookReceipt(conn, session, out, event)
}

func writeHookReceipt(conn io.ReadWriter, session string, out io.Writer, event string) error {
	encoder := json.NewEncoder(conn)
	decoder := json.NewDecoder(io.LimitReader(conn, 8<<20))
	if err := encoder.Encode(hookRequest{Session: session}); err != nil {
		return err
	}
	var reply hookReply
	if err := decoder.Decode(&reply); err != nil {
		return err
	}
	if reply.State != "granted" || reply.Context == "" {
		return nil
	}
	payload, err := hookContext(reply.Context, event)
	if err != nil {
		return err
	}
	written, writeErr := out.Write(payload)
	if writeErr == nil && written != len(payload) {
		writeErr = io.ErrShortWrite
	}
	request := hookRequest{Offer: reply.Offer, Written: written, Accepted: writeErr == nil, Failed: writeErr != nil}
	if err := encoder.Encode(request); err != nil {
		return errors.Join(writeErr, err)
	}
	if err := decoder.Decode(&reply); err != nil {
		return errors.Join(writeErr, err)
	}
	if writeErr != nil {
		return writeErr
	}
	if reply.Offer != request.Offer || reply.State != "accepted" {
		return errors.New("hook output receipt was not confirmed")
	}
	return nil
}

func hookContext(text, event string) ([]byte, error) {
	if event == "" {
		event = "PostToolUse"
	}
	encoded, err := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": event, "additionalContext": text}})
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}
