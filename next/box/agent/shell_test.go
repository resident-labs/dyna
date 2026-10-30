package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type shellFake struct {
	command, cwd string
	timeout      int
}

func (f *shellFake) Run(_ context.Context, command, cwd string, timeout int) (string, error) {
	f.command, f.cwd, f.timeout = command, cwd, timeout
	return "local output", nil
}

func TestShellRunsLocalCommand(t *testing.T) {
	input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"shell","arguments":{"command":"pwd","cwd":"/tmp"}}}` + "\n")
	var output bytes.Buffer
	fake := &shellFake{}
	if err := Shell(context.Background(), fake, input, &output); err != nil {
		t.Fatal(err)
	}
	var response struct {
		ID     int `json:"id"`
		Result struct {
			Content []shellText `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if fake.command != "pwd" || fake.cwd != "/tmp" || fake.timeout != 600 || response.ID != 1 || len(response.Result.Content) != 1 || response.Result.Content[0].Text != "local output" {
		t.Fatalf("command=%q cwd=%q response=%+v", fake.command, fake.cwd, response)
	}
}
