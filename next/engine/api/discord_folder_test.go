package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

type discordFolderService struct {
	fakeService
	export contract.DiscordExportRequest
	plan   contract.DiscordPlanRequest
}

func (s *discordFolderService) DiscordExport(_ context.Context, caller contract.Principal, req contract.DiscordExportRequest) (contract.DiscordFolderResponse, *contract.Error) {
	s.caller, s.export = caller, req
	return contract.DiscordFolderResponse{Text: "exported"}, nil
}
func (s *discordFolderService) DiscordPlan(_ context.Context, caller contract.Principal, req contract.DiscordPlanRequest) (contract.DiscordFolderResponse, *contract.Error) {
	s.caller, s.plan = caller, req
	return contract.DiscordFolderResponse{Text: "0 changes"}, nil
}
func TestDiscordFolderThroughOwnerAPI(t *testing.T) {
	service := &discordFolderService{}
	principal := contract.Principal{Kind: contract.PrincipalOwner}
	handler := New(service, nil).handler(Caller{Principal: principal})
	response := request(handler, "/v1/discord.export", `{"room":"work","server":"100","force":true}`)
	if response.Code != http.StatusOK || service.caller != principal || service.export.Room != "work" || service.export.Server != "100" || !service.export.Force {
		t.Fatalf("export: %d %s %+v", response.Code, response.Body, service.export)
	}
	response = request(handler, "/v1/discord.plan", `{"room":"work","server":"100"}`)
	if response.Code != http.StatusOK || service.caller != principal || service.plan.Room != "work" || service.plan.Server != "100" {
		t.Fatalf("plan: %d %s %+v", response.Code, response.Body, service.plan)
	}
}
