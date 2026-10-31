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
}

func NewOperations(ctx context.Context, files *Files, state operationState, appPort, cardPort int) *Operations {
	return &Operations{ctx: ctx, files: files, state: state, appPort: appPort, cardPort: cardPort, wake: make(chan struct{}, 1)}
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
			_, err := LinkMachine(ctx, &Relay{Base: req.Relay}, o.files, o.state, LinkOptions{Name: req.Name, AppPort: o.appPort, CardPort: o.cardPort, Ticks: ticker.C, Started: started})
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
	if linked || a.State == contract.ConnectAttemptStarting || a.State == contract.ConnectAttemptPending {
		o.mu.Lock()
		defer o.mu.Unlock()
		o.launch(contract.ConnectStartRequest{})
	}
	return nil
}

func (o *Operations) Start(ctx context.Context, req contract.ConnectStartRequest) error {
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
	if linked || a.State == contract.ConnectAttemptStarting || a.State == contract.ConnectAttemptPending {
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
	out := contract.ConnectStatusResponse{Attempt: a, Linked: linked}
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
	if _, err := os.Stat(filepath.Join(o.files.dir, "machine.key")); err != nil {
		return out, err
	}
	key, err := o.files.Key()
	if err != nil {
		return out, err
	}
	out.URL, out.Code, out.Fingerprint = p.Start.URL, p.Start.UserCode, Fingerprint(key.Public().(ed25519.PublicKey))
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
	o.stop()
	linked, err := o.state.ConnectLinked(ctx)
	if err != nil {
		return err
	}
	if linked {
		return errors.New("link is already active")
	}
	if err := o.state.SetConnectAttempt(ctx, contract.ConnectAttempt{State: contract.ConnectAttemptCanceled}); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(o.files.dir, "pending.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (o *Operations) Unlink(ctx context.Context) error {
	defer o.signal()
	o.mu.Lock()
	defer o.mu.Unlock()
	o.stop()
	linked, err := o.state.ConnectLinked(ctx)
	if err != nil {
		return err
	}
	if linked {
		link, err := o.files.Link()
		if err != nil {
			return err
		}
		key, err := o.files.Key()
		if err != nil {
			return err
		}
		if err := (&Relay{Base: link.Relay, Key: key}).Unlink(ctx, link.Machine); err != nil {
			return err
		}
	}
	if err := o.state.UnlinkConnect(ctx); err != nil {
		return err
	}
	if err := o.state.SetConnectAttempt(ctx, contract.ConnectAttempt{State: contract.ConnectAttemptCanceled}); err != nil {
		return err
	}
	for _, name := range []string{"pending.json", "link.json", "machine.key", "tunnel-token", "jwks.json"} {
		if err := os.Remove(filepath.Join(o.files.dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
