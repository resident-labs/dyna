package connect

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/resident-labs/dyna/contract"
)

type LinkState interface {
	InitializeConnectLink(context.Context, int64) error
	ConnectLinked(context.Context) (bool, error)
	ConnectAttempt(context.Context) (contract.ConnectAttempt, error)
	SetConnectAttempt(context.Context, contract.ConnectAttempt) error
}

type LinkOptions struct {
	Name              string
	AppPort, CardPort int
	Ticks             <-chan time.Time
	Now               func() time.Time
	Started           func(LinkStart, string)
}

type pendingLink struct {
	Relay  string    `json:"relay"`
	Secret string    `json:"secret"`
	Start  LinkStart `json:"start"`
}

// LinkMachine resumes a pending attempt with its original key and poll secret.
func LinkMachine(ctx context.Context, relay *Relay, files *Files, state LinkState, o LinkOptions) (_ Link, resultErr error) {
	files.mu.Lock()
	defer files.mu.Unlock()
	lock, err := os.OpenFile(filepath.Join(files.dir, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return Link{}, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return Link{}, err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	linked, err := state.ConnectLinked(ctx)
	if err != nil {
		return Link{}, err
	}
	attempt, err := state.ConnectAttempt(ctx)
	if err != nil {
		return Link{}, err
	}
	path := filepath.Join(files.dir, "pending.json")
	finish := func(status contract.ConnectAttemptState) error {
		if err := state.SetConnectAttempt(context.WithoutCancel(ctx), contract.ConnectAttempt{State: status}); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if linked {
		link, err := files.Link()
		if err != nil {
			return Link{}, err
		}
		base, err := CheckRelayURL(link.Relay)
		if err != nil {
			return Link{}, err
		}
		if _, err := os.Stat(filepath.Join(files.dir, "machine.key")); err != nil {
			return Link{}, err
		}
		key, err := files.Key()
		if err != nil {
			return Link{}, err
		}
		relay.Base, relay.Key = base, key
		link, err = completeLink(ctx, relay, files, link)
		if err != nil {
			return link, err
		}
		return link, finish(contract.ConnectAttemptLinked)
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	var pending pendingLink
	switch attempt.State {
	case contract.ConnectAttemptStarting, contract.ConnectAttemptPending:
		b, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) && attempt.State == contract.ConnectAttemptStarting {
			if err := finish(contract.ConnectAttemptFailed); err != nil {
				return Link{}, err
			}
			if err := os.Remove(filepath.Join(files.dir, "machine.key")); err != nil && !errors.Is(err, os.ErrNotExist) {
				return Link{}, err
			}
			return Link{}, errors.New("interrupted link start")
		}
		if err != nil {
			return Link{}, errors.New("incomplete pending link")
		}
		if err := json.Unmarshal(b, &pending); err != nil || pending.Secret == "" || pending.Start.ID == "" || pending.Start.Expires <= 0 {
			return Link{}, errors.New("invalid pending link")
		}
		if attempt.State == contract.ConnectAttemptPending && attempt.Expires != pending.Start.Expires {
			return Link{}, errors.New("inconsistent pending link")
		}
		if _, err := os.Stat(filepath.Join(files.dir, "machine.key")); err != nil {
			return Link{}, errors.New("pending link key missing")
		}
		if _, err := CheckRelayURL(pending.Relay); err != nil {
			return Link{}, err
		}
	case contract.ConnectAttemptIdle, contract.ConnectAttemptCanceled, contract.ConnectAttemptFailed, contract.ConnectAttemptLinked:
		if _, err := os.Stat(path); err == nil {
			return Link{}, errors.New("unexpected pending link")
		} else if !errors.Is(err, os.ErrNotExist) {
			return Link{}, err
		}
		if o.AppPort < 1 || o.AppPort > 65535 || o.CardPort < 1 || o.CardPort > 65535 || o.AppPort == o.CardPort {
			return Link{}, errors.New("invalid app or card port")
		}
		base, err := CheckRelayURL(relay.Base)
		if err != nil {
			return Link{}, err
		}
		if err := state.SetConnectAttempt(ctx, contract.ConnectAttempt{State: contract.ConnectAttemptStarting}); err != nil {
			return Link{}, err
		}
		saved := false
		defer func() {
			if saved {
				return
			}
			if err := finish(contract.ConnectAttemptFailed); err != nil {
				resultErr = errors.Join(resultErr, err)
				return
			}
			if err := os.Remove(filepath.Join(files.dir, "machine.key")); err != nil && !errors.Is(err, os.ErrNotExist) {
				resultErr = errors.Join(resultErr, err)
			}
		}()
		if err := os.Remove(filepath.Join(files.dir, "machine.key")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return Link{}, err
		}
		key, err := files.Key()
		if err != nil {
			return Link{}, err
		}
		relay.Base, relay.Key = base, key
		var secretBytes [40]byte
		if _, err := rand.Read(secretBytes[:]); err != nil {
			return Link{}, err
		}
		pending.Relay, pending.Secret = base, b64.EncodeToString(secretBytes[:])
		pending.Start, err = relay.StartLink(ctx, o.Name, pending.Secret, o.AppPort, o.CardPort)
		if err != nil {
			return Link{}, err
		}
		b, err := json.Marshal(pending)
		if err != nil {
			return Link{}, err
		}
		if err := files.write("pending.json", b); err != nil {
			return Link{}, err
		}
		saved = true
	default:
		return Link{}, errors.New("invalid link attempt state")
	}
	key, err := files.Key()
	if err != nil {
		return Link{}, err
	}
	relay.Base, relay.Key = pending.Relay, key
	if err := state.SetConnectAttempt(ctx, contract.ConnectAttempt{State: contract.ConnectAttemptPending, Expires: pending.Start.Expires}); err != nil {
		return Link{}, err
	}
	if o.Started != nil {
		o.Started(pending.Start, Fingerprint(key.Public().(ed25519.PublicKey)))
	}
	for {
		select {
		case <-ctx.Done():
			return Link{}, ctx.Err()
		case _, ok := <-o.Ticks:
			if !ok {
				return Link{}, errors.New("link polling stopped")
			}
		}
		if now().After(time.UnixMilli(pending.Start.Expires).Add(time.Minute)) {
			if err := finish(contract.ConnectAttemptFailed); err != nil {
				return Link{}, err
			}
			return Link{}, errors.New("link expired")
		}
		p, err := relay.PollLink(ctx, pending.Start.ID, pending.Secret)
		if err != nil {
			return Link{}, err
		}
		switch p.Status {
		case "denied", "expired":
			if err := finish(contract.ConnectAttemptFailed); err != nil {
				return Link{}, err
			}
			return Link{}, errors.New("link was denied or expired")
		case "linked":
			m := p.Machine
			if m.ID == "" || m.Owner.ID == "" || m.Epoch < 1 || routeRank(m.App, m.App) < 0 {
				return Link{}, errors.New("invalid linked machine")
			}
			l := Link{Relay: pending.Relay, Machine: m.ID, Name: m.Name, Owner: m.Owner, App: m.App, Cards: m.Cards}
			if err := files.SaveLink(l); err != nil {
				return Link{}, err
			}
			if err := state.InitializeConnectLink(ctx, m.Epoch); err != nil {
				return Link{}, err
			}
			l, err = completeLink(ctx, relay, files, l)
			if err != nil {
				return l, err
			}
			return l, finish(contract.ConnectAttemptLinked)
		}
	}
}

func completeLink(ctx context.Context, relay *Relay, files *Files, link Link) (Link, error) {
	token, err := relay.TunnelToken(ctx, link.Machine)
	if err != nil {
		return link, fmt.Errorf("linked; tunnel token unavailable: %w", err)
	}
	if err := files.SaveTunnelToken(token); err != nil {
		return link, err
	}
	keys, err := relay.FetchKeys(ctx)
	if err != nil {
		return link, fmt.Errorf("linked; relay keys unavailable: %w", err)
	}
	if err := files.SaveKeys(keys); err != nil {
		return link, err
	}
	return link, nil
}
