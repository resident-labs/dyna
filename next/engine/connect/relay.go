package connect

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

type machineClaims struct {
	Iss string `json:"iss"`
	Aud string `json:"aud"`
	Htm string `json:"htm"`
	Htu string `json:"htu"`
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
	Jti string `json:"jti"`
	Bh  string `json:"bh"`
}

const DefaultRelay = "https://connect.residentlabs.dev"

type RelayError struct{ Status int }

func (e *RelayError) Error() string { return fmt.Sprintf("relay returned HTTP %d", e.Status) }

type Relay struct {
	Base   string
	Key    ed25519.PrivateKey
	Client *http.Client
	Now    func() time.Time
}

func (r *Relay) client() *http.Client {
	client := http.Client{Timeout: 15 * time.Second}
	if r.Client != nil {
		client = *r.Client
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}

type LinkStart struct {
	ID          string `json:"id"`
	UserCode    string `json:"userCode"`
	URL         string `json:"url"`
	Fingerprint string `json:"fingerprint"`
	Expires     int64  `json:"expires"`
}

type LinkPoll struct {
	Status  string      `json:"status"`
	Machine MachineInfo `json:"machine"`
}

type startBody struct {
	Pubkey   string `json:"pubkey"`
	PollHash string `json:"pollHash"`
	Name     string `json:"name"`
	AppPort  int    `json:"appPort"`
	CardPort int    `json:"cardPort"`
}
type pollBody struct {
	ID     string `json:"id"`
	Secret string `json:"secret"`
}
type machineBody struct {
	Machine string `json:"machine"`
}
type originBody struct {
	Machine  string `json:"machine"`
	AppPort  int    `json:"appPort"`
	CardPort int    `json:"cardPort"`
}

// call handles arbitrary typed relay messages; their concrete types define the wire schema.
func call[I, O any](ctx context.Context, r *Relay, issuer, path string, body I, out *O) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	var nonce [18]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return err
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	claims := machineClaims{issuer, r.Base, "POST", path, now.Unix(), now.Unix() + 60, b64.EncodeToString(nonce[:]), hashToken(string(b))}
	header, _ := json.Marshal(jwsHeader{Alg: "EdDSA", Typ: "dyna-machine+jws"})
	payload, err := json.Marshal(claims)
	if err != nil {
		return err
	}
	input := b64.EncodeToString(header) + "." + b64.EncodeToString(payload)
	token := input + "." + b64.EncodeToString(ed25519.Sign(r.Key, []byte(input)))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.Base+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "DynaMachine "+token)
	client := r.client()
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return &RelayError{Status: res.StatusCode}
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(out)
}

func (r *Relay) StartLink(ctx context.Context, name, secret string, appPort, cardPort int) (LinkStart, error) {
	var out LinkStart
	err := call(ctx, r, "link", "/v1/link/start", startBody{b64.EncodeToString(r.Key.Public().(ed25519.PublicKey)), hashToken(secret), name, appPort, cardPort}, &out)
	return out, err
}
func (r *Relay) PollLink(ctx context.Context, id, secret string) (LinkPoll, error) {
	var out LinkPoll
	err := call(ctx, r, id, "/v1/link/poll", pollBody{id, secret}, &out)
	return out, err
}
func (r *Relay) TunnelToken(ctx context.Context, machine string) (string, error) {
	var out struct {
		Token string `json:"token"`
	}
	err := call(ctx, r, machine, "/v1/machine/tunnel-token", machineBody{machine}, &out)
	if err == nil && out.Token == "" {
		err = errors.New("empty tunnel token")
	}
	return out.Token, err
}
func (r *Relay) ReportOrigin(ctx context.Context, machine string, appPort, cardPort int) error {
	var out struct {
		OK bool `json:"ok"`
	}
	return call(ctx, r, machine, "/v1/machine/origin", originBody{machine, appPort, cardPort}, &out)
}

func (r *Relay) Unlink(ctx context.Context, machine string) error {
	var out struct {
		OK bool `json:"ok"`
	}
	return call(ctx, r, machine, "/v1/machine/unlink", machineBody{machine}, &out)
}

type Grant struct {
	Active bool  `json:"active"`
	Epoch  int64 `json:"epoch"`
}

func (r *Relay) CheckGrant(ctx context.Context, machine, user, device string) (Grant, error) {
	var out Grant
	body := struct {
		Machine string `json:"machine"`
		User    string `json:"user"`
		Device  string `json:"dev"`
	}{machine, user, device}
	err := call(ctx, r, machine, "/v1/machine/grant", body, &out)
	return out, err
}

// FetchKeys retrieves the relay public keys without following redirects.
func (r *Relay) FetchKeys(ctx context.Context) (KeySet, error) {
	var keys KeySet
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.Base+"/.well-known/jwks.json", nil)
	if err != nil {
		return keys, err
	}
	client := r.client()
	res, err := client.Do(req)
	if err != nil {
		return keys, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return keys, fmt.Errorf("relay key set: HTTP %d", res.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&keys); err != nil {
		return keys, err
	}
	if len(keys.Keys) == 0 {
		return keys, errors.New("relay key set is empty")
	}
	return keys, nil
}

// CheckRelayURL accepts HTTPS origins and loopback HTTP for test relays.
func CheckRelayURL(s string) (string, error) {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("invalid relay origin")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost")) {
		return "", errors.New("relay must use HTTPS")
	}
	return u.Scheme + "://" + u.Host, nil
}
