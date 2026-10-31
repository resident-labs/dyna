package model

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

var errConnect = errors.New("account connection unavailable")

type connectAttempt struct {
	provider      contract.AccountProvider
	proxyProvider string
	label         string
	state         string
	expires       time.Time
	generation    uint64
	status        contract.ConnectStatus
	account       contract.AccountID
	callbackSent  bool
}

type Connections struct {
	proxy    *Proxy
	key      string
	store    *store.Store
	now      func() time.Time
	mu       sync.Mutex
	attempts map[string]*connectAttempt
}

func (p *Proxy) Connections(key string, st *store.Store, now func() time.Time) *Connections {
	return &Connections{proxy: p, key: key, store: st, now: now, attempts: make(map[string]*connectAttempt)}
}

type oauthResponse struct {
	URL       string `json:"url"`
	State     string `json:"state"`
	UserCode  string `json:"user_code"`
	ExpiresIn int    `json:"expires_in"`
	Status    string `json:"status"`
	AuthID    string `json:"auth_id"`
	Saved     bool   `json:"saved"`
	Adopted   bool   `json:"adopted"`
}

func (c *Connections) request(ctx context.Context, method, path string, body []byte, out *oauthResponse) error {
	req, err := http.NewRequestWithContext(ctx, method, "http://cliproxy/v8/management/"+path, bytes.NewReader(body))
	if err != nil {
		return errConnect
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.proxy.client.Do(req)
	if err != nil {
		return errConnect
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return errConnect
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(out); err != nil {
		return errConnect
	}
	return nil
}

func (c *Connections) Start(ctx context.Context, req contract.AccountsConnectRequest) (contract.AccountsConnectResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, entry := range c.attempts {
		if !c.now().Before(entry.expires.Add(time.Hour)) {
			delete(c.attempts, id)
		}
	}
	if len(c.attempts) >= 128 {
		return contract.AccountsConnectResponse{}, errConnect
	}
	var provider string
	flow := contract.ConnectFlowCallback
	switch req.Provider {
	case contract.ProviderAnthropic:
		provider = "claude"
	case contract.ProviderOpenAI:
		provider = "codex"
	case contract.ProviderKimi:
		provider, flow = "kimi", contract.ConnectFlowDevice
	case contract.ProviderXAI:
		provider, flow = "xai", contract.ConnectFlowDevice
	default:
		return contract.AccountsConnectResponse{}, errConnect
	}
	var result oauthResponse
	generation := c.proxy.generation.Load()
	if err := c.request(ctx, http.MethodGet, "oauth/auth-url?provider="+provider, nil, &result); err != nil {
		return contract.AccountsConnectResponse{}, err
	}
	link, err := url.Parse(result.URL)
	if err != nil || link.Scheme != "https" || link.Host == "" || result.State == "" || generation != c.proxy.generation.Load() {
		return contract.AccountsConnectResponse{}, errConnect
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return contract.AccountsConnectResponse{}, errConnect
	}
	seconds := result.ExpiresIn
	if seconds <= 0 || seconds > 600 {
		seconds = 300
	}
	expires := c.now().Add(time.Duration(seconds) * time.Second)
	attempt := hex.EncodeToString(id[:])
	c.attempts[attempt] = &connectAttempt{provider: req.Provider, proxyProvider: provider, label: req.Label, state: result.State, expires: expires, generation: generation, status: contract.ConnectPending}
	return contract.AccountsConnectResponse{Attempt: attempt, Flow: flow, Link: result.URL, Code: result.UserCode, ExpiresAt: expires}, nil
}

func (c *Connections) Callback(ctx context.Context, req contract.AccountsConnectCallbackRequest) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	a := c.attempts[req.Attempt]
	if a == nil || a.status != contract.ConnectPending || a.callbackSent || !c.now().Before(a.expires) || a.generation != c.proxy.generation.Load() || (a.provider != contract.ProviderAnthropic && a.provider != contract.ProviderOpenAI) {
		return errConnect
	}
	u, err := url.Parse(req.RedirectURL)
	if err != nil || len(u.Query()["state"]) != 1 || u.Query().Get("state") != a.state {
		return errConnect
	}
	payload, err := json.Marshal(struct {
		Provider    string `json:"provider"`
		RedirectURL string `json:"redirect_url"`
	}{a.proxyProvider, req.RedirectURL})
	if err != nil {
		return errConnect
	}
	a.callbackSent = true
	var result oauthResponse
	return c.request(ctx, http.MethodPost, "oauth/callback", payload, &result)
}

func (c *Connections) Status(ctx context.Context, req contract.AccountsConnectStatusRequest) (contract.AccountsConnectStatusResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a := c.attempts[req.Attempt]
	if a == nil {
		return contract.AccountsConnectStatusResponse{Status: contract.ConnectFailed}, nil
	}
	if a.status == contract.ConnectConnected || a.status == contract.ConnectFailed {
		return contract.AccountsConnectStatusResponse{Status: a.status, AccountID: a.account}, nil
	}
	if !c.now().Before(a.expires) {
		return contract.AccountsConnectStatusResponse{Status: contract.ConnectExpired}, nil
	}
	if a.generation != c.proxy.generation.Load() {
		a.status = contract.ConnectFailed
		return contract.AccountsConnectStatusResponse{Status: a.status}, nil
	}
	var result oauthResponse
	if err := c.request(ctx, http.MethodGet, "oauth/status?state="+url.QueryEscape(a.state), nil, &result); err != nil {
		a.status = contract.ConnectFailed
		return contract.AccountsConnectStatusResponse{Status: a.status}, nil
	}
	if a.generation != c.proxy.generation.Load() {
		a.status = contract.ConnectFailed
		return contract.AccountsConnectStatusResponse{Status: a.status}, nil
	}
	switch result.Status {
	case "wait":
		return contract.AccountsConnectStatusResponse{Status: contract.ConnectPending}, nil
	case "ok":
		if result.AuthID == "" || !result.Saved {
			a.status = contract.ConnectFailed
			break
		}
		if !result.Adopted {
			return contract.AccountsConnectStatusResponse{Status: contract.ConnectPending}, nil
		}
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return contract.AccountsConnectStatusResponse{}, errConnect
		}
		id := contract.AccountID(hex.EncodeToString(raw[:]))
		if err := c.store.BindAccount(ctx, id, result.AuthID, a.provider, a.label); err != nil {
			return contract.AccountsConnectStatusResponse{}, errConnect
		}
		a.account, a.status = id, contract.ConnectConnected
	default:
		a.status = contract.ConnectFailed
	}
	return contract.AccountsConnectStatusResponse{Status: a.status, AccountID: a.account}, nil
}
