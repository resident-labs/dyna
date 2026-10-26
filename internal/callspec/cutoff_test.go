package callspec

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"testing"
)

func TestIsCutOff(t *testing.T) {
	// net/http can report a connection the server closed at once as this error,
	// instead of io.EOF, depending on which goroutine sees the close first.
	idle := &url.Error{Op: "Post", URL: "http://engine/api/StartSubagent", Err: errors.New("http: server closed idle connection")}
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"eof":                {fmt.Errorf("send: %w", io.EOF), true},
		"server closed idle": {idle, true},
		"other error":        {errors.New("engine: 500 Internal Server Error"), false},
		"nil":                {nil, false},
	} {
		if got := IsCutOff(tc.err); got != tc.want {
			t.Errorf("%s: IsCutOff = %v, want %v", name, got, tc.want)
		}
	}
}
