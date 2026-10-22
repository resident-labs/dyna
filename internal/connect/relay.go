package connect

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// DefaultRelay is the Resident Labs Connect relay.
const DefaultRelay = "https://connect.residentlabs.dev"

// RelayError is an answer from the relay that says no. A transport failure
// or a 5xx is not a RelayError: then the relay is down, not refusing.
type RelayError struct {
	Status int
	Msg    string
	Code   string
}

func (e *RelayError) Error() string { return fmt.Sprintf("relay: %s (HTTP %d)", e.Msg, e.Status) }

// IsRefusal reports a real answer from the relay that refuses, as opposed
// to a relay that cannot be reached.
func IsRefusal(err error) bool {
	var re *RelayError
	return errors.As(err, &re)
}

// Relay calls the relay's machine API. Every request is signed by the
// machine key: method, path, body hash, a 60-second life and a one-time jti.
type Relay struct {
	Base   string
	Key    ed25519.PrivateKey
	Client *http.Client
}

func (r *Relay) client() *http.Client {
	if r.Client != nil {
		return r.Client
	}
	return &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// call posts body to path, signed with iss as the issuer.
func (r *Relay) call(ctx context.Context, iss, path string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	tok, err := signMachine(r.Key, map[string]any{
		"iss": iss, "aud": r.Base, "htm": "POST", "htu": path, "iat": now, "exp": now + 60,
		"jti": randomToken(18), "bh": hashToken(string(b)),
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.Base+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "DynaMachine "+tok)
	res, err := r.client().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	// A 5xx, a 429 from the relay's rate limiter and a 408 are congestion,
	// not an answer about the grant: the caller treats them as an
	// unreachable relay, so grace rules apply and no session ends.
	if res.StatusCode >= 500 || res.StatusCode == http.StatusTooManyRequests || res.StatusCode == http.StatusRequestTimeout {
		return fmt.Errorf("relay: HTTP %d", res.StatusCode)
	}
	if res.StatusCode != http.StatusOK {
		var e struct{ Error, Code string }
		_ = json.Unmarshal(data, &e)
		if e.Error == "" {
			e.Error = http.StatusText(res.StatusCode)
		}
		return &RelayError{Status: res.StatusCode, Msg: e.Error, Code: e.Code}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// LinkStart is the relay's answer to a link request.
type LinkStart struct {
	ID          string `json:"id"`
	UserCode    string `json:"userCode"`
	URL         string `json:"url"`
	Fingerprint string `json:"fingerprint"`
	Expires     int64  `json:"expires"`
}

// StartLink asks the relay for a device code. The poll secret's hash goes
// with it, so only this process can collect the result.
func (r *Relay) StartLink(ctx context.Context, name, pollSecret string, appPort, cardPort int) (LinkStart, error) {
	var out LinkStart
	err := r.call(ctx, "link", "/v1/link/start", map[string]any{
		"pubkey": b64.EncodeToString(r.Key.Public().(ed25519.PublicKey)), "pollHash": hashToken(pollSecret),
		"name": name, "appPort": appPort, "cardPort": cardPort,
	}, &out)
	return out, err
}

// MachineInfo is the relay's view of this machine.
type MachineInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Owner  Owner  `json:"owner"`
	Epoch  int    `json:"epoch"`
	Status string `json:"status"`
	App    string `json:"app"`
	Cards  string `json:"cards"`
	Relay  string `json:"relay"`
}

// LinkPoll is one poll answer: pending, provisioning, linked, denied or
// expired.
type LinkPoll struct {
	Status  string      `json:"status"`
	Machine MachineInfo `json:"machine"`
}

// PollLink asks whether the owner approved the link.
func (r *Relay) PollLink(ctx context.Context, id, secret string) (LinkPoll, error) {
	var out LinkPoll
	err := r.call(ctx, id, "/v1/link/poll", map[string]any{"id": id, "secret": secret}, &out)
	return out, err
}

// Status reads the machine's record on the relay.
func (r *Relay) Status(ctx context.Context, machine string) (MachineInfo, error) {
	var out struct{ Machine MachineInfo }
	err := r.call(ctx, machine, "/v1/machine/status", map[string]any{"machine": machine}, &out)
	return out.Machine, err
}

// TunnelToken fetches the cloudflared connector token.
func (r *Relay) TunnelToken(ctx context.Context, machine string) (string, error) {
	var out struct{ Token string }
	err := r.call(ctx, machine, "/v1/machine/tunnel-token", map[string]any{"machine": machine}, &out)
	if err == nil && out.Token == "" {
		err = errors.New("relay: empty tunnel token")
	}
	return out.Token, err
}

// Grant is the relay's answer to: may this device still use me?
type Grant struct {
	Active bool `json:"active"`
	Epoch  int  `json:"epoch"`
}

// CheckGrant asks about one relay device of one user.
func (r *Relay) CheckGrant(ctx context.Context, machine, user, device string) (Grant, error) {
	var out Grant
	err := r.call(ctx, machine, "/v1/machine/grant", map[string]any{"machine": machine, "user": user, "dev": device}, &out)
	return out, err
}

// RouteInfo is one route as the relay stores it.
type RouteInfo struct {
	Kind  string `json:"kind"`
	App   string `json:"app"`
	Cards string `json:"cards"`
}

// ReportRoutes tells the relay which origins /open may send a browser to.
func (r *Relay) ReportRoutes(ctx context.Context, machine string, routes []RouteInfo) error {
	return r.call(ctx, machine, "/v1/machine/routes", map[string]any{"machine": machine, "routes": routes}, nil)
}

// ReportOrigin tells the relay the loopback ports the tunnel points at.
func (r *Relay) ReportOrigin(ctx context.Context, machine string, appPort, cardPort int) error {
	return r.call(ctx, machine, "/v1/machine/origin", map[string]any{"machine": machine, "appPort": appPort, "cardPort": cardPort}, nil)
}

// Unlink removes the link on the relay, which deletes the tunnel and names.
func (r *Relay) Unlink(ctx context.Context, machine string) error {
	return r.call(ctx, machine, "/v1/machine/unlink", map[string]any{"machine": machine}, nil)
}

// FetchKeys reads the relay's public key set.
func (r *Relay) FetchKeys(ctx context.Context) (KeySet, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.Base+"/.well-known/jwks.json", nil)
	if err != nil {
		return KeySet{}, err
	}
	res, err := r.client().Do(req)
	if err != nil {
		return KeySet{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return KeySet{}, fmt.Errorf("relay key set: HTTP %d", res.StatusCode)
	}
	var ks KeySet
	if err := json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&ks); err != nil {
		return KeySet{}, err
	}
	if len(ks.Keys) == 0 {
		return KeySet{}, errors.New("relay key set is empty")
	}
	ks.Fetched = time.Now()
	return ks, nil
}

// CheckRelayURL accepts an https relay origin, or http on loopback for tests.
func CheckRelayURL(s string) (string, error) {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("%q is not a relay origin like https://connect.residentlabs.dev", s)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost")) {
		return "", fmt.Errorf("%q: the relay must use https", s)
	}
	return u.Scheme + "://" + u.Host, nil
}
