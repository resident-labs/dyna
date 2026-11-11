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
	bot   contract.DiscordConfigureRequest
	route contract.RoomDiscordRequest
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
