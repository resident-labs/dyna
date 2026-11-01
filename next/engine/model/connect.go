package model

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/resident-labs/dyna/contract"
)

var errConnect = errors.New("account connection unavailable")

// ErrReconnectUnavailable means the saved credential has no usable identity.
var ErrReconnectUnavailable = errors.New("account reconnect unavailable")

// ErrCompletionRejected means the attempt cannot bind and its staged credential is safe to discard.
var ErrCompletionRejected = errors.New("account completion rejected")

type connectAttempt struct {
	provider      contract.AccountProvider
	proxyProvider string
	flow          contract.ConnectFlow
	label         string
	state         string
	expires       time.Time
	generation    uint64
	status        contract.ConnectStatus
	account       contract.AccountID
	proxyID       string
	callbackSent  bool
	saved         bool
	reason        contract.ConnectFailureReason
	ready         chan struct{}
}

type Connections struct {
	proxy      *Proxy
	management managementClient
	store      AccountStore
	now        func() time.Time
	// Lock order: removeMu -> proxy credential gate -> manager.mu -> mu -> store.
	// Never call the proxy while holding mu.
	mu       sync.Mutex
	attempts map[string]*connectAttempt
}

func (p *Proxy) Connections(key string, st AccountStore, now func() time.Time) *Connections {
	return &Connections{proxy: p, management: managementClient{client: p.client, key: key}, store: st, now: now, attempts: make(map[string]*connectAttempt)}
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
	data, err := c.management.request(ctx, method, "/v8/management/"+path, body)
	if err != nil || json.Unmarshal(data, out) != nil {
		return errConnect
	}
	return nil
}

func (c *Connections) Start(ctx context.Context, req contract.AccountsConnectRequest) (contract.AccountsConnectResponse, error) {
	return c.start(ctx, req, "", "")
}

func (c *Connections) Reconnect(ctx context.Context, account contract.AccountID, proxyID string, provider contract.AccountProvider) (contract.AccountsConnectResponse, error) {
	records, err := c.store.ListProxyAuths(ctx)
	if err != nil {
		return contract.AccountsConnectResponse{}, errConnect
	}
	for _, record := range records {
		if record.ID == proxyID {
			if _, value := rawCredentialIdentity(record.Record); value != "" {
				return c.start(ctx, contract.AccountsConnectRequest{Provider: provider}, account, proxyID)
			}
			break
		}
	}
	return contract.AccountsConnectResponse{}, ErrReconnectUnavailable
}

func (c *Connections) start(ctx context.Context, req contract.AccountsConnectRequest, account contract.AccountID, proxyID string) (contract.AccountsConnectResponse, error) {
	c.mu.Lock()
	for id, entry := range c.attempts {
		if !(entry.saved && entry.status == contract.ConnectPending) && !c.now().Before(entry.expires.Add(time.Hour)) {
			delete(c.attempts, id)
		}
	}
	if len(c.attempts) >= 128 {
		c.mu.Unlock()
		return contract.AccountsConnectResponse{}, errConnect
	}
	c.mu.Unlock()
	entry, ok := c.proxy.pools["plan_"+string(req.Provider)]
	if !ok {
		return contract.AccountsConnectResponse{}, errConnect
	}
	provider, flow := entry.proxy, entry.info.Flows[0]
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return contract.AccountsConnectResponse{}, errConnect
	}
	attempt := hex.EncodeToString(id[:])
	generation := c.proxy.generation.Load()
	a := &connectAttempt{provider: req.Provider, proxyProvider: provider, flow: flow, label: req.Label, expires: c.now().Add(time.Hour), generation: generation, status: contract.ConnectPending, account: account, proxyID: proxyID, ready: make(chan struct{})}
	c.mu.Lock()
	if len(c.attempts) >= 128 {
		c.mu.Unlock()
		return contract.AccountsConnectResponse{}, errConnect
	}
	if account != "" {
		for _, entry := range c.attempts {
			if entry.account == account && entry.generation == generation && ((entry.status == contract.ConnectPending && c.now().Before(entry.expires)) || (entry.saved && entry.status != contract.ConnectConnected)) {
				c.mu.Unlock()
				return contract.AccountsConnectResponse{}, errConnect
			}
		}
	}
	c.attempts[attempt] = a
	c.mu.Unlock()
	var result oauthResponse
	data, err := c.management.requestAttempt(ctx, http.MethodGet, "/v8/management/oauth/auth-url?provider="+provider, nil, attempt)
	if err != nil || json.Unmarshal(data, &result) != nil {
		c.mu.Lock()
		delete(c.attempts, attempt)
		close(a.ready)
		c.mu.Unlock()
		return contract.AccountsConnectResponse{}, errConnect
	}
	link, err := url.Parse(result.URL)
	if err != nil || link.Scheme != "https" || link.Host == "" || result.State == "" || generation != c.proxy.generation.Load() {
		c.mu.Lock()
		delete(c.attempts, attempt)
		close(a.ready)
		c.mu.Unlock()
		return contract.AccountsConnectResponse{}, errConnect
	}
	seconds := result.ExpiresIn
	if seconds <= 0 || seconds > 600 {
		seconds = 300
	}
	expires := c.now().Add(time.Duration(seconds) * time.Second)
	c.mu.Lock()
	defer c.mu.Unlock()
	if generation != c.proxy.generation.Load() || a.status != contract.ConnectPending {
		delete(c.attempts, attempt)
		close(a.ready)
		return contract.AccountsConnectResponse{}, errConnect
	}
	a.state, a.expires = result.State, expires
	close(a.ready)
	return contract.AccountsConnectResponse{Attempt: attempt, Flow: flow, Link: result.URL, Code: result.UserCode, ExpiresAt: expires}, nil
}

// CancelReconnect prevents unsaved attempts from replacing a removed account.
func (c *Connections) CancelReconnect(id contract.AccountID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, a := range c.attempts {
		if a.account == id && !a.saved && a.status == contract.ConnectPending {
			a.status = contract.ConnectFailed
		}
	}
}

func (c *Connections) Callback(ctx context.Context, req contract.AccountsConnectCallbackRequest) error {
	c.mu.Lock()
	a := c.attempts[req.Attempt]
	if a == nil || a.status != contract.ConnectPending || a.callbackSent || !c.now().Before(a.expires) || a.generation != c.proxy.generation.Load() || a.flow != contract.ConnectFlowCallback {
		c.mu.Unlock()
		return errConnect
	}
	u, err := url.Parse(req.RedirectURL)
	if err != nil || len(u.Query()["state"]) != 1 || u.Query().Get("state") != a.state {
		c.mu.Unlock()
		return errConnect
	}
	payload, err := json.Marshal(struct {
		Provider    string `json:"provider"`
		RedirectURL string `json:"redirect_url"`
	}{a.proxyProvider, req.RedirectURL})
	if err != nil {
		c.mu.Unlock()
		return errConnect
	}
	a.callbackSent = true
	c.mu.Unlock()
	var result oauthResponse
	return c.request(ctx, http.MethodPost, "oauth/callback", payload, &result)
}

func (c *Connections) Status(ctx context.Context, req contract.AccountsConnectStatusRequest, commit func(func() error) error) (contract.AccountsConnectStatusResponse, error) {
	return c.status(ctx, req, "", commit)
}

// Complete handles a proxy completion even when no browser requests status.
func (c *Connections) Complete(ctx context.Context, completed OAuthCompletion, commit func(func() error) error) error {
	c.mu.Lock()
	var attempt string
	for id, entry := range c.attempts {
		if entry.state == completed.State {
			attempt = id
			break
		}
	}
	var saved bool
	if attempt != "" {
		saved = c.attempts[attempt].saved
	}
	c.mu.Unlock()
	if attempt == "" {
		return ErrCompletionRejected
	}
	status, err := c.status(ctx, contract.AccountsConnectStatusRequest{Attempt: attempt}, completed.AuthID, commit)
	if err != nil {
		return err
	}
	if status.Status != contract.ConnectConnected {
		if saved {
			return errConnect
		}
		return ErrCompletionRejected
	}
	return nil
}

func (c *Connections) status(ctx context.Context, req contract.AccountsConnectStatusRequest, expected string, commit func(func() error) error) (contract.AccountsConnectStatusResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a := c.attempts[req.Attempt]
	if a == nil {
		return contract.AccountsConnectStatusResponse{Status: contract.ConnectFailed}, nil
	}
	if expected != "" && a.proxyID != "" && a.proxyID != expected {
		if a.saved {
			return contract.AccountsConnectStatusResponse{}, errConnect
		}
		return contract.AccountsConnectStatusResponse{}, ErrCompletionRejected
	}
	if a.status == contract.ConnectConnected || a.status == contract.ConnectFailed {
		return contract.AccountsConnectStatusResponse{Status: a.status, AccountID: a.account, Reason: a.reason}, nil
	}
	if !a.saved && !c.now().Before(a.expires) {
		if expected != "" {
			a.status = contract.ConnectFailed
		}
		return contract.AccountsConnectStatusResponse{Status: contract.ConnectExpired}, nil
	}
	if a.generation != c.proxy.generation.Load() {
		a.status = contract.ConnectFailed
		return contract.AccountsConnectStatusResponse{Status: a.status}, nil
	}
	state := a.state
	c.mu.Unlock()
	var result oauthResponse
	requestErr := c.request(ctx, http.MethodGet, "oauth/status?state="+url.QueryEscape(state), nil, &result)
	c.mu.Lock()
	if c.attempts[req.Attempt] != a || a.status != contract.ConnectPending {
		return contract.AccountsConnectStatusResponse{Status: a.status, AccountID: a.account}, nil
	}
	if !a.saved && !c.now().Before(a.expires) {
		return contract.AccountsConnectStatusResponse{Status: contract.ConnectExpired}, nil
	}
	if requestErr != nil {
		if a.saved {
			return contract.AccountsConnectStatusResponse{}, errConnect
		}
		a.status = contract.ConnectFailed
		return contract.AccountsConnectStatusResponse{Status: a.status}, nil
	}
	if a.generation != c.proxy.generation.Load() {
		a.status = contract.ConnectFailed
		return contract.AccountsConnectStatusResponse{Status: a.status}, nil
	}
	switch result.Status {
	case "wait":
		if expected != "" {
			a.status = contract.ConnectFailed
		}
		return contract.AccountsConnectStatusResponse{Status: contract.ConnectPending}, nil
	case "ok":
		if result.AuthID == "" || !result.Saved || (expected != "" && result.AuthID != expected) || (a.account != "" && result.AuthID != a.proxyID) {
			a.status = contract.ConnectFailed
			break
		}
		if !result.Adopted {
			if a.saved {
				a.status, a.reason = contract.ConnectFailed, contract.ConnectAdoptionFailed
				return contract.AccountsConnectStatusResponse{Status: a.status, AccountID: a.account, Reason: a.reason}, nil
			}
			if expected != "" {
				a.status = contract.ConnectFailed
			}
			return contract.AccountsConnectStatusResponse{Status: contract.ConnectPending}, nil
		}
		if a.account != "" {
			bindings, err := c.store.Accounts(ctx)
			if err != nil {
				return contract.AccountsConnectStatusResponse{}, errConnect
			}
			found := false
			for _, binding := range bindings {
				if binding.Account.ID == a.account && binding.ProxyID == a.proxyID {
					found = true
					break
				}
			}
			if !found {
				return contract.AccountsConnectStatusResponse{}, errConnect
			}
			if !a.saved {
				a.status = contract.ConnectFailed
				break
			}
			a.status = contract.ConnectConnected
			break
		}
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return contract.AccountsConnectStatusResponse{}, errConnect
		}
		id := contract.AccountID(hex.EncodeToString(raw[:]))
		if err := commit(func() error { return c.store.BindAccount(ctx, id, result.AuthID, a.provider, a.label) }); err != nil {
			return contract.AccountsConnectStatusResponse{}, errConnect
		}
		a.account, a.proxyID, a.status = id, result.AuthID, contract.ConnectConnected
	default:
		a.status = contract.ConnectFailed
	}
	return contract.AccountsConnectStatusResponse{Status: a.status, AccountID: a.account}, nil
}
