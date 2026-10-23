package computers

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
)

type Handler func(context.Context, Request) (any, error)
type connectionKey struct{}

var connectionSeq atomic.Uint64

// ConnectionID identifies the lifetime of a control connection, not a request.
func ConnectionID(ctx context.Context) string {
	id, _ := ctx.Value(connectionKey{}).(string)
	return id
}

// Subscription is a stream reply. Cancel must release its event registration.
type Subscription struct {
	Events <-chan Event
	Cancel func()
}

// Serve accepts bounded JSON lines and preserves each connection for leases.
// A disconnected peer cancels a blocked act without blocking other requests.
func Serve(ctx context.Context, l net.Listener, handler Handler) error {
	go func() { <-ctx.Done(); l.Close() }()
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go serveConnection(ctx, conn, handler)
	}
}
func serveConnection(parent context.Context, conn net.Conn, handler Handler) {
	ctx, cancel := context.WithCancel(context.WithValue(parent, connectionKey{}, strconv.FormatUint(connectionSeq.Add(1), 10)))
	defer cancel()
	defer conn.Close()
	go func() { <-ctx.Done(); conn.Close() }()
	lines := make(chan []byte, 1)
	go func() {
		defer cancel()
		scan := bufio.NewScanner(conn)
		scan.Buffer(make([]byte, 4096), 1<<20)
		for scan.Scan() {
			line := append([]byte(nil), scan.Bytes()...)
			select {
			case lines <- line:
			case <-ctx.Done():
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case line := <-lines:
			var req Request
			err := json.Unmarshal(line, &req)
			var value any
			if err == nil {
				value, err = handler(ctx, req)
			}
			if err != nil {
				value = map[string]string{"error": err.Error()}
			}
			enc := json.NewEncoder(conn)
			if sub, ok := value.(Subscription); ok {
				defer sub.Cancel()
				if enc.Encode(map[string]bool{"subscribed": true}) != nil {
					return
				}
				for {
					select {
					case <-ctx.Done():
						return
					case event, ok := <-sub.Events:
						if !ok {
							return
						}
						if enc.Encode(event) != nil {
							return
						}
					}
				}
			}
			if enc.Encode(value) != nil {
				return
			}
		}
	}
}

// FakeSupervisor serves real persistent connections without GUI processes.
type FakeSupervisor struct {
	mu          sync.Mutex
	Current     Status
	Picture     Shot
	Handle      Handler
	Changed     chan struct{} // test hook: one token per state change
	views       map[string]string
	humanViewer string
	next        int
}

// changed reports a state change to a waiting test and never blocks.
func (f *FakeSupervisor) changed() {
	select {
	case f.Changed <- struct{}{}:
	default:
	}
}

func NewFakeSupervisor(id string) *FakeSupervisor {
	return &FakeSupervisor{Current: Status{SessionID: id, State: StateCreated, Width: 1280, Height: 800, Control: ControlAgent, Apps: []App{}}, views: map[string]string{}}
}
func (f *FakeSupervisor) Serve(ctx context.Context, l net.Listener) error {
	return Serve(ctx, l, f.Dispatch)
}
func (f *FakeSupervisor) Dispatch(ctx context.Context, r Request) (any, error) {
	if f.Handle != nil {
		return f.Handle(ctx, r)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Op {
	case "register":
		return Meta{SessionID: r.Session, Room: r.Room}, nil
	case "list":
		return []Status{f.Current}, nil
	case "status":
		return f.Current, nil
	case "shot":
		return f.Picture, nil
	case "act":
		return ActionResult{Shot: f.Picture, Frames: []Frame{{Shot: f.Picture}}, ActionsExecuted: len(r.Actions)}, nil
	case "view.open":
		f.next++
		id := fmt.Sprint(f.next)
		f.views[id] = ConnectionID(ctx)
		f.Current.Viewers = len(f.views)
		f.Current.Stream.Running = true
		f.changed()
		go func() {
			<-ctx.Done()
			f.mu.Lock()
			defer f.mu.Unlock()
			delete(f.views, id)
			if f.humanViewer == id {
				f.humanViewer = ""
				f.Current.Control = ControlAgent
			}
			f.Current.Viewers = len(f.views)
			if len(f.views) == 0 {
				f.Current.Control = ControlAgent
			}
			f.changed()
		}()
		return View{ViewerID: id, RFBSocket: "fake-rfb.sock"}, nil
	case "view.close":
		delete(f.views, r.ViewerID)
		if f.humanViewer == r.ViewerID {
			f.humanViewer = ""
			f.Current.Control = ControlAgent
		}
		f.Current.Viewers = len(f.views)
		if len(f.views) == 0 {
			f.Current.Control = ControlAgent
		}
		f.changed()
		return Viewers{len(f.views)}, nil
	case "control":
		if r.Mode != ControlAgent && r.Mode != ControlHuman {
			return nil, fmt.Errorf("invalid control mode")
		}
		if _, ok := f.views[r.ViewerID]; !ok {
			return nil, fmt.Errorf("viewer lease is closed")
		}
		f.humanViewer = ""
		if r.Mode == ControlHuman {
			f.humanViewer = r.ViewerID
		}
		f.Current.Control = r.Mode
		f.changed()
		return Control{r.Mode}, nil
	case "stop":
		f.Current.State = StateIdleStopped
		f.views = map[string]string{}
		f.Current.Viewers = 0
		f.humanViewer = ""
		f.Current.Control = ControlAgent
		f.Current.Stream.Running = false
		return Stopped{f.Current.State}, nil
	default:
		return nil, fmt.Errorf("unknown computer operation %q", r.Op)
	}
}
