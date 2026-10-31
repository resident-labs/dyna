//go:build dynaeval

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
)

type evalRecord struct {
	Session     string `json:"session"`
	Host        string `json:"host"`
	Path        string `json:"path"`
	Ordinal     uint64 `json:"ordinal"`
	ToolsSHA256 string `json:"tools_sha256"`
}

type evalSink struct {
	mu       sync.Mutex
	file     *os.File
	ordinals map[string]uint64
}
type evalTransport struct {
	next    http.RoundTripper
	sink    *evalSink
	session string
}

func (t evalTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body == nil {
		return t.next.RoundTrip(req)
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, errors.New("evaluation: cannot read request body")
	}
	_ = req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(body))
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return t.next.RoundTrip(req)
	}
	sum := sha256.Sum256(fields["tools"])
	t.sink.mu.Lock()
	t.sink.ordinals[t.session]++
	record := evalRecord{t.session, req.URL.Host, req.URL.Path, t.sink.ordinals[t.session], hex.EncodeToString(sum[:])}
	err = json.NewEncoder(t.sink.file).Encode(record)
	t.sink.mu.Unlock()
	if err != nil {
		return nil, errors.New("evaluation: cannot write hash record")
	}
	return t.next.RoundTrip(req)
}
func proxyEvalMiddleware() (gin.HandlerFunc, func(), error) {
	path := os.Getenv("DYNA_EVAL_HASH_FILE")
	if path == "" {
		return nil, func() {}, nil
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		return nil, nil, errors.New("evaluation: cannot open hash sink")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		file.Close()
		return nil, nil, errors.New("evaluation: hash sink must be a private regular file")
	}
	sink := &evalSink{file: file, ordinals: make(map[string]uint64)}
	return func(c *gin.Context) {
		session := c.GetHeader("X-Claude-Code-Session-Id")
		if session != "" {
			ctx := httptransport.WithDecorator(c.Request.Context(), func(next http.RoundTripper) http.RoundTripper {
				return evalTransport{next: next, sink: sink, session: session}
			})
			c.Request = c.Request.WithContext(ctx)
		}
		c.Next()
	}, func() { file.Close() }, nil
}
