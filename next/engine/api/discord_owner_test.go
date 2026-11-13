package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

type discordOwnerService struct {
	fakeService
	policy  contract.DiscordPolicyRequest
	check   contract.DiscordCheckRequest
	mapping contract.RoomDiscordRequest
}

func (s *discordOwnerService) DiscordPolicy(_ context.Context, caller contract.Principal, req contract.DiscordPolicyRequest) (contract.DiscordPolicyResponse, *contract.Error) {
	s.caller = caller
	s.policy = req
	return contract.DiscordPolicyResponse{Text: "front: {}"}, nil
}
func (s *discordOwnerService) DiscordCheck(_ context.Context, caller contract.Principal, req contract.DiscordCheckRequest) (contract.DiscordCheckResponse, *contract.Error) {
	s.caller = caller
	s.check = req
	return contract.DiscordCheckResponse{}, nil
}
func (s *discordOwnerService) RoomDiscord(_ context.Context, caller contract.Principal, req contract.RoomDiscordRequest) (contract.RoomDiscordResponse, *contract.Error) {
	s.caller = caller
	s.mapping = req
	return contract.RoomDiscordResponse{}, nil
}

// Test the API access registry, not just handlers: BoxAccess used to reject the
// owner before either Discord handler could see its PrincipalOwner branch.
func TestDiscordPolicyAndCheckThroughOwnerAPI(t *testing.T) {
	service := &discordOwnerService{}
	for _, caller := range []Caller{{Principal: contract.Principal{Kind: contract.PrincipalOwner}}, {Principal: contract.Principal{Kind: contract.PrincipalSession, Session: "front"}, Box: "box", Room: "work"}} {
		handler := New(service, nil).handler(caller)
		for _, body := range []string{`{"room":"work","server":"100"}`, `{"room":"work","server":"100","text":"front: {}"}`} {
			response := request(handler, "/v1/discord.policy", body)
			if response.Code != http.StatusOK || service.caller != caller.Principal || service.policy.Room != "work" || service.policy.Server != "100" {
				t.Fatalf("policy: %d %s %+v", response.Code, response.Body, service.policy)
			}
		}
		response := request(handler, "/v1/discord.check", `{"room":"work","server":"100"}`)
		if response.Code != http.StatusOK || service.caller != caller.Principal || service.check.Room != "work" || service.check.Server != "100" {
			t.Fatalf("check: %d %s %+v", response.Code, response.Body, service.check)
		}
	}
	// room discord --policy-file first configures the mapping, then uses the same
	// discord.policy endpoint above; both must be admitted on the owner socket.
	handler := New(service, nil).handler(Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}})
	response := request(handler, "/v1/room.discord", `{"room":"work","server":"100"}`)
	if response.Code != http.StatusOK || service.caller.Kind != contract.PrincipalOwner || service.mapping.Room != "work" {
		t.Fatalf("mapping: %d %s", response.Code, response.Body)
	}
}
