package connect

import (
	"context"
	"errors"
	"net/http"
)

// checkGrant asks the relay whether the session's device may still use
// this machine, with a 60-second cache. refused is a real "no".
func (s *Service) checkGrant(ctx context.Context, x Session) (ok, refused bool, err error) {
	s.mu.Lock()
	g, cached := s.grants[x.Device]
	s.mu.Unlock()
	l, linked := s.link()
	if !linked {
		return false, true, nil
	}
	if !cached || s.now().Sub(g.at) > grantCache {
		res, err := s.relay(l).CheckGrant(ctx, l.Machine, x.User, x.Device)
		if err != nil {
			if IsRefusal(err) {
				var re *RelayError
				errors.As(err, &re)
				if re.Status == http.StatusGone {
					l.Unlinked = true
					_ = s.o.Store.SaveLink(l)
				}
				return false, true, nil
			}
			return false, false, err
		}
		g = grantEntry{at: s.now(), active: res.Active, epoch: res.Epoch}
		s.mu.Lock()
		s.grants[x.Device] = g
		s.mu.Unlock()
		if res.Epoch > l.Epoch {
			l = s.bumpEpoch(l, res.Epoch)
		}
	}
	return g.active && x.Epoch >= g.epoch, !(g.active && x.Epoch >= g.epoch), nil
}

func (s *Service) dropGrant(dev string) {
	s.mu.Lock()
	delete(s.grants, dev)
	s.mu.Unlock()
}
