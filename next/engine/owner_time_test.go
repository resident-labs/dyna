package engine

import (
	"github.com/resident-labs/dyna/contract"
	"strings"
	"testing"
	"time"
)

func TestInputFrameTimes(t *testing.T) {
	s, room, _, worker, kid, ctx := receiveTree(t)
	at := time.Date(2026, 11, 15, 3, 52, 0, 0, time.UTC)
	delivery := contract.Delivery{Surface: contract.SurfaceSession, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}}
	for _, tc := range []struct {
		name string
		from contract.Principal
		body contract.EventBody
	}{
		{"owner", contract.Principal{Kind: contract.PrincipalOwner}, contract.PostCreated{Text: "hi"}},
		{"child", asSession(kid), contract.TurnEnded{}},
		{"notice", contract.Principal{Kind: contract.PrincipalEngine}, contract.SessionNotice{}},
		{"discord", contract.Principal{Kind: contract.PrincipalEngine}, contract.DiscordMessage{Owner: true}},
		{"discord files", contract.Principal{Kind: contract.PrincipalEngine}, contract.DiscordFilesRetrieved{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := contract.Event{Time: at, Room: room, From: tc.from, Body: tc.body}
			frame, _, err := s.inputFrameAt(ctx, worker, event, at, delivery)
			if err != nil || !strings.HasPrefix(frame, "[Dyna] 03:52 UTC. ") || strings.Contains(frame, "previous message") {
				t.Fatalf("%s %v", frame, err)
			}
			t.Log(frame)
			frame, _, err = s.inputFrameAt(ctx, worker, event, at.Add(24*time.Hour), delivery)
			if err != nil || !strings.HasPrefix(frame, "[Dyna] Nov 15 03:52 UTC. ") {
				t.Fatalf("%s %v", frame, err)
			}
		})
	}
	zone := "America/Chicago"
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	if _, e := s.OwnerTimezone(ctx, asSession(worker), contract.OwnerTimezoneRequest{Zone: &zone}); e == nil {
		t.Fatal("session changed zone")
	}
	bad := "Local"
	if _, e := s.OwnerTimezone(ctx, owner, contract.OwnerTimezoneRequest{Zone: &bad}); e == nil {
		t.Fatal("accepted Local")
	}
	bad = "Not/AZone"
	if _, e := s.OwnerTimezone(ctx, owner, contract.OwnerTimezoneRequest{Zone: &bad}); e == nil {
		t.Fatal("accepted invalid zone")
	}
	if _, e := s.OwnerTimezone(ctx, owner, contract.OwnerTimezoneRequest{Zone: &zone}); e != nil {
		t.Fatal(e)
	}
	clock, err := s.frameClock(ctx, at, at)
	if err != nil || clock != "03:52 UTC (21:52 owner time)" {
		t.Fatalf("%s %v", clock, err)
	}
	zone = ""
	if _, e := s.OwnerTimezone(ctx, owner, contract.OwnerTimezoneRequest{Zone: &zone}); e != nil {
		t.Fatal(e)
	}
	clock, err = s.frameClock(ctx, at, at)
	if err != nil || clock != "03:52 UTC" {
		t.Fatalf("%s %v", clock, err)
	}
}

func TestOwnerGapAcrossSurfacesAndBatchedInputs(t *testing.T) {
	s, room, _, worker, _, ctx := receiveTree(t)
	at := time.Date(2026, 11, 15, 0, 10, 0, 0, time.UTC)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	post := func(id string, when time.Time, body contract.EventBody, from contract.Principal, surface contract.Surface) contract.Event {
		event := contract.Event{ID: contract.EventID(id), Time: when, Room: room, From: from, Body: body}
		d := contract.Delivery{To: asSession(worker), Surface: surface, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}}
		seq, err := s.store.Post(ctx, event, []contract.Delivery{d})
		if err != nil {
			t.Fatal(err)
		}
		event.Seq = seq
		return event
	}
	post("time-first", at.Add(-92*time.Minute), contract.PostCreated{Text: "first"}, owner, contract.SurfaceDM)
	event := post("time-second", at, contract.DiscordMessage{Owner: true}, contract.Principal{Kind: contract.PrincipalEngine}, contract.SurfaceFeed)
	later := post("time-third", at.Add(10*time.Minute), contract.PostCreated{Text: "third"}, owner, contract.SurfaceFeed)
	delivery := contract.Delivery{Surface: contract.SurfaceFeed, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}}
	for _, e := range []contract.Event{event, later} {
		frame, _, err := s.inputFrameAt(ctx, worker, e, at.Add(24*time.Hour), delivery)
		if err != nil {
			t.Fatal(err)
		}
		if e.Seq == event.Seq && (!strings.Contains(frame, "The owner's previous message was 1 h 32 m ago.") || !strings.HasPrefix(frame, "[Dyna] Nov 15 00:10 UTC.")) {
			t.Fatal(frame)
		}
		if e.Seq == later.Seq && strings.Contains(frame, "previous message") {
			t.Fatal(frame)
		}
		t.Log(frame)
	}
}
