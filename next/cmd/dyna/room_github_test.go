package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

type githubCLIClient struct {
	client
	request contract.RoomGitHubRequest
}

func (c *githubCLIClient) RoomGitHub(_ context.Context, req contract.RoomGitHubRequest) (contract.RoomGitHubResponse, error) {
	c.request = req
	return contract.RoomGitHubResponse{Login: "work", Name: req.Name, Email: req.Email}, nil
}
func TestRoomGitHubTokenOnlyOnStdin(t *testing.T) {
	service := &githubCLIClient{}
	cmd := roomGitHubCommand(func() client { return service })
	output := new(bytes.Buffer)
	cmd.SetOut(output)
	cmd.SetErr(output)
	cmd.SetIn(strings.NewReader("private-token\n"))
	cmd.SetArgs([]string{"work-room", "--name", "Work User", "--email", "work@example.com"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if service.request.Token != "private-token" {
		t.Fatal("stdin token not passed")
	}
	if strings.Contains(output.String(), "private-token") || !strings.Contains(output.String(), "GitHub account: work") {
		t.Fatal("unsafe output")
	}
}
