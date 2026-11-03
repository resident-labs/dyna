package engine

import (
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// An owner's input from a device that names its host says so in the frame; with no host, or another device, it does not.
func TestOwnerFrameNamesTheDevicesHost(t *testing.T) {
	s, _, worker, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	if err := s.store.AddHost(ctx, box.Host{Name: "omarchy", State: box.HostReady, NoAgents: true, CheckedAt: s.now()}); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100000, 0)
	for i, id := range []string{"dev_laptop", "dev_phone"} {
		if err := s.store.CreateDevice(ctx, store.Device{ID: id, Name: id, TokenHash: [32]byte{byte(i + 1)}, PairedAt: now, LastSeenAt: now, ExpiresAt: s.now().Add(time.Hour), Scopes: store.FullDeviceScopes}); err != nil {
			t.Fatal(err)
		}
	}
	if _, problem := s.DeviceSetHost(ctx, owner, contract.DeviceSetHostRequest{ID: "dev_laptop", Host: "omarchy"}); problem != nil {
		t.Fatal(problem)
	}
	frame := func(device contract.DeviceID) string {
		t.Helper()
		f, _, err := s.inputFrame(ctx, worker, contract.Event{Seq: 1, From: contract.Principal{Kind: contract.PrincipalOwner, Device: device}, Body: contract.PostCreated{Text: "hi"}}, contract.Delivery{Surface: contract.SurfaceFeed, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}})
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	if got := frame("dev_laptop"); got[:len("[Dyna] From the owner, on omarchy, in the room feed.")] != "[Dyna] From the owner, on omarchy, in the room feed." {
		t.Fatalf("laptop: %q", got)
	}
	for _, device := range []contract.DeviceID{"dev_phone", ""} {
		if got := frame(device); got[:len("[Dyna] From the owner, in the room feed.")] != "[Dyna] From the owner, in the room feed." {
			t.Fatalf("%q: %q", device, got)
		}
	}
	// Clearing it takes the host back out.
	if _, problem := s.DeviceSetHost(ctx, owner, contract.DeviceSetHostRequest{ID: "dev_laptop"}); problem != nil {
		t.Fatal(problem)
	}
	if got := frame("dev_laptop"); got[:len("[Dyna] From the owner, in")] != "[Dyna] From the owner, in" {
		t.Fatalf("cleared: %q", got)
	}
}

func TestDeviceSetHostRefusesWhatItCannotName(t *testing.T) {
	s, _, worker, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	if _, problem := s.DeviceSetHost(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: worker}, contract.DeviceSetHostRequest{ID: "d", Host: "host"}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("a session: %+v", problem)
	}
	if _, problem := s.DeviceSetHost(ctx, owner, contract.DeviceSetHostRequest{ID: "d", Host: "nowhere"}); problem == nil || problem.Code != contract.ErrInvalid {
		t.Fatalf("unknown host: %+v", problem)
	}
	if _, problem := s.DeviceSetHost(ctx, owner, contract.DeviceSetHostRequest{ID: "missing", Host: "host"}); problem == nil || problem.Code != contract.ErrInvalid {
		t.Fatalf("unknown device: %+v", problem)
	}
}

func TestAuthenticatedDeviceCarriesItsHost(t *testing.T) {
	s, _, _, ctx := setup(t)
	if err := s.store.AddHost(ctx, box.Host{Name: "omarchy", State: box.HostReady, NoAgents: true, CheckedAt: s.now()}); err != nil {
		t.Fatal(err)
	}
	now := s.now()
	hash := [32]byte{9}
	if err := s.store.CreateDevice(ctx, store.Device{ID: "dev_a", Name: "a", TokenHash: hash, PairedAt: now, LastSeenAt: now, ExpiresAt: s.now().Add(time.Hour), Scopes: store.FullDeviceScopes}); err != nil {
		t.Fatal(err)
	}
	host := func() contract.HostName {
		t.Helper()
		device, valid, err := s.store.AuthenticateDevice(ctx, hash, now)
		if err != nil || !valid {
			t.Fatal(valid, err)
		}
		return device.Host
	}
	if got := host(); got != "" {
		t.Fatalf("before: %q", got)
	}
	if _, problem := s.DeviceSetHost(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.DeviceSetHostRequest{ID: "dev_a", Host: "omarchy"}); problem != nil {
		t.Fatal(problem)
	}
	if got := host(); got != "omarchy" {
		t.Fatalf("after: %q", got)
	}
}
