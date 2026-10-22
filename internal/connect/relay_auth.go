package connect

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"dyna-agent/internal/websvc"
)

// --- relay requests: mint and revoke ---

type relayClaims struct {
	Op     string `json:"op"`
	Iss    string `json:"iss"`
	Aud    string `json:"aud"`
	Sub    string `json:"sub"`
	Dev    string `json:"dev"`
	Epoch  int    `json:"epoch"`
	JKT    string `json:"jkt"`
	Origin string `json:"origin"`
	Nonce  string `json:"nonce"`
	Iat    int64  `json:"iat"`
	Exp    int64  `json:"exp"`
}

// relayKey finds a relay key by kid. It refetches the key set when the kid
// is unknown or the cache is older than 7 days, at most once a minute.
func (s *Service) relayKey(ctx context.Context, l Link, kid string) (ed25519.PublicKey, error) {
	s.mu.Lock()
	ks := s.keys
	fresh := s.now().Sub(ks.Fetched) < keySetMaxAge
	k, found := ks.key(kid)
	tried := s.now().Sub(s.keysTried) < keyRefetch
	if !(found && fresh) && !tried {
		s.keysTried = s.now()
	}
	s.mu.Unlock()
	if found && fresh {
		return k, nil
	}
	if tried {
		return nil, errors.New("the relay key is unknown or the cached key set is too old")
	}
	nks, err := s.relay(l).FetchKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("cannot fetch the relay key set: %w", err)
	}
	_ = s.o.Store.SaveKeySet(nks)
	s.mu.Lock()
	s.keys = nks
	s.mu.Unlock()
	if k, ok := nks.key(kid); ok {
		return k, nil
	}
	return nil, errors.New("the relay key is unknown")
}

// relayRequest checks a JWS from the relay: its key, issuer, audience,
// a 60-second window and a nonce seen once.
func (s *Service) relayRequest(r *http.Request, op string) (Link, relayClaims, error) {
	l, ok := s.link()
	if !ok {
		return l, relayClaims{}, errors.New("this machine is not linked")
	}
	if r.Method != http.MethodPost {
		return l, relayClaims{}, errors.New("use POST")
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, 8192))
	if err != nil {
		return l, relayClaims{}, err
	}
	j, err := parseJWS(strings.TrimSpace(string(b)))
	if err != nil {
		return l, relayClaims{}, err
	}
	if j.header.Alg != "EdDSA" {
		return l, relayClaims{}, errors.New("the relay signs with EdDSA")
	}
	key, err := s.relayKey(r.Context(), l, j.header.Kid)
	if err != nil {
		return l, relayClaims{}, err
	}
	if !ed25519.Verify(key, []byte(j.input), j.sig) {
		return l, relayClaims{}, errors.New("bad relay signature")
	}
	var c relayClaims
	if err := json.Unmarshal(j.payload, &c); err != nil {
		return l, relayClaims{}, errors.New("bad relay claims")
	}
	now := s.now().Unix()
	switch {
	case c.Op != op:
		return l, c, fmt.Errorf("wrong operation %q", c.Op)
	case c.Iss != l.Relay:
		return l, c, errors.New("wrong issuer")
	case c.Aud != l.Machine:
		return l, c, errors.New("this request is for another machine")
	case c.Exp < now || c.Iat > now+60 || c.Exp-c.Iat > 60:
		return l, c, errors.New("the relay request expired; check this computer's clock")
	case !nonceRe.MatchString(c.Nonce) || !s.once("relay:"+c.Nonce, time.Unix(c.Exp, 0).Add(time.Minute)):
		return l, c, errors.New("replayed relay request")
	}
	if c.Epoch < l.Epoch {
		return l, c, errors.New("the relay request has an old epoch")
	}
	if c.Epoch > l.Epoch {
		l = s.bumpEpoch(l, c.Epoch)
	}
	return l, c, nil
}

// once records a one-time value; false when it was seen already.
func (s *Service) once(key string, until time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for k, t := range s.seen {
		if now.After(t) {
			delete(s.seen, k)
		}
	}
	if _, dup := s.seen[key]; dup {
		return false
	}
	s.seen[key] = until
	return true
}

func (s *Service) bumpEpoch(l Link, epoch int) Link {
	l.Epoch = epoch
	if err := s.o.Store.SaveLink(l); err != nil {
		s.logf("connect: save link: %v", err)
	}
	s.endSessions(func(x Session) bool { return x.Epoch < epoch }, fmt.Sprintf("link epoch is now %d", epoch))
	return l
}

var jktRe = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// MintMessage is what the machine signs in a mint answer: the relay
// checks it with the key it stored when the machine linked.
func MintMessage(nonce, code string) string {
	return "dyna-connect-mint.v1\n" + nonce + "\n" + hashToken(code)
}

func (s *Service) mint(w http.ResponseWriter, r *http.Request, rt websvc.Route) {
	w.Header().Set("Cache-Control", "no-store")
	l, ok := s.link()
	// Mint only through the managed tunnel host, where the relay sends it.
	if !ok || rt.App != l.App {
		websvc.JSONError(w, http.StatusNotFound, "not found")
		return
	}
	if !s.limit.allow("mint", 30, time.Minute) {
		websvc.JSONError(w, http.StatusTooManyRequests, "too many sign-ins")
		return
	}
	l, c, err := s.relayRequest(r, "mint")
	if err != nil {
		s.o.Store.Audit("mint.refused", err.Error())
		websvc.JSONError(w, http.StatusForbidden, err.Error())
		return
	}
	if c.Sub != l.Owner.ID {
		websvc.JSONError(w, http.StatusForbidden, "that account does not own this machine")
		return
	}
	if !jktRe.MatchString(c.JKT) || !strings.HasPrefix(c.Dev, "dev_") || !slices.ContainsFunc(s.o.Routes, func(x Route) bool { return x.App == c.Origin }) {
		websvc.JSONError(w, http.StatusBadRequest, "bad mint request")
		return
	}
	code := randomToken(32)
	s.mu.Lock()
	for k, v := range s.codes {
		if s.now().After(v.exp) {
			delete(s.codes, k)
		}
	}
	s.codes[hashToken(code)] = mintCode{jkt: c.JKT, origin: c.Origin, dev: c.Dev, user: c.Sub, epoch: l.Epoch, exp: s.now().Add(CodeLife)}
	s.mu.Unlock()
	s.o.Store.Audit("mint", fmt.Sprintf("device %s at %s", c.Dev, c.Origin))
	sig := ed25519.Sign(s.key, []byte(MintMessage(c.Nonce, code)))
	websvc.JSONReply(w, http.StatusOK, map[string]string{"code": code, "sig": b64.EncodeToString(sig)})
}

func (s *Service) revoke(w http.ResponseWriter, r *http.Request) {
	_, c, err := s.relayRequest(r, "revoke")
	if err != nil {
		websvc.JSONError(w, http.StatusForbidden, err.Error())
		return
	}
	if c.Dev != "" {
		s.dropGrant(c.Dev)
		s.endSessions(func(x Session) bool { return x.Device == c.Dev }, "relay revoked device "+c.Dev)
	}
	websvc.JSONReply(w, http.StatusOK, map[string]bool{"ok": true})
}
