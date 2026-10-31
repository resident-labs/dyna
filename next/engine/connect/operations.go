package connect

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/resident-labs/dyna/contract"
)

type operationState interface {
	LinkState
	UnlinkConnect(context.Context) error
	ConnectConnectorMissing(context.Context) (bool, error)
}

// Operations owns the link worker. Its context outlives API requests.
type Operations struct {
	files             *Files
	state             operationState
	ctx               context.Context
	appPort, cardPort int
	mu                sync.Mutex
	cancel            context.CancelFunc
	done              chan struct{}
	wake              chan struct{}
	relay             func(string) *Relay
}

func NewOperations(ctx context.Context, files *Files, state operationState, appPort, cardPort int) *Operations {
	return &Operations{ctx: ctx, files: files, state: state, appPort: appPort, cardPort: cardPort, wake: make(chan struct{}, 1), relay: func(base string) *Relay { return &Relay{Base: base} }}
}

func (o *Operations) launch(req contract.ConnectStartRequest) <-chan error {
	ctx, cancel := context.WithCancel(o.ctx)
	o.cancel, o.done = cancel, make(chan struct{})
	done, ready := o.done, make(chan error, 1)
	go func() {
		defer close(done)
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		defer o.signal()
		notified := false
		started := func(LinkStart, string) {
			if !notified {
				ready <- nil
				notified = true
			}
		}
		for {
			_, err := LinkMachine(ctx, o.relay(req.Relay), o.files, o.state, LinkOptions{Name: req.Name, AppPort: o.appPort, CardPort: o.cardPort, Ticks: ticker.C, Started: started})
			if !notified {
				ready <- err
				notified = true
			}
			if err == nil || ctx.Err() != nil {
				return
			}
			a, e := o.state.ConnectAttempt(ctx)
			linked, le := o.state.ConnectLinked(ctx)
			if e != nil || le != nil || (!linked && a.State != contract.ConnectAttemptPending) {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return ready
}

// Resume restarts an interrupted attempt or completes an approved link.
func (o *Operations) Resume(ctx context.Context) error {
	a, err := o.state.ConnectAttempt(ctx)
	if err != nil {
		return err
	}
	linked, err := o.state.ConnectLinked(ctx)
	if err != nil {
		return err
	}
	if a.State == contract.ConnectAttemptUnlinking {
		o.mu.Lock()
		ctx, cancel := context.WithCancel(o.ctx)
		o.cancel, o.done = cancel, make(chan struct{})
		done := o.done
		go func() {
			defer close(done)
			defer o.signal()
			ticker := time.NewTicker(3 * time.Second)
			defer ticker.Stop()
			for {
				if o.unlink(ctx) == nil || ctx.Err() != nil {
					return
				}
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
		o.mu.Unlock()
		return nil
	}
	if linked || a.State == contract.ConnectAttemptStarting || a.State == contract.ConnectAttemptPending {
		o.mu.Lock()
		defer o.mu.Unlock()
		o.launch(contract.ConnectStartRequest{})
	}
	return nil
}

func (o *Operations) Start(ctx context.Context, req contract.ConnectStartRequest) error {
	if req.Relay == "" {
		req.Relay = DefaultRelay
	}
	if req.Name == "" {
		name, err := os.Hostname()
		if err != nil || name == "" {
			return errors.New("machine name unavailable")
		}
		req.Name = name
	}
	if _, err := CheckRelayURL(req.Relay); err != nil {
		return err
	}
	if req.Name == "" || o.appPort == 0 || o.cardPort == 0 {
		return errors.New("web listener and machine name required")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.done != nil {
		select {
		case <-o.done:
		default:
			return errors.New("link operation is running")
		}
	}
	a, err := o.state.ConnectAttempt(ctx)
	if err != nil {
		return err
	}
	linked, err := o.state.ConnectLinked(ctx)
	if err != nil {
		return err
	}
	if linked || a.State == contract.ConnectAttemptStarting || a.State == contract.ConnectAttemptPending || a.State == contract.ConnectAttemptUnlinking {
		return errors.New("link already active")
	}
	ready := o.launch(req)
	select {
	case err := <-ready:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (o *Operations) Status(ctx context.Context) (contract.ConnectStatusResponse, error) {
	a, err := o.state.ConnectAttempt(ctx)
	if err != nil {
		return contract.ConnectStatusResponse{}, err
	}
	linked, err := o.state.ConnectLinked(ctx)
	if err != nil {
		return contract.ConnectStatusResponse{}, err
	}
	missing, err := o.state.ConnectConnectorMissing(ctx)
	if err != nil {
		return contract.ConnectStatusResponse{}, err
	}
	out := contract.ConnectStatusResponse{Attempt: a, Linked: linked, ConnectorMissing: missing}
	if linked {
		link, err := o.files.Link()
		if err != nil {
			return out, err
		}
		out.Relay = link.Relay
	}
	if linked || a.State == contract.ConnectAttemptPending {
		key, err := o.files.existingKey()
		if err != nil {
			return out, err
		}
		out.Fingerprint = Fingerprint(key.Public().(ed25519.PublicKey))
	}
	if a.State != contract.ConnectAttemptPending || linked {
		return out, nil
	}
	b, err := os.ReadFile(filepath.Join(o.files.dir, "pending.json"))
	if err != nil {
		return out, err
	}
	var p pendingLink
	if err := json.Unmarshal(b, &p); err != nil || p.Start.Expires != a.Expires {
		return out, errors.New("invalid pending link")
	}
	out.URL, out.Code = p.Start.URL, p.Start.UserCode
	return out, nil
}

func (o *Operations) signal() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}
func (o *Operations) Wake() <-chan struct{} { return o.wake }

func (o *Operations) stop() {
	if o.cancel != nil {
		o.cancel()
		<-o.done
		o.cancel, o.done = nil, nil
	}
}

func (o *Operations) Close() { o.mu.Lock(); defer o.mu.Unlock(); o.stop() }

func (o *Operations) Cancel(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	a, err := o.state.ConnectAttempt(ctx)
	if err != nil {
		return err
	}
	if a.State == contract.ConnectAttemptUnlinking {
		return errors.New("unlink is in progress")
	}
	linked, err := o.state.ConnectLinked(ctx)
	if err != nil {
		return err
	}
	if linked {
		return errors.New("link is already active")
	}
	o.stop()
	linked, err = o.state.ConnectLinked(context.WithoutCancel(ctx))
	if err != nil {
		o.launch(contract.ConnectStartRequest{})
		return err
	}
	if linked {
		o.launch(contract.ConnectStartRequest{})
		return errors.New("link is already active")
	}
	return o.unlink(context.WithoutCancel(ctx))
}

func (o *Operations) Unlink(ctx context.Context) error {
	defer o.signal()
	o.mu.Lock()
	defer o.mu.Unlock()
	o.stop()
	return o.unlink(ctx)
}

func (o *Operations) unlink(ctx context.Context) error {
	if err := o.state.SetConnectAttempt(ctx, contract.ConnectAttempt{State: contract.ConnectAttemptUnlinking}); err != nil {
		return err
	}
	o.signal()
	linked, err := o.state.ConnectLinked(ctx)
	if err != nil {
		return err
	}
	if linked {
		link, err := o.files.Link()
		if err != nil {
			return err
		}
		key, err := o.files.existingKey()
		if err != nil {
			return err
		}
		if err := (&Relay{Base: link.Relay, Key: key}).Unlink(ctx, link.Machine); err != nil {
			var relayErr *RelayError
			if !errors.As(err, &relayErr) || relayErr.Status != 410 {
				return err
			}
		}
	}
	if err := o.state.UnlinkConnect(ctx); err != nil {
		return err
	}
	for _, name := range []string{"pending.json", "link.json", "machine.key", "tunnel-token", "jwks.json"} {
		if err := os.Remove(filepath.Join(o.files.dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return o.state.SetConnectAttempt(ctx, contract.ConnectAttempt{State: contract.ConnectAttemptCanceled})
}
