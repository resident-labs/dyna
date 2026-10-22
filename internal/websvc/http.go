package websvc

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"dyna-agent/internal/websvc/devicestore"
)

// JSONReply writes v as JSON with no caching.
func JSONReply(w http.ResponseWriter, code int, v any) { jsonReply(w, code, v) }

// JSONError writes {"error": msg}.
func JSONError(w http.ResponseWriter, code int, msg string) { jsonError(w, code, msg) }

// CheckCSRF is the POST check every API route uses.
func CheckCSRF(r *http.Request, rt Route) error { return csrf(r, rt) }

func jsonReply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// gzipMin is the smallest reply worth compressing. A room's feed is large,
// repeats words, and compresses to a fraction; a phone pays for every byte.
const gzipMin = 2048

// gzipJSONReply is jsonReply that compresses a large reply for a client that
// accepts gzip. With revalidate it also sends an ETag of the reply, and answers
// 304 with no body (and no compression) when the client already holds that reply.
func gzipJSONReply(w http.ResponseWriter, r *http.Request, code int, v any, revalidate bool) {
	b, err := json.Marshal(v)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	b = append(b, '\n')
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Add("Vary", "Accept-Encoding")
	if revalidate && code == http.StatusOK {
		sum := sha256.Sum256(b)
		tag := `"` + hex.EncodeToString(sum[:16]) + `"`
		h.Set("ETag", tag)
		if r.Header.Get("If-None-Match") == tag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	if len(b) >= gzipMin && acceptsGzip(r) {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		_, _ = zw.Write(b)
		if zw.Close() == nil {
			h.Set("Content-Encoding", "gzip")
			b = buf.Bytes()
		}
	}
	h.Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(code)
	_, _ = w.Write(b)
}

func acceptsGzip(r *http.Request) bool {
	wildcard := false
	for _, part := range strings.Split(strings.Join(r.Header.Values("Accept-Encoding"), ","), ",") {
		fields := strings.Split(part, ";")
		name := strings.TrimSpace(fields[0])
		if !strings.EqualFold(name, "gzip") && name != "*" {
			continue
		}
		quality := 1.0
		for _, param := range fields[1:] {
			key, value, _ := strings.Cut(param, "=")
			if strings.EqualFold(strings.TrimSpace(key), "q") {
				q, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
				if err != nil || !(q >= 0 && q <= 1) {
					quality = 0
				} else {
					quality = q
				}
			}
		}
		if strings.EqualFold(name, "gzip") {
			return quality > 0
		}
		wildcard = quality > 0
	}
	return wildcard
}

func jsonError(w http.ResponseWriter, code int, msg string) {
	jsonReply(w, code, map[string]string{"error": msg})
}

// csrf is the check for every POST: the Origin must be this route's app
// origin (a missing Origin fails), the X-Dyna header must be set, and the
// body must be JSON. A custom header forces a CORS preflight, and the server
// answers no CORS, so no other site can send one. The SameSite=Strict
// cookie is the third layer.
func csrf(r *http.Request, rt Route) error {
	if r.Method != http.MethodPost {
		return errors.New("use POST")
	}
	if !localRequest(r) && r.Header.Get("Origin") != rt.App {
		return errors.New("the request did not come from the Dyna page")
	}
	if !localRequest(r) && r.Header.Get("X-Dyna") != "1" {
		return errors.New("missing X-Dyna header")
	}
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		return errors.New("the body must be JSON")
	}
	return nil
}

func token(r *http.Request) string {
	c, err := r.Cookie(devicestore.CookieName)
	if err != nil {
		return ""
	}
	return c.Value
}
