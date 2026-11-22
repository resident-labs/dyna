package lifecycle

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

type recordingBrowserRecovery struct {
	mu    sync.Mutex
	calls []browserRecoveryCall
	st    *store.Store
}

type browserRecoveryCall struct {
	profile     string
	box         contract.BoxID
	host        contract.HostName
	checkVolume bool
}

func (r *recordingBrowserRecovery) RecoverBrowserLease(ctx context.Context, lease store.BrowserLeaseRow, host contract.HostName, checkVolume bool) error {
	r.mu.Lock()
	r.calls = append(r.calls, browserRecoveryCall{profile: lease.Profile, box: lease.Box, host: host, checkVolume: checkVolume})
	r.mu.Unlock()
	_, err := r.st.BrowserFinishCheckin(ctx, lease.Profile, lease.Box, lease.Generation)
	return err
}

func TestBrowserLeasePassUnreachableVsStopped(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := t.Context()
	now := time.Unix(1_700_000_000, 0).UTC()

	room := contract.RoomID("room_1")
	if err := st.CreateRoom(ctx, room, "work", now); err != nil {
		t.Fatal(err)
	}
	if err := st.BrowserCreate(ctx, "prof_lost"); err != nil {
		t.Fatal(err)
	}
	if err := st.BrowserCreate(ctx, "prof_stopped"); err != nil {
		t.Fatal(err)
	}

	ev := contract.Event{ID: "e1", Room: room, Time: now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.SessionStarted{}}
	boxEv := contract.Event{ID: "be1", Room: room, Time: now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{}}
	if _, err := st.StartSession(ctx, store.Session{ID: "ses_lost", Room: room, Name: "lost", Profile: "sol", Box: "box_lost"}, "", contract.BoxSizeAgent, "", ev, boxEv, "", false, "", "", 0); err != nil {
		t.Fatal(err)
	}
	ev2 := contract.Event{ID: "e2", Room: room, Time: now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.SessionStarted{}}
	boxEv2 := contract.Event{ID: "be2", Room: room, Time: now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{}}
	if _, err := st.StartSession(ctx, store.Session{ID: "ses_stopped", Room: room, Name: "stopped", Profile: "sol", Box: "box_stopped"}, "", contract.BoxSizeAgent, "", ev2, boxEv2, "", false, "", "", 0); err != nil {
		t.Fatal(err)
	}

	// Checkout prof_lost on box_lost, and prof_stopped on box_stopped.
	if err := st.SetRoomBrowser(ctx, room, "prof_lost"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := st.BrowserCheckout(ctx, "box_lost", "ses_lost", room, now); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRoomBrowser(ctx, room, "prof_stopped"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := st.BrowserCheckout(ctx, "box_stopped", "ses_stopped", room, now); err != nil {
		t.Fatal(err)
	}

	rec := &recordingBrowserRecovery{st: st}
	r := New(st, nil, nil, nil, nil, func() time.Time { return now })
	r.SetBrowserRecovery(rec)

	rows := []store.LifecycleBox{
		{Box: box.Box{ID: "box_lost", Host: "h_down", State: contract.BoxLost, Image: "img"}},
		{Box: box.Box{ID: "box_stopped", Host: "h_up", State: contract.BoxStopped, Image: "img"}},
	}
	hostState := map[contract.HostName]box.HostState{
		"h_down": box.HostUnreachable,
		"h_up":   box.HostReady,
	}
	if err := r.browserLeasePass(ctx, rows, hostState); err != nil {
		t.Fatal(err)
	}
	r.wg.Wait()

	// box_stopped on h_up must be recovered with checkVolume=true; box_lost on h_down must keep its lease.
	rec.mu.Lock()
	calls := append([]browserRecoveryCall(nil), rec.calls...)
	rec.mu.Unlock()
	if len(calls) != 1 || calls[0].profile != "prof_stopped" || calls[0].box != "box_stopped" || calls[0].host != "h_up" || !calls[0].checkVolume {
		t.Fatalf("unexpected recovery calls: %+v", calls)
	}
	if holds, err := st.BoxHoldsBrowserLease(ctx, "box_lost"); err != nil || !holds {
		t.Fatalf("box_lost should keep its lease on unreachable host: holds=%v err=%v", holds, err)
	}
	if holds, err := st.BoxHoldsBrowserLease(ctx, "box_stopped"); err != nil || holds {
		t.Fatalf("box_stopped lease should be released: holds=%v err=%v", holds, err)
	}

	// An abandoned (BoxDeleted) box with a remaining host leftover keeps its lease until the leftover clears.
	if err := st.BrowserCreate(ctx, "prof_abandoned"); err != nil {
		t.Fatal(err)
	}
	ev3 := contract.Event{ID: "e3", Room: room, Time: now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.SessionStarted{}}
	boxEv3 := contract.Event{ID: "be3", Room: room, Time: now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{}}
	if _, err := st.StartSession(ctx, store.Session{ID: "ses_ab", Room: room, Name: "ab", Profile: "sol", Box: "box_ab"}, "", contract.BoxSizeAgent, "", ev3, boxEv3, "", false, "", "", 0); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRoomBrowser(ctx, room, "prof_abandoned"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := st.BrowserCheckout(ctx, "box_ab", "ses_ab", room, now); err != nil {
		t.Fatal(err)
	}
	if err := st.AddHost(ctx, box.Host{Name: "h_down", SSH: "u@down", State: box.HostUnreachable}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddBoxLeftover(ctx, "h_down", room, "box_ab", "dyna-box_ab.slice"); err != nil {
		t.Fatal(err)
	}
	rowsWithAbandoned := []store.LifecycleBox{
		{Box: box.Box{ID: "box_ab", Host: "h_down", State: contract.BoxDeleted, Image: "img"}},
	}
	if err := r.browserLeasePass(ctx, rowsWithAbandoned, hostState); err != nil {
		t.Fatal(err)
	}
	r.wg.Wait()
	if holds, err := st.BoxHoldsBrowserLease(ctx, "box_ab"); err != nil || !holds {
		t.Fatalf("abandoned box with leftover should keep lease: holds=%v err=%v", holds, err)
	}
	if err := st.ForgetLeftover(ctx, store.Leftover{Host: "h_down", Room: room, Box: "box_ab", Slice: "dyna-box_ab.slice"}); err != nil {
		t.Fatal(err)
	}
	if err := r.browserLeasePass(ctx, rowsWithAbandoned, hostState); err != nil {
		t.Fatal(err)
	}
	r.wg.Wait()
	if holds, err := st.BoxHoldsBrowserLease(ctx, "box_ab"); err != nil || holds {
		t.Fatalf("abandoned box lease should release once leftover clears: holds=%v err=%v", holds, err)
	}
}
