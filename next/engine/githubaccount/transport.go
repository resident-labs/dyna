package githubaccount

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Transport labels GitHub access failures without retrying under another identity.
// Successful bodies, including git's streaming protocol, are left untouched.
type Transport struct {
	Base         http.RoundTripper
	Label, Token string
}

func (t Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := t.Base.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("%s: GitHub request unavailable", t.Label)
	}
	if response.StatusCode < 400 {
		return response, nil
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	_ = response.Body.Close()
	if err != nil {
		data = []byte(http.StatusText(response.StatusCode))
	}
	if t.Token != "" {
		data = bytes.ReplaceAll(data, []byte(t.Token), []byte("***"))
	}
	message := string(data)
	var payload map[string]json.RawMessage
	if json.Unmarshal(data, &payload) == nil && payload != nil {
		var original string
		_ = json.Unmarshal(payload["message"], &original)
		if original == "" {
			original = http.StatusText(response.StatusCode)
		}
		payload["message"], _ = json.Marshal(t.Label + ": " + strings.ReplaceAll(original, t.Token, "***"))
		data, _ = json.Marshal(payload)
	} else {
		data = []byte(t.Label + ": " + message)
	}
	response.Body = io.NopCloser(bytes.NewReader(data))
	response.ContentLength = int64(len(data))
	response.Header.Del("Content-Length")
	return response, nil
}
