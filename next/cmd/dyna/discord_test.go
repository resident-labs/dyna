package main

import (
	"bytes"
	"context"
	"github.com/resident-labs/dyna/contract"
	"strings"
	"testing"
)

type discordCLIClient struct {
	client
	policy contract.DiscordPolicyRequest
	check  contract.DiscordCheckRequest
	bot    contract.DiscordConfigureRequest
	route  contract.RoomDiscordRequest
}

func (c *discordCLIClient) DiscordConfigure(_ context.Context, r contract.DiscordConfigureRequest) (contract.DiscordConfigureResponse, error) {
	c.bot = r
	return contract.DiscordConfigureResponse{BotName: "Kira"}, nil
}
func (c *discordCLIClient) RoomDiscord(_ context.Context, r contract.RoomDiscordRequest) (contract.RoomDiscordResponse, error) {
	c.route = r
	return contract.RoomDiscordResponse{}, nil
}
func TestDiscordConfigureTokenOnlyOnStdin(t *testing.T) {
	for _, input := range []string{"private-token\n", ""} {
		service := &discordCLIClient{}
		cmd := discordCommand(func() client { return service })
		output := new(bytes.Buffer)
		cmd.SetOut(output)
		cmd.SetErr(output)
		cmd.SetIn(strings.NewReader(input))
		cmd.SetArgs([]string{"--owner", "123"})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if service.bot.Owner == nil || *service.bot.Owner != "123" || service.bot.Token != strings.TrimSpace(input) {
			t.Fatalf("wrong config %+v", service.bot)
		}
		if strings.Contains(output.String(), "private-token") {
			t.Fatal("token in output")
		}
	}
}
func TestDiscordInstructionsOnStdin(t *testing.T) {
	for _, input := range []string{"Be cordial.", ""} {
		service := &discordCLIClient{}
		cmd := roomDiscordCommand(func() client { return service })
		cmd.SetOut(new(bytes.Buffer))
		cmd.SetIn(strings.NewReader(input))
		cmd.SetArgs([]string{"work", "--server", "100", "--channel", "200", "--instructions-file", "-"})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if service.route.Instructions == nil || *service.route.Instructions != input || service.route.Server != "100" || service.route.Channel != "200" {
			t.Fatalf("wrong route %+v", service.route)
		}
	}
}

func (c *discordCLIClient) DiscordPolicy(_ context.Context, req contract.DiscordPolicyRequest) (contract.DiscordPolicyResponse, error) {
	c.policy = req
	return contract.DiscordPolicyResponse{}, nil
}
func (c *discordCLIClient) DiscordCheck(_ context.Context, req contract.DiscordCheckRequest) (contract.DiscordCheckResponse, error) {
	c.check = req
	return contract.DiscordCheckResponse{}, nil
}
func TestDiscordOwnerRoomFlags(t *testing.T) {
	for _, action := range []string{"policy", "check"} {
		for _, room := range []string{"work", "room_123"} {
			service := &discordCLIClient{}
			cmd := discordCommand(func() client { return service })
			cmd.SetOut(new(bytes.Buffer))
			cmd.SetArgs([]string{action, "--room", room, "--server", "100"})
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if action == "policy" && (service.policy.Room != contract.RoomID(room) || service.policy.Server != "100") {
				t.Fatalf("policy %+v", service.policy)
			}
			if action == "check" && (service.check.Room != contract.RoomID(room) || service.check.Server != "100") {
				t.Fatalf("check %+v", service.check)
			}
		}
	}
}
func TestDiscordOwnerPolicyFileUsesPolicyEndpoint(t *testing.T) {
	service := &discordCLIClient{}
	cmd := roomDiscordCommand(func() client { return service })
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetIn(strings.NewReader("front: {}"))
	cmd.SetArgs([]string{"work", "--server", "100", "--policy-file", "-"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if service.policy.Room != "work" || service.policy.Server != "100" || service.policy.Text == nil || *service.policy.Text != "front: {}" {
		t.Fatalf("wrong policy %+v", service.policy)
	}
}
