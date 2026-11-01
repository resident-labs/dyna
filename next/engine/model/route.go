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
)

const maxRequestBytes = 32 << 20

// RouteError is a typed model route refusal.
type RouteError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func unixClient(socket string) *http.Client {
	return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}, Timeout: 30 * time.Second}
}

func handler(client *http.Client, clientKey, sessionID, namespace string, allowedModels, eligibleIDs []string, entry pool) http.Handler {
	allowed := make(map[string]struct{}, len(allowedModels))
	for _, name := range allowedModels {
		allowed[name] = struct{}{}
	}
	proxy := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "http", Host: "cliproxy"})
	proxy.Transport = client.Transport
	proxy.FlushInterval = -1
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/model/v1/messages" && r.URL.Path != "/model/v1/chat/completions" && r.URL.Path != "/model/v1/responses" {
			refuse(w, http.StatusNotFound, "unsupported_route", "The model endpoint is not supported.")
			return
		}
		if r.Method != http.MethodPost {
			refuse(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only POST is supported.")
			return
		}
		if namespace == "" {
			refuse(w, http.StatusForbidden, "model_forbidden", "The profile does not allow this model.")
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes+1))
		if err != nil {
			refuse(w, http.StatusBadRequest, "invalid_request", "Could not read request.")
			return
		}
		if len(body) > maxRequestBytes {
			refuse(w, http.StatusRequestEntityTooLarge, "request_too_large", "Model request exceeds 32 MiB.")
			return
		}
		decoder := json.NewDecoder(bytes.NewReader(body))
		token, err := decoder.Token()
		if err != nil || token != json.Delim('{') {
			refuse(w, http.StatusBadRequest, "invalid_request", "A model is required.")
			return
		}
		var model string
		var valueStart, valueEnd int64
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
				valueEnd = decoder.InputOffset()
				valueStart = valueEnd - int64(len(value))
				seenModel = true
			}
		}
		if valid {
			_, err = decoder.Token()
			valid = err == nil && decoder.Decode(new(json.RawMessage)) == io.EOF
		}
		if !valid || model == "" {
			refuse(w, http.StatusBadRequest, "invalid_request", "A model is required.")
			return
		}
		if _, ok := allowed[model]; !ok {
			refuse(w, http.StatusForbidden, "model_forbidden", "The profile does not allow this model.")
			return
		}
		if len(eligibleIDs) == 0 {
			refuse(w, http.StatusForbidden, "account_forbidden", "No account is available to this room.")
			return
		}
		encoded, _ := json.Marshal(namespace + "/" + entry.modelID(model))
		body = append(append(append([]byte(nil), body[:valueStart]...), encoded...), body[valueEnd:]...)
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/model")
		r.URL.RawPath = ""
		query := r.URL.Query()
		for _, name := range []string{"key", "api_key", "access_token"} {
			query.Del(name)
		}
		r.URL.RawQuery = query.Encode()
		// Drop Connection before adding the policy: ReverseProxy removes headers
		// named by Connection, which otherwise lets a box strip the policy.
		// Do not forward box-controlled credentials or affinity headers.
		for _, name := range []string{"Authorization", "X-Api-Key", "Proxy-Authorization", "X-Goog-Api-Key", "Api-Key", "X-Auth-Token", "X-Claude-Code-Session-Id", "Session-Id", "Session_id", "X-Session-Id", "X-Session-Affinity", "X-Client-Request-Id", "X-Dyna-Eligible-Auth", "Connection"} {
			r.Header.Del(name)
		}
		encodedIDs := make([]string, len(eligibleIDs))
		for i, id := range eligibleIDs {
			encodedIDs[i] = base64.RawURLEncoding.EncodeToString([]byte(id))
		}
		r.Header.Set("X-Dyna-Eligible-Auth", strings.Join(encodedIDs, ","))
		r.Header.Set("Authorization", "Bearer "+clientKey)
		r.Header.Set("X-Api-Key", clientKey)
		r.Header.Set("X-Claude-Code-Session-Id", sessionID)
		proxy.ServeHTTP(w, r)
	})
}

func refuse(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(RouteError{Code: code, Message: message})
}
