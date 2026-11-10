package prs

import (
	"context"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// Accounts keeps independent caches, quotas and viewer identities for each room
// credential. A changed credential replaces the old client on the next lookup.
type Accounts struct {
	Resolve   func(context.Context, contract.RepoName) (string, string, error)
	Endpoint  *url.URL
	Transport http.RoundTripper
	Now       func() time.Time
	mu        sync.Mutex
	clients   map[string]*Client
}

func (a *Accounts) Repo(ctx context.Context, repo contract.RepoName) (*Client, error) {
	token, login, err := a.Resolve(ctx, repo)
	if err != nil {
		return nil, err
	}
	return a.Client(token, login), nil
}

func (a *Accounts) Client(token, login string) *Client {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.clients == nil {
		a.clients = map[string]*Client{}
	}
	c := a.clients[login]
	if c == nil || c.Token != token {
		c = &Client{Token: token, Endpoint: a.Endpoint, Transport: a.Transport, Now: a.Now}
		a.clients[login] = c
	}
	return c
}
