// Package model runs the model proxy and enforces session model permissions.
package model

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
)

const maxRequestBytes = 32 << 20

// RouteError is a typed model route refusal.
type RouteError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Handler returns a route bound to one session and its allowed models.
// socket is the proxy listener; clientKey never leaves the engine. pinnedID is the
// credential the owner chose for this session, or empty for Automatic. It comes from the
// store, never from the request. crew says the session's workers each keep their own account.
func Handler(socket string, clientKey, sessionID string, allowedModels, eligibleIDs []string, pinnedID string, crew bool) http.Handler {
	return handler(unixClient(socket), clientKey, sessionID, allowedModels, eligibleIDs, pinnedID, crew)
}

func unixClient(socket string) *http.Client {
	return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}, Timeout: 30 * time.Second}
}

func handler(client *http.Client, clientKey, sessionID string, allowedModels, eligibleIDs []string, pinnedID string, crew bool) http.Handler {
	allowed := make(map[string]struct{}, len(allowedModels))
	for _, name := range allowedModels {
		allowed[name] = struct{}{}
	}
	allow := func(model string) bool {
		_, ok := allowed[model]
		return ok
	}
	return serve(client, clientKey, sessionID, boxRoutes, allow, eligibleIDs, pinnedID, crew)
}

// boxRoutes maps each path to its method. A box needs only what Pi sends.
var boxRoutes = map[string]string{
	"/model/v1/messages":         http.MethodPost,
	"/model/v1/chat/completions": http.MethodPost,
	"/model/v1/responses":        http.MethodPost,
}

// serve applies the policy every caller shares: it checks the model in a POST, then
// replaces the caller's credentials and affinity headers with the engine's.
func serve(client *http.Client, clientKey, affinityID string, routes map[string]string, allow func(string) bool, eligibleIDs []string, pinnedID string, crew bool) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "http", Host: "cliproxy"})
	proxy.Transport = client.Transport
	proxy.FlushInterval = -1
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, ok := routes[r.URL.Path]
		if !ok {
			refuse(w, http.StatusNotFound, "unsupported_route", "The model endpoint is not supported.")
			return
		}
		if r.Method != method {
			refuse(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only "+method+" is supported.")
			return
		}
		if method == http.MethodPost && !checkModel(w, r, allow) {
			return
		}
		if len(eligibleIDs) == 0 {
			refuse(w, http.StatusForbidden, "account_forbidden", "No account is available to this room.")
			return
		}
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/model")
		r.URL.RawPath = ""
		query := r.URL.Query()
		for _, name := range []string{"key", "api_key", "access_token"} {
			query.Del(name)
		}
		r.URL.RawQuery = query.Encode()
		affinity := affinityID
		if worker := r.Header.Get(contract.WorkerHeader); crew && worker != "" {
			affinity += "/" + worker
		}
		// Drop Connection before adding the policy: ReverseProxy removes headers
		// named by Connection, which otherwise lets a box strip the policy.
		// Do not forward box-controlled credentials or affinity headers.
		for _, name := range []string{"Authorization", "X-Api-Key", "Proxy-Authorization", "X-Goog-Api-Key", "Api-Key", "X-Auth-Token", "X-Claude-Code-Session-Id", "Session-Id", "Session_id", "X-Session-Id", "X-Session-Affinity", "X-Client-Request-Id", "X-Dyna-Eligible-Auth", "X-Dyna-Pinned-Auth", contract.WorkerHeader, "Connection"} {
			r.Header.Del(name)
		}
		encodedIDs := make([]string, len(eligibleIDs))
		for i, id := range eligibleIDs {
			encodedIDs[i] = base64.RawURLEncoding.EncodeToString([]byte(id))
		}
		r.Header.Set("X-Dyna-Eligible-Auth", strings.Join(encodedIDs, ","))
		if pinnedID != "" {
			r.Header.Set("X-Dyna-Pinned-Auth", base64.RawURLEncoding.EncodeToString([]byte(pinnedID)))
		}
		r.Header.Set("Authorization", "Bearer "+clientKey)
		r.Header.Set("X-Api-Key", clientKey)
		r.Header.Set("X-Claude-Code-Session-Id", affinity)
		proxy.ServeHTTP(w, r)
	})
}

// checkModel reads the body, refuses a request whose model the caller may not use,
// and leaves the body ready to forward.
func checkModel(w http.ResponseWriter, r *http.Request, allow func(string) bool) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes+1))
	if err != nil {
		refuse(w, http.StatusBadRequest, "invalid_request", "Could not read request.")
		return false
	}
	if len(body) > maxRequestBytes {
		refuse(w, http.StatusRequestEntityTooLarge, "request_too_large", "Model request exceeds 32 MiB.")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		refuse(w, http.StatusBadRequest, "invalid_request", "A model is required.")
		return false
	}
	var model string
	seenModel := false
	valid := true
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			valid = false
			break
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			valid = false
			break
		}
		if key == "model" {
			if seenModel || json.Unmarshal(value, &model) != nil {
				valid = false
				break
			}
			seenModel = true
		}
	}
	if valid {
		_, err = decoder.Token()
		valid = err == nil && decoder.Decode(new(json.RawMessage)) == io.EOF
	}
	if !valid || model == "" {
		refuse(w, http.StatusBadRequest, "invalid_request", "A model is required.")
		return false
	}
	if !allow(model) {
		refuse(w, http.StatusForbidden, "model_forbidden", "The profile does not allow this model.")
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	return true
}

func refuse(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(RouteError{Code: code, Message: message})
}
