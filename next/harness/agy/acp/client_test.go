package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReadLineDropsOversizedLinesAndKeepsGoing(t *testing.T) {
	input := "short\n" + strings.Repeat("x", 100000) + "\nafter\r\nunfinished"
	r := bufio.NewReaderSize(strings.NewReader(input), 16)
	var got []string
	for {
		line, err := ReadLine(r, 1000)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatal(err)
			}
			break
		}
		got = append(got, string(line))
	}
	if strings.Join(got, "|") != "short|after" {
		t.Fatalf("lines = %q", got)
	}
}

func TestCallsRequestsAndNotifications(t *testing.T) {
	agentIn, clientOut := io.Pipe()
	clientIn, agentOut := io.Pipe()
	notes := make(chan string, 1)
	c := NewClient(clientIn, clientOut, Handler{Notify: func(method string, _ json.RawMessage) { notes <- method }})
	go func() {
		reader := bufio.NewReader(agentIn)
		line, _ := reader.ReadBytes('\n')
		var call struct {
			ID     int64
			Method string
		}
		json.Unmarshal(line, &call)
		io.WriteString(agentOut, `{"jsonrpc":"2.0","method":"session/update","params":{}}`+"\n")
		io.WriteString(agentOut, `{"jsonrpc":"2.0","id":"7","method":"fs/read_text_file","params":{}}`+"\n")
		reply, _ := reader.ReadBytes('\n')
		if !strings.Contains(string(reply), `"id":"7"`) || !strings.Contains(string(reply), `"code":-32601`) {
			t.Errorf("reply to the agent's request = %s", reply)
		}
		io.WriteString(agentOut, `{"jsonrpc":"2.0","id":`+string(rune('0'+call.ID))+`,"result":{"ok":true}}`+"\n")
		agentOut.Close()
	}()
	var out struct{ OK bool }
	if err := c.Call(context.Background(), "ping", map[string]any{}, &out); err != nil || !out.OK {
		t.Fatalf("call = %v %+v", err, out)
	}
	if got := <-notes; got != "session/update" {
		t.Fatalf("notification %q", got)
	}
	<-c.Done()
	if err := c.Call(context.Background(), "ping", nil, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("call after close = %v", err)
	}
}
