package runtime

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// antigravityAuthorization accepts only Google's code flow with an explicit
// loopback listener. The callback must match this flow's port and OAuth state.
func antigravityAuthorization(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 16<<10 || u.Scheme != "https" || u.Host != "accounts.google.com" ||
		u.User != nil || u.Path != "/o/oauth2/v2/auth" || u.Fragment != "" {
		return nil, errors.New("Invalid Google sign-in link.")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q["redirect_uri"]) != 1 || len(q["state"]) != 1 || q.Get("state") == "" ||
		len(q["response_type"]) != 1 || q.Get("response_type") != "code" {
		return nil, errors.New("Invalid Google sign-in link.")
	}
	redirect, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || !antigravityLoopback(redirect) || redirect.RawQuery != "" {
		return nil, errors.New("Invalid Google sign-in link.")
	}
	return u, nil
}

func antigravityLoopback(u *url.URL) bool {
	if u.Scheme != "http" || u.User != nil || u.Opaque != "" || u.Path != "/" ||
		u.RawPath != "" || u.Fragment != "" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
		return false
	}
	port, err := strconv.Atoi(u.Port())
	return err == nil && port > 0 && port <= 65535 && strconv.Itoa(port) == u.Port() &&
		u.Host == net.JoinHostPort(u.Hostname(), u.Port())
}

func parseAntigravitySignIn(st *SignInState, line string) {
	for _, field := range strings.Fields(line) {
		if _, err := antigravityAuthorization(field); err != nil {
			continue
		}
		st.URL = field
		st.Phase = signInWaiting
		st.Message = "Open the Google link. Then paste the final redirect URL from the page that could not load."
		return
	}
}

// antigravityCallbackURL validates the pasted response before any request.
// Localhost is accepted as input, but the request uses the numeric loopback
// address so DNS cannot choose a different destination.
func antigravityCallbackURL(st SignInState, raw string) (*url.URL, error) {
	invalid := errors.New("Paste the complete redirect URL for this Google sign-in.")
	authorization, err := antigravityAuthorization(st.URL)
	if err != nil {
		return nil, invalid
	}
	pending := authorization.Query()
	expected, _ := url.Parse(pending.Get("redirect_uri"))
	callback, err := url.Parse(raw)
	if err != nil || len(raw) > 4096 || !antigravityLoopback(callback) || callback.Port() != expected.Port() {
		return nil, invalid
	}
	q, err := url.ParseQuery(callback.RawQuery)
	if err != nil || len(q["state"]) != 1 || q.Get("state") != pending.Get("state") {
		return nil, invalid
	}
	code := len(q["code"]) == 1 && q.Get("code") != "" && len(q["error"]) == 0
	denied := len(q["error"]) == 1 && q.Get("error") != "" && len(q["code"]) == 0
	if (!code && !denied) || len(q["iss"]) > 1 || (len(q["iss"]) == 1 && q.Get("iss") != "https://accounts.google.com") {
		return nil, invalid
	}
	callback.Host = net.JoinHostPort("127.0.0.1", expected.Port())
	return callback, nil
}

func respondAntigravitySignIn(ctx context.Context, st SignInState, text string) error {
	// A fresh transport has no proxy and cannot reuse another flow's connection.
	return forwardAntigravityCallback(ctx, st, text, &http.Transport{DisableKeepAlives: true})
}

func forwardAntigravityCallback(ctx context.Context, st SignInState, text string, transport http.RoundTripper) error {
	callback, err := antigravityCallbackURL(st, text)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, callback.String(), nil)
	if err != nil {
		return errors.New("The sign-in response could not be sent. Try again.")
	}
	client := &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("The sign-in response could not be sent. Try again.")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return errors.New("The sign-in response was refused. Start sign-in again.")
	}
	return nil
}
