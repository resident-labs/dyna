package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

type folderCLIClient struct {
	client
	export contract.DiscordExportRequest
	plan   contract.DiscordPlanRequest
}

func (service *folderCLIClient) DiscordExport(_ context.Context, req contract.DiscordExportRequest) (contract.DiscordFolderResponse, error) {
	service.export = req
	return contract.DiscordFolderResponse{Text: "Exported"}, nil
}
func (service *folderCLIClient) DiscordPlan(_ context.Context, req contract.DiscordPlanRequest) (contract.DiscordFolderResponse, error) {
	service.plan = req
	return contract.DiscordFolderResponse{Text: "0 changes"}, nil
}
func TestDiscordFolderCLI(t *testing.T) {
	for _, action := range []string{"export", "plan"} {
		for _, roomArgs := range [][]string{{"work"}, {"--room", "work"}} {
			service := &folderCLIClient{}
			command := discordCommand(func() client { return service })
			output := new(bytes.Buffer)
			command.SetOut(output)
			args := append([]string{action}, roomArgs...)
			args = append(args, "--server", "100")
			if action == "export" {
				args = append(args, "--force")
			}
			command.SetArgs(args)
			if err := command.Execute(); err != nil {
				t.Fatal(err)
			}
			if action == "export" && (service.export.Room != "work" || service.export.Server != "100" || !service.export.Force || output.String() != "Exported\n") {
				t.Fatalf("export %+v: %s", service.export, output)
			}
			if action == "plan" && (service.plan.Room != "work" || service.plan.Server != "100" || output.String() != "0 changes\n") {
				t.Fatalf("plan %+v: %s", service.plan, output)
			}
		}
	}
}
