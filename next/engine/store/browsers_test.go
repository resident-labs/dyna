package store

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestBrowserProfilesLeasesAndRelease(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()

	// Create two rooms and their root sessions.
	roomA := contract.RoomID("room_a")
	roomB := contract.RoomID("room_b")
	if err := s.CreateRoom(ctx, roomA, "alpha", now); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRoom(ctx, roomB, "beta", now); err != nil {
		t.Fatal(err)
	}
	sesA := contract.SessionID("ses_a")
	sesB := contract.SessionID("ses_b")
	boxA := contract.BoxID("box_a")
	boxB := contract.BoxID("box_b")
	evA := contract.Event{ID: "e_a", Room: roomA, Time: now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.SessionStarted{}}
	boxEvA := contract.Event{ID: "be_a", Room: roomA, Time: now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{}}
	evB := contract.Event{ID: "e_b", Room: roomB, Time: now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.SessionStarted{}}
	boxEvB := contract.Event{ID: "be_b", Room: roomB, Time: now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{}}
	if _, err := s.StartSession(ctx, Session{ID: sesA, Room: roomA, Name: "root", Profile: "sol", Box: boxA}, "", contract.BoxSizeAgent, "", evA, boxEvA, "", false, "", "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartSession(ctx, Session{ID: sesB, Room: roomB, Name: "worker", Profile: "sol", Box: boxB}, "", contract.BoxSizeAgent, "", evB, boxEvB, "", false, "", "", 0); err != nil {
		t.Fatal(err)
	}

	// Create profile & duplicate refusal.
	if err := s.BrowserCreate(ctx, "personal"); err != nil {
		t.Fatal(err)
	}
	if err := s.BrowserCreate(ctx, "personal"); !errors.Is(err, ErrBrowserExists) {
		t.Fatalf("duplicate create = %v, want ErrBrowserExists", err)
	}

	// Room without profile checks out empty.
	if prof, gen, holder, err := s.BrowserCheckout(ctx, boxA, sesA, roomA, now); err != nil || prof != "" || gen != 0 || holder != nil {
		t.Fatalf("no-profile checkout: prof=%q gen=%d holder=%+v err=%v", prof, gen, holder, err)
	}

	// Assign to roomA, then roomB.
	if err := s.SetRoomBrowser(ctx, roomA, "personal"); err != nil {
		t.Fatal(err)
	}
	prof, others, err := s.RoomBrowser(ctx, roomA)
	if err != nil || prof != "personal" || len(others) != 0 {
		t.Fatalf("RoomBrowser alone: prof=%q others=%v err=%v", prof, others, err)
	}
	if err := s.SetRoomBrowser(ctx, roomB, "personal"); err != nil {
		t.Fatal(err)
	}
	prof, others, err = s.RoomBrowser(ctx, roomA)
	if err != nil || prof != "personal" || !slices.Equal(others, []string{"beta"}) {
		t.Fatalf("RoomBrowser shared: prof=%q others=%v err=%v", prof, others, err)
	}

	// Remove refused while rooms reference it.
	if err := s.BrowserRemove(ctx, "personal"); !errors.Is(err, ErrBrowserInUse) {
		t.Fatalf("remove while referenced = %v, want ErrBrowserInUse", err)
	}

	// Checkout by boxA succeeds, bumps generation 1 -> 2.
	prof, gen1, holder, err := s.BrowserCheckout(ctx, boxA, sesA, roomA, now)
	if err != nil || prof != "personal" || gen1 != 2 || holder != nil {
		t.Fatalf("checkout A: prof=%q gen=%d holder=%+v err=%v", prof, gen1, holder, err)
	}
	// Idempotent repeat checkout by same boxA keeps gen1.
	prof, genSame, _, err := s.BrowserCheckout(ctx, boxA, sesA, roomA, now.Add(time.Second))
	if err != nil || prof != "personal" || genSame != gen1 {
		t.Fatalf("idempotent checkout A: prof=%q gen=%d err=%v", prof, genSame, err)
	}
	// Second boxB is refused with ErrBrowserLeased and holder metadata.
	_, _, holder, err = s.BrowserCheckout(ctx, boxB, sesB, roomB, now.Add(2*time.Second))
	if !errors.Is(err, ErrBrowserLeased) || holder == nil || holder.Box != boxA || holder.SessionName != "root" || holder.RoomName != "alpha" {
		t.Fatalf("checkout B conflict: holder=%+v err=%v", holder, err)
	}
	// BrowserList reports rooms and active lease.
	list, err := s.BrowserList(ctx)
	if err != nil || len(list) != 1 || list[0].Lease == nil || list[0].Lease.Box != boxA || !slices.Equal(list[0].Rooms, []string{"alpha", "beta"}) {
		t.Fatalf("BrowserList: %+v %v", list, err)
	}
	// Stale generation checkin is refused.
	if ok, err := s.BrowserLeaseMatches(ctx, "personal", boxA, gen1-1); err != nil || ok {
		t.Fatalf("stale matches: ok=%v err=%v", ok, err)
	}
	if ok, err := s.BrowserFinishCheckin(ctx, "personal", boxA, gen1-1); err != nil || ok {
		t.Fatalf("stale finish: ok=%v err=%v", ok, err)
	}
	// Wrong box checkin is refused.
	if ok, err := s.BrowserFinishCheckin(ctx, "personal", boxB, gen1); err != nil || ok {
		t.Fatalf("wrong box finish: ok=%v err=%v", ok, err)
	}
	// Remove refused while leased.
	if err := s.BrowserRemove(ctx, "personal"); !errors.Is(err, ErrBrowserLeased) {
		t.Fatalf("remove while leased = %v, want ErrBrowserLeased", err)
	}

	// Owner release drops lease, bumps generation 2 -> 3, refuses boxA's late checkin.
	released, genAfterRelease, err := s.BrowserRelease(ctx, "personal")
	if err != nil || !released || genAfterRelease != 3 {
		t.Fatalf("BrowserRelease: released=%v gen=%d err=%v", released, genAfterRelease, err)
	}
	if ok, err := s.BrowserLeaseMatches(ctx, "personal", boxA, gen1); err != nil || ok {
		t.Fatalf("late match after release: ok=%v err=%v", ok, err)
	}
	if ok, err := s.BrowserFinishCheckin(ctx, "personal", boxA, gen1); err != nil || ok {
		t.Fatalf("late checkin after release: ok=%v err=%v", ok, err)
	}

	// Now boxB checks out cleanly with monotonic generation 4.
	_, gen2, _, err := s.BrowserCheckout(ctx, boxB, sesB, roomB, now.Add(3*time.Second))
	if err != nil || gen2 != 4 {
		t.Fatalf("checkout B after release: gen=%d err=%v", gen2, err)
	}
	if holds, err := s.BoxHoldsBrowserLease(ctx, boxB); err != nil || !holds {
		t.Fatalf("BoxHoldsBrowserLease B: holds=%v err=%v", holds, err)
	}
	// Matching checkin releases lease.
	if ok, err := s.BrowserFinishCheckin(ctx, "personal", boxB, gen2); err != nil || !ok {
		t.Fatalf("matching checkin B: ok=%v err=%v", ok, err)
	}
	if holds, err := s.BoxHoldsBrowserLease(ctx, boxB); err != nil || holds {
		t.Fatalf("BoxHoldsBrowserLease B after checkin: holds=%v err=%v", holds, err)
	}

	// Unassign rooms and remove profile.
	if err := s.SetRoomBrowser(ctx, roomA, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRoomBrowser(ctx, roomB, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.BrowserRemove(ctx, "personal"); err != nil {
		t.Fatalf("BrowserRemove: %v", err)
	}
}
