package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	hostbox "github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine"
	"github.com/resident-labs/dyna/engine/store"
)

type instructionsService struct {
	*fakeService
	core *engine.Service
}

func (s instructionsService) InstructionsGet(ctx context.Context, caller contract.Principal, req contract.InstructionsGetRequest) (contract.InstructionsGetResponse, *contract.Error) {
	return s.core.InstructionsGet(ctx, caller, req)
}
func (s instructionsService) InstructionsSet(ctx context.Context, caller contract.Principal, req contract.InstructionsSetRequest) (contract.InstructionsSetResponse, *contract.Error) {
	return s.core.InstructionsSet(ctx, caller, req)
}

// From its box, a room's root reaches the global file and another room's: the gate does not pin instructions to its
// room. A child's box is refused with the reason.
func TestARootEditsInstructionsFromItsBox(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.AddHost(ctx, hostbox.Host{Name: "host", State: hostbox.HostReady, CPUs: 32, MemBytes: 64 * hostbox.GiB, DiskBytes: 100 * hostbox.GiB, CheckedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	core := engine.New(st, nil, questionProfiles{}, time.Now)
	core.SetLayout(hostbox.Layout{Data: t.TempDir(), HostData: t.TempDir()})
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	horizon, problem := core.RoomCreate(ctx, owner, contract.RoomCreateRequest{Name: "horizon", RootProfile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	other, problem := core.RoomCreate(ctx, owner, contract.RoomCreateRequest{Name: "other", RootProfile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	var root contract.SessionID
	if err := st.Read().QueryRowContext(ctx, "SELECT id FROM sessions WHERE room=? AND parent IS NULL", horizon.Room).Scan(&root); err != nil {
		t.Fatal(err)
	}
	worker, problem := core.SessionStart(ctx, owner, contract.SessionStartRequest{Room: horizon.Room, Name: "worker", Profile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	a := New(instructionsService{fakeService: &fakeService{}, core: core}, nil)
	rootBox := a.handler(Caller{Principal: contract.Principal{Kind: contract.PrincipalSession, Session: root}, Box: "box-root", Room: horizon.Room})
	workerBox := a.handler(Caller{Principal: contract.Principal{Kind: contract.PrincipalSession, Session: worker.Session}, Box: "box-worker", Room: horizon.Room})

	for _, target := range []contract.RoomID{"", other.Room} {
		body, _ := json.Marshal(contract.InstructionsGetRequest{Room: target})
		got := request(rootBox, "/v1/instructions.get", string(body))
		if got.Code != http.StatusOK {
			t.Fatalf("%q get: %d %s", target, got.Code, got.Body.String())
		}
		var read contract.InstructionsGetResponse
		if err := json.Unmarshal(got.Body.Bytes(), &read); err != nil {
			t.Fatal(err)
		}
		body, _ = json.Marshal(contract.InstructionsSetRequest{Room: target, Text: "standing rules", Base: read.Hash})
		if saved := request(rootBox, "/v1/instructions.set", string(body)); saved.Code != http.StatusOK {
			t.Fatalf("%q set: %d %s", target, saved.Code, saved.Body.String())
		}
		if again := request(rootBox, "/v1/instructions.set", string(body)); again.Code != http.StatusConflict {
			t.Fatalf("%q stale set: %d %s", target, again.Code, again.Body.String())
		}
		// The save landed in the room it named, not the root's own.
		seen, problem := core.InstructionsGet(ctx, owner, contract.InstructionsGetRequest{Room: target})
		if problem != nil || seen.Text != "standing rules" || seen.SavedBy == nil || seen.SavedBy.Name != "root" || seen.SavedBy.RoomName != "horizon" {
			t.Fatalf("%q owner view: %+v %v", target, seen, problem)
		}
	}
	if own, _ := core.InstructionsGet(ctx, owner, contract.InstructionsGetRequest{Room: horizon.Room}); own.Text != "" {
		t.Fatalf("the root's own room changed: %+v", own)
	}

	refused := request(workerBox, "/v1/instructions.get", `{}`)
	var reason contract.Error
	if refused.Code != http.StatusForbidden || json.Unmarshal(refused.Body.Bytes(), &reason) != nil || reason.Message != "only the owner or a room's root edits instructions" {
		t.Fatalf("child get: %d %s", refused.Code, refused.Body.String())
	}
	if got := request(workerBox, "/v1/instructions.set", `{"text":"mine","base":""}`); got.Code != http.StatusForbidden {
		t.Fatalf("child set: %d %s", got.Code, got.Body.String())
	}
}
