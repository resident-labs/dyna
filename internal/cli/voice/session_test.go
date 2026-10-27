package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"dyna-agent/internal/voice/agentwire"
)

// fakeConn is the engine socket. It answers with frames only after the
// command has sent its end line, so a test reads what the command wrote.
type fakeConn struct {
	mu     sync.Mutex
	sent   []agentwire.InMessage
	ended  chan struct{}
	frames []string
}

func newFakeConn(frames ...string) *fakeConn {
	return &fakeConn{ended: make(chan struct{}), frames: frames}
}

func (c *fakeConn) WriteJSON(v any) error {
	m := v.(agentwire.InMessage)
	c.mu.Lock()
	c.sent = append(c.sent, m)
	c.mu.Unlock()
	if m.Type == agentwire.InEnd {
		close(c.ended)
	}
	return nil
}

func (c *fakeConn) ReadMessage() (int, []byte, error) {
	<-c.ended
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.frames) == 0 {
		return 0, nil, io.EOF
	}
	f := c.frames[0]
	c.frames = c.frames[1:]
	return 1, []byte(f), nil
}

func (c *fakeConn) Close() error { return nil }

func TestSessionCommandCopiesLinesBothWays(t *testing.T) {
	dir := t.TempDir()
	png := filepath.Join(dir, "a.png")
	if err := os.WriteFile(png, []byte("\x89PNG\r\n\x1a\n0000"), 0o644); err != nil {
		t.Fatal(err)
	}
	txt := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(txt, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	conn := newFakeConn(`{"type":"ready","rate":48000}`, `{"type":"audio","response_id":"r1","pcm":"AAA="}`, `{"type":"closed","reason":"input-closed"}`)
	stdin := strings.Join([]string{
		`{"type":"audio","pcm":"AQI="}`,
		`{"type":"image","path":"` + png + `"}`,
		`{"type":"image","path":"` + txt + `"}`,
		`not json`,
	}, "\n")
	var stdout bytes.Buffer
	if err := runSession(t.Context(), conn, agentwire.SessionRequest{Rate: 48000}, strings.NewReader(stdin), &stdout); err != nil {
		t.Fatal(err)
	}
	var types []agentwire.MessageType
	for _, m := range conn.sent {
		types = append(types, m.Type)
	}
	if got := types; len(got) != 4 || got[0] != agentwire.InStart || got[1] != agentwire.InAudio || got[2] != agentwire.InImage || got[3] != agentwire.InEnd {
		t.Fatalf("sent line types = %v; want start, audio, image, end (the bad lines stay local)", got)
	}
	if conn.sent[0].Request.Rate != 48000 || conn.sent[2].Mime != "image/png" || len(conn.sent[2].Image) != 12 {
		t.Fatalf("start = %+v, image = %q %d bytes", conn.sent[0].Request, conn.sent[2].Mime, len(conn.sent[2].Image))
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	var local, engine int
	for _, l := range lines {
		var m agentwire.OutMessage
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("stdout line %q is not JSON", l)
		}
		if m.Type == agentwire.OutError {
			local++
		} else {
			engine++
		}
	}
	if local != 2 || engine != 3 {
		t.Fatalf("stdout has %d error lines and %d engine lines:\n%s", local, engine, stdout.String())
	}
}

func TestSessionCommandFailsWhenTheSessionNeverStarts(t *testing.T) {
	conn := newFakeConn(`{"type":"error","message":"the owner turned off agent voice in Settings > Voice"}`, `{"type":"closed","reason":"error"}`)
	var stdout bytes.Buffer
	err := runSession(context.Background(), conn, agentwire.SessionRequest{}, strings.NewReader(""), &stdout)
	if err == nil || err.Error() != "the owner turned off agent voice in Settings > Voice" || stdout.Len() != 0 {
		t.Fatalf("err = %v, stdout = %q", err, stdout.String())
	}
	if errors.Is(err, io.EOF) {
		t.Fatal("the error hides the engine's message")
	}
}
