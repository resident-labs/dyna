package connect

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// LinkState initializes durable state in one store transaction. Until it
// succeeds, callers must treat any link.json as inactive after a restart.
type LinkState interface {
	InitializeConnectLink(context.Context, int64) error
	ConnectLinked(context.Context) (bool, error)
}

type LinkOptions struct {
	Name              string
	AppPort, CardPort int
	Ticks             <-chan time.Time
	Now               func() time.Time
	Started           func(LinkStart, string)
}

// LinkMachine links through an approved fake or production relay. The caller
// owns the tick source and can cancel without waiting for a timer.
func LinkMachine(ctx context.Context, relay *Relay, files *Files, state LinkState, o LinkOptions) (Link, error) {
	if o.AppPort < 1 || o.AppPort > 65535 || o.CardPort < 1 || o.CardPort > 65535 || o.AppPort == o.CardPort {
		return Link{}, errors.New("invalid app or card port")
	}
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
		return completeLink(ctx, relay, files, link)
	}
	base, err := CheckRelayURL(relay.Base)
	if err != nil {
		return Link{}, err
	}
	relay.Base = base
	if err := os.Remove(filepath.Join(files.dir, "machine.key")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Link{}, err
	}
	key, err := files.Key()
	if err != nil {
		return Link{}, err
	}
	relay.Key = key
	var secretBytes [40]byte
	if _, err = rand.Read(secretBytes[:]); err != nil {
		return Link{}, err
	}
	secret := b64.EncodeToString(secretBytes[:])
	start, err := relay.StartLink(ctx, o.Name, secret, o.AppPort, o.CardPort)
	if err != nil {
		return Link{}, err
	}
	if o.Started != nil {
		o.Started(start, Fingerprint(key.Public().(ed25519.PublicKey)))
	}
	now := o.Now
	if now == nil {
		now = time.Now
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
		if now().After(time.UnixMilli(start.Expires).Add(time.Minute)) {
			return Link{}, errors.New("link expired")
		}
		p, err := relay.PollLink(ctx, start.ID, secret)
		if err != nil {
			return Link{}, err
		}
		switch p.Status {
		case "denied", "expired":
			return Link{}, errors.New("link was denied or expired")
		case "linked":
			m := p.Machine
			if m.ID == "" || m.Owner.ID == "" || m.Epoch < 1 {
				return Link{}, errors.New("invalid linked machine")
			}
			l := Link{Relay: base, Machine: m.ID, Name: m.Name, Owner: m.Owner, App: m.App, Cards: m.Cards}
			if err := files.SaveLink(l); err != nil {
				return Link{}, err
			}
			if err := state.InitializeConnectLink(ctx, m.Epoch); err != nil {
				return Link{}, err
			}
			return completeLink(ctx, relay, files, l)
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
