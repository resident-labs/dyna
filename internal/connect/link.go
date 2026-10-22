package connect

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"time"
)

// LinkOptions describes a new machine link and its approval prompt.
type LinkOptions struct {
	Relay, Name       string
	AppPort, CardPort int
	Started           func(LinkStart, string)
}

// LinkMachine waits for approval and saves the machine before fetching tunnel
// credentials. Sync can finish setup if the credential request fails.
func (s *Store) LinkMachine(ctx context.Context, o LinkOptions) (Link, error) {
	return s.linkMachine(ctx, o, 3*time.Second)
}

func (s *Store) linkMachine(ctx context.Context, o LinkOptions, interval time.Duration) (Link, error) {
	relayURL, err := CheckRelayURL(o.Relay)
	if err != nil {
		return Link{}, err
	}
	if l, ok, err := s.Link(); err != nil {
		return Link{}, err
	} else if ok && !l.Unlinked {
		return Link{}, fmt.Errorf("this computer is already linked as %s (%s) to %s; run dyna engine connect unlink first", l.Name, l.Machine, l.Owner.Name)
	}
	// A fresh key for every link: an old key may still be on the relay.
	if err := s.ResetKey(); err != nil {
		return Link{}, err
	}
	key, err := s.Key()
	if err != nil {
		return Link{}, err
	}
	rl := &Relay{Base: relayURL, Key: key}
	secret := randomToken(40)
	start, err := rl.StartLink(ctx, o.Name, secret, o.AppPort, o.CardPort)
	if err != nil {
		return Link{}, err
	}
	if o.Started != nil {
		o.Started(start, Fingerprint(key.Public().(ed25519.PublicKey)))
	}
	deadline := time.UnixMilli(start.Expires).Add(time.Minute)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return Link{}, ctx.Err()
		case <-time.After(interval):
		}
		p, err := rl.PollLink(ctx, start.ID, secret)
		if err != nil {
			if IsRefusal(err) {
				return Link{}, err
			}
			continue // the relay may be briefly away; keep polling
		}
		switch p.Status {
		case "denied":
			return Link{}, errors.New("the link was denied on the relay page")
		case "expired":
			return Link{}, errors.New("the code expired; run dyna engine connect link again")
		case "linked":
			m := p.Machine
			l := Link{Relay: relayURL, Machine: m.ID, Name: m.Name, Owner: m.Owner, App: m.App, Cards: m.Cards, Epoch: m.Epoch, Linked: time.Now()}
			if err := s.SaveLink(l); err != nil {
				return Link{}, err
			}
			s.Audit("link", fmt.Sprintf("%s (%s) to %s", m.Name, m.ID, m.Owner.Name))
			tok, err := rl.TunnelToken(ctx, m.ID)
			if err != nil {
				return l, fmt.Errorf("linked, but the tunnel token did not come: %w (dyna engine run tries again)", err)
			}
			if err := s.SaveTunnelToken(tok); err != nil {
				return l, fmt.Errorf("linked, but the tunnel token was not saved: %w (dyna engine run tries again)", err)
			}
			if ks, err := rl.FetchKeys(ctx); err == nil {
				_ = s.SaveKeySet(ks)
			}
			return l, nil
		}
	}
	return Link{}, errors.New("no approval in time; run dyna engine connect link again")
}
